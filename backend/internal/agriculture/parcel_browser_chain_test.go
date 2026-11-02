package agriculture

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

const parcelBrowserChecks = "production_nginx_configuration_valid real_react_login_shared_vue_session http_only_platform_cookie explicit_parent_parcel_capabilities create_preview_bound_to_parent duplicate_click_single_create parcel_survives_reload current_geometry_crs_area_hash ui_revision_two_same_identity historical_revision_one_readonly historical_at_selector_version_one current_revision_restored concurrent_api_revision_three stale_revision_409_preserves_draft foreign_workspace_denied same_tenant_other_workspace_denied same_cookie_after_revocation revoked_old_page_clears_content revoked_history_navigation_no_content all_database_checkpoints_acknowledged no_internal_credentials_identity_spoofing_or_external_requests"
const parcelBrowserStages = "preflight login_navigation login_account login_password login_submit workspace parent_open parcel_open create_form create_preview create_confirm create_reload revision_form revision_confirm history at_time race scope_negatives revocation complete"

func parcelBrowserSnapshot(t *testing.T, f *farmChainFixture) map[string]string {
	t.Helper()
	out := f.snapshot(t)
	for key, value := range parcelChainSnapshot(t, f) {
		out[key] = value
	}
	return out
}

func TestParcelBrowserPostgresChain(t *testing.T) {
	for _, key := range []string{"CHROME_BIN", "NGINX_BIN", "NGINX_MIME_TYPES", "REDIS_URL", "AGRI_IDENTITY_PYTHON", "AGRI_TEST_PG_RUNTIME_DSN", "AGRI_TEST_PG_MIGRATION_DSN", "AGRI_TEST_GEO_RUNTIME_DSN", "AGRI_TEST_GEO_MIGRATION_DSN"} {
		if os.Getenv(key) == "" {
			if os.Getenv("AGRI_PARCEL_CHAIN_REQUIRED") == "1" {
				t.Fatal("required parcel browser dependency absent")
			}
			t.Skip("actual parcel browser dependencies not configured")
		}
	}
	t.Setenv("AGRI_FARM_AUTH_COMPONENT_ONLY", "0")
	t.Setenv("AGRI_FARM_CHAIN_REQUIRED", "1")
	stage := "fixture"
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("PARCEL_BROWSER_DIAGNOSTIC stage=" + stage)
		}
	})
	f := newFarmChainFixture(t, true)
	farmID, _ := f.create(t, "parcel-browser-parent", "fixture-north")
	stage = "explicit_grants"
	parcelChainGrant(t, f, farmID)
	status, _ := f.request(t, f.actorCookie, "GET", "/api/agri/v1/farms/"+farmID+"/parcel-context?workspace_id="+f.org.Workspace.ID, nil, "")
	if status != 200 {
		t.Fatalf("real parcel context preflight status=%d", status)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required")
	}
	reportPath := filepath.Join(f.private, "parcel-browser-report.json")
	allowed := map[string]bool{}
	for _, name := range strings.Fields(parcelBrowserChecks) {
		allowed[name] = true
	}
	stages := map[string]bool{}
	for _, name := range strings.Fields(parcelBrowserStages) {
		stages[name] = true
	}
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		raw, e := os.ReadFile(reportPath)
		if e != nil || len(raw) > 1<<20 {
			return
		}
		var v struct {
			Result    string          `json:"result"`
			Stage     string          `json:"stage"`
			UIFlags   map[string]bool `json:"ui_flags"`
			ErrorCode *string         `json:"error_code"`
			Results   []struct {
				Name   string `json:"name"`
				Result string `json:"result"`
			} `json:"results"`
		}
		if json.Unmarshal(raw, &v) != nil || !stages[v.Stage] {
			return
		}
		seen := map[string]bool{}
		for _, r := range v.Results {
			if !allowed[r.Name] || seen[r.Name] || r.Result != "pass" {
				return
			}
			seen[r.Name] = true
		}
		flags := map[string]bool{}
		for _, key := range strings.Fields("login session_ready context create detail editor preview busy conflict") {
			if value, ok := v.UIFlags[key]; ok {
				flags[key] = value
			}
		}
		if v.ErrorCode != nil {
			known := false
			for _, code := range strings.Fields("REVISION_CONFLICT STATE_CONFLICT INVALID_GEOMETRY INVALID_ARGUMENT INVALID_RESPONSE DEPENDENCY_UNAVAILABLE AUTHORITY_UNAVAILABLE NETWORK_ERROR TIMEOUT RESOURCE_NOT_FOUND FORBIDDEN") {
				if *v.ErrorCode == code {
					known = true
				}
			}
			if !known {
				return
			}
		}
		clean, _ := json.Marshal(map[string]any{"result": "fail", "stage": v.Stage, "results": v.Results, "diagnostic_only": true, "data_class": "fixture", "production_acceptance": false, "ui_flags": flags, "error_code": v.ErrorCode})
		dir := filepath.Join(f.root, ".local/evidence")
		if os.MkdirAll(dir, 0700) == nil {
			_ = os.WriteFile(filepath.Join(dir, "parcel-chain-browser.json"), clean, 0600)
		}
	})
	config := filepath.Join(f.private, "parcel-browser.json")
	chainPrivate(t, config, map[string]any{"platform_url": f.server.URL, "gateway_port": f.gatewayPort, "username": f.actor, "password": f.password, "workspace_id": f.org.Workspace.ID, "foreign_workspace_id": f.foreign.Workspace.ID, "second_workspace_id": f.second, "farm_id": farmID, "report_path": reportPath})
	stage = "child_start"
	child := chainStart(t, node, f.root, filepath.Join(f.root, "scripts/agriculture/parcel-browser-e2e.mjs"), config)
	var stableID string
	var immutable []string
	var frozen map[string]string
	for _, checkpoint := range []string{"created", "revised", "raced", "revoke-request", "revoked-verified"} {
		stage = "wait_" + checkpoint
		var event struct {
			Event      string `json:"event"`
			Checkpoint string `json:"checkpoint"`
		}
		chainLine(t, child, 90*time.Second, &event)
		if event.Event != "checkpoint" || event.Checkpoint != checkpoint {
			t.Fatal("unexpected parcel browser checkpoint")
		}
		stage = "verify_" + checkpoint
		if checkpoint == "created" || checkpoint == "revised" || checkpoint == "raced" {
			want := int64(1)
			if checkpoint == "revised" {
				want = 2
			}
			if checkpoint == "raced" {
				want = 3
			}
			if err := f.geo.Transaction(func(tx *gorm.DB) error {
				if e := tx.Exec("SELECT set_config('app.tenant_ids',?,true),set_config('app.workspace_id',?,true)", f.org.Tenant.ID, f.org.Workspace.ID).Error; e != nil {
					return e
				}
				var rows []struct {
					ParcelID        string
					FarmID          string
					CurrentRevision int64
					ExternalCode    string
					DataClass       string
				}
				if e := tx.Raw("SELECT parcel_id,farm_id,current_revision,external_code,data_class FROM agriculture_parcels WHERE tenant_id=? AND workspace_id=?", f.org.Tenant.ID, f.org.Workspace.ID).Scan(&rows).Error; e != nil {
					return e
				}
				if len(rows) != 1 || rows[0].FarmID != farmID || rows[0].CurrentRevision != want || rows[0].ExternalCode != "browser-parcel" || rows[0].DataClass != "demo" {
					t.Fatal("actual parcel identity differs from browser lifecycle")
				}
				if stableID == "" {
					stableID = rows[0].ParcelID
				}
				if rows[0].ParcelID != stableID {
					t.Fatal("parcel stable identity replaced")
				}
				var history []struct {
					Revision int64
					ActorID  string
					Raw      string
				}
				if e := tx.Raw("SELECT revision,actor_id,to_jsonb(r)::text AS raw FROM agriculture_parcel_revisions r WHERE tenant_id=? AND workspace_id=? AND parcel_id=? ORDER BY revision", f.org.Tenant.ID, f.org.Workspace.ID, stableID).Scan(&history).Error; e != nil {
					return e
				}
				if len(history) != int(want) {
					t.Fatal("immutable revision count differs")
				}
				for i, row := range history {
					if row.Revision != int64(i+1) || row.ActorID != f.actor {
						t.Fatal("revision chronology or trusted actor differs")
					}
					if i < len(immutable) && immutable[i] != row.Raw {
						t.Fatal("historical parcel revision mutated")
					}
					if i == len(immutable) {
						immutable = append(immutable, row.Raw)
					}
				}
				for _, table := range []string{"agriculture_parcel_commands", "agriculture_parcel_outbox"} {
					var count int64
					if e := tx.Raw("SELECT count(*) FROM "+table+" WHERE tenant_id=? AND workspace_id=? AND parcel_id=?", f.org.Tenant.ID, f.org.Workspace.ID, stableID).Scan(&count).Error; e != nil {
						return e
					}
					if count != want {
						t.Fatal("parcel receipt or outbox not exactly one per revision")
					}
				}
				return nil
			}); err != nil {
				t.Fatalf("parcel database checkpoint failed: %T", err)
			}
		}
		if checkpoint == "revoke-request" {
			suspended := "suspended"
			if e := f.platform.Service.ChangeMember(context.Background(), f.owner, f.org.Workspace.ID, f.actor, nil, &suspended); e != nil {
				t.Fatal("real parcel browser revocation failed")
			}
			frozen = parcelBrowserSnapshot(t, f)
		}
		if checkpoint == "revoked-verified" && !reflect.DeepEqual(frozen, parcelBrowserSnapshot(t, f)) {
			t.Fatal("revoked browser changed parcel business data")
		}
		ack, _ := json.Marshal(map[string]string{"event": "ack", "checkpoint": checkpoint})
		if _, e := child.input.Write(append(ack, '\n')); e != nil {
			t.Fatal("parcel checkpoint acknowledgement failed")
		}
	}
	stage = "child_finish"
	select {
	case <-child.done:
		if child.err != nil {
			t.Fatal("parcel browser child failed")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("parcel browser did not finish")
	}
	stage = "report_validate"
	raw, err := os.ReadFile(reportPath)
	if err != nil || len(raw) > 1<<20 {
		t.Fatal("parcel browser report unavailable")
	}
	var report struct {
		Result  string `json:"result"`
		Stage   string `json:"stage"`
		Results []struct {
			Name   string `json:"name"`
			Result string `json:"result"`
		} `json:"results"`
	}
	if json.Unmarshal(raw, &report) != nil || report.Result != "pass" || report.Stage != "complete" || len(report.Results) != len(allowed) {
		t.Fatal("parcel browser required report not passed")
	}
	seen := map[string]bool{}
	for _, r := range report.Results {
		if !allowed[r.Name] || seen[r.Name] || r.Result != "pass" {
			t.Fatal("parcel browser required assertions differ")
		}
		seen[r.Name] = true
	}
	for _, secret := range []string{f.password, f.actorCookie.Value, f.ownerCookie.Value, os.Getenv("AGRI_TEST_PG_RUNTIME_DSN"), os.Getenv("AGRI_TEST_GEO_RUNTIME_DSN")} {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("private material in parcel report")
		}
	}
	dir := filepath.Join(f.root, ".local/evidence")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal("parcel evidence directory unavailable")
	}
	if err = os.WriteFile(filepath.Join(dir, "parcel-chain-browser.json"), raw, 0600); err != nil {
		t.Fatal("parcel evidence write failed")
	}
}
