package agriculture

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

const assetBrowserChecks = "production_nginx_configuration_valid real_react_login_shared_vue_session http_only_platform_cookie explicit_asset_registration_capability explicit_initial_grant_plan pending_rights_metadata_only_preview duplicate_click_single_create asset_survives_reload metadata_not_ready_without_entity unknown_source_rights_pending version_two_committed old_grant_denies_current_and_history explicit_regrant_restores_version_two historical_version_one_readonly history_uses_current_authority not_ready_download_denied foreign_workspace_denied same_tenant_other_workspace_denied same_cookie_after_revocation revoked_old_page_clears_content revoked_history_navigation_no_content all_database_checkpoints_acknowledged no_internal_credentials_identity_spoofing_or_external_requests"
const assetBrowserStages = "preflight login_navigation login_account login_password login_submit workspace create_form initial_grants create_preview create_confirm create_reload version_form version_confirm old_grant regrant history download scope_negatives revocation complete"

func assetBrowserSnapshot(t *testing.T, f *farmChainFixture) map[string]string {
	t.Helper()
	out := f.snapshot(t)
	for _, scope := range []struct{ tenant, workspace string }{{f.org.Tenant.ID, f.org.Workspace.ID}, {f.org.Tenant.ID, f.second}, {f.foreign.Tenant.ID, f.foreign.Workspace.ID}} {
		for _, database := range []struct {
			db     *gorm.DB
			tables []string
		}{{f.seed, []string{"flow_agriculture_asset_operations", "flow_agriculture_asset_events"}}, {f.geo, []string{"agriculture_asset_catalog", "agriculture_asset_catalog_versions", "agriculture_asset_catalog_commands", "agriculture_asset_catalog_outbox", "agriculture_private_objects"}}} {
			e := database.db.Transaction(func(tx *gorm.DB) error {
				if e := tx.Exec("SELECT set_config('app.tenant_ids',?,true),set_config('app.workspace_id',?,true)", scope.tenant, scope.workspace).Error; e != nil {
					return e
				}
				for _, table := range database.tables {
					var raw string
					if e := tx.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(v) ORDER BY to_jsonb(v)::text),'[]')::text FROM "+table+" v WHERE tenant_id=? AND workspace_id=?", scope.tenant, scope.workspace).Scan(&raw).Error; e != nil {
						return e
					}
					out[scope.workspace+":"+table] = raw
				}
				return nil
			})
			if e != nil {
				t.Fatal("scoped asset snapshot failed")
			}
		}
	}
	if out[f.org.Workspace.ID+":agriculture_asset_catalog"] == "[]" || out[f.org.Workspace.ID+":flow_agriculture_asset_operations"] == "[]" {
		t.Fatal("asset snapshot missing real business canary")
	}
	return out
}

func assetBrowserRegrant(t *testing.T, f *farmChainFixture, id string) {
	t.Helper()
	rows, e := f.platform.Service.ListBindings(context.Background(), f.owner, f.org.Workspace.ID)
	if e != nil {
		t.Fatal("asset grant inventory failed")
	}
	var revision int64
	for _, row := range rows {
		if row.SubjectID == f.actor && row.ResourceKind == "asset" && row.ResourceID == id {
			revision = row.AuthVersion
		}
	}
	if revision < 1 {
		t.Fatal("explicit initial asset grant missing")
	}
	_, e = f.platform.Service.GrantResourceBinding(context.Background(), f.owner, f.org.Workspace.ID, f.actor, ResourceRef{"asset", id}, []string{"agri.asset.read", "agri.asset.create", "agri.asset_download.read"}, nil, &revision)
	if e != nil {
		t.Fatal("explicit production asset regrant failed")
	}
}

func TestAssetBrowserPostgresChain(t *testing.T) {
	for _, key := range []string{"CHROME_BIN", "NGINX_BIN", "NGINX_MIME_TYPES", "REDIS_URL", "AGRI_IDENTITY_PYTHON", "AGRI_TEST_PG_RUNTIME_DSN", "AGRI_TEST_PG_MIGRATION_DSN", "AGRI_TEST_GEO_RUNTIME_DSN", "AGRI_TEST_GEO_MIGRATION_DSN"} {
		if os.Getenv(key) == "" {
			if os.Getenv("AGRI_ASSET_CHAIN_REQUIRED") == "1" {
				t.Fatal("required asset browser dependency absent")
			}
			t.Skip("actual asset browser dependencies not configured")
		}
	}
	t.Setenv("AGRI_FARM_AUTH_COMPONENT_ONLY", "0")
	t.Setenv("AGRI_FARM_CHAIN_REQUIRED", "1")
	stage := "fixture"
	t.Cleanup(func() {
		if t.Failed() {
			t.Log("ASSET_BROWSER_DIAGNOSTIC stage=" + stage)
		}
	})
	f := newFarmChainFixture(t, true)
	stage = "asset_preflight"
	status, _ := f.request(t, f.actorCookie, "GET", "/api/agri/v1/asset-context?workspace_id="+f.org.Workspace.ID, nil, "")
	if status != 200 {
		t.Fatalf("real asset context preflight status=%d", status)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required")
	}
	reportPath := filepath.Join(f.private, "asset-browser-report.json")
	allowed := map[string]bool{}
	for _, name := range strings.Fields(assetBrowserChecks) {
		allowed[name] = true
	}
	stages := map[string]bool{}
	for _, name := range strings.Fields(assetBrowserStages) {
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
			_ = os.WriteFile(filepath.Join(dir, "asset-chain-browser.json"), clean, 0600)
		}
	})
	config := filepath.Join(f.private, "asset-browser.json")
	chainPrivate(t, config, map[string]any{"platform_url": f.server.URL, "gateway_port": f.gatewayPort, "username": f.actor, "password": f.password, "workspace_id": f.org.Workspace.ID, "foreign_workspace_id": f.foreign.Workspace.ID, "second_workspace_id": f.second, "report_path": reportPath})
	stage = "child_start"
	child := chainStart(t, node, f.root, filepath.Join(f.root, "scripts/agriculture/asset-browser-e2e.mjs"), config)
	var stableID string
	var immutable []string
	var frozen map[string]string
	var staleFrozen map[string]string
	for _, checkpoint := range []string{"created", "versioned", "regranted", "revoke-request", "revoked-verified"} {
		stage = "wait_" + checkpoint
		var event struct {
			Event      string `json:"event"`
			Checkpoint string `json:"checkpoint"`
		}
		chainLine(t, child, 90*time.Second, &event)
		if event.Event != "checkpoint" || event.Checkpoint != checkpoint {
			t.Fatal("unexpected asset browser checkpoint")
		}
		stage = "verify_" + checkpoint
		if checkpoint == "created" || checkpoint == "versioned" {
			want := int64(1)
			if checkpoint == "versioned" {
				want = 2
			}
			if e := f.geo.Transaction(func(tx *gorm.DB) error {
				if e := tx.Exec("SELECT set_config('app.tenant_ids',?,true),set_config('app.workspace_id',?,true)", f.org.Tenant.ID, f.org.Workspace.ID).Error; e != nil {
					return e
				}
				var rows []struct {
					AssetID         string
					FarmID          *string
					CurrentRevision int64
					DataClass       string
					Status          string
				}
				if e := tx.Raw("SELECT asset_id,farm_id,current_revision,data_class,status FROM agriculture_asset_catalog WHERE tenant_id=? AND workspace_id=?", f.org.Tenant.ID, f.org.Workspace.ID).Scan(&rows).Error; e != nil {
					return e
				}
				if len(rows) != 1 || rows[0].FarmID != nil || rows[0].CurrentRevision != want || rows[0].DataClass != "demo" || rows[0].Status != "validating" {
					t.Fatal("actual asset identity or nonready state differs")
				}
				if stableID == "" {
					stableID = rows[0].AssetID
				}
				if rows[0].AssetID != stableID {
					t.Fatal("stable asset replaced")
				}
				var history []struct {
					Version   int64
					CreatedBy string
					Raw       string
					ObjectID  *string
					Rights    string
					Name      string
				}
				if e := tx.Raw("SELECT version,created_by,to_jsonb(v)::text AS raw,object_id,rights_snapshot->>'status' AS rights,metadata_json->>'name' AS name FROM agriculture_asset_catalog_versions v WHERE tenant_id=? AND workspace_id=? AND asset_id=? ORDER BY version", f.org.Tenant.ID, f.org.Workspace.ID, stableID).Scan(&history).Error; e != nil {
					return e
				}
				if len(history) != int(want) {
					t.Fatal("asset version count differs")
				}
				for i, row := range history {
					name := "Synthetic browser asset one"
					if i == 1 {
						name = "Synthetic browser asset two"
					}
					if row.Version != int64(i+1) || row.CreatedBy != f.actor || row.ObjectID != nil || row.Rights != "pending" || row.Name != name {
						t.Fatal("asset immutable metadata or trusted actor differs")
					}
					if i < len(immutable) && immutable[i] != row.Raw {
						t.Fatal("historical asset overwritten")
					}
					if i == len(immutable) {
						immutable = append(immutable, row.Raw)
					}
				}
				for _, table := range []string{"agriculture_asset_catalog_commands", "agriculture_asset_catalog_outbox"} {
					var count int64
					if e := tx.Raw("SELECT count(*) FROM "+table+" WHERE tenant_id=? AND workspace_id=?", f.org.Tenant.ID, f.org.Workspace.ID).Scan(&count).Error; e != nil {
						return e
					}
					if count != want {
						t.Fatal("asset receipt/outbox count differs")
					}
				}
				return nil
			}); e != nil {
				t.Fatalf("asset database checkpoint failed: %T", e)
			}
			for _, table := range []string{"flow_agriculture_asset_operations", "flow_agriculture_asset_events"} {
				var count int64
				if e := f.seed.Raw("SELECT count(*) FROM "+table+" WHERE tenant_id=? AND workspace_id=?", f.org.Tenant.ID, f.org.Workspace.ID).Scan(&count).Error; e != nil || count != want {
					t.Fatal("asset platform receipt checkpoint differs")
				}
			}
		}
		if checkpoint == "regranted" {
			before := assetBrowserSnapshot(t, f)
			if !reflect.DeepEqual(staleFrozen, before) {
				t.Fatal("stale browser read or receipt query changed business data")
			}
			for _, version := range []string{"1", "2"} {
				status, body := f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets/"+stableID+"/versions/"+version+"?workspace_id="+f.org.Workspace.ID, nil, "")
				if (status != 403 && status != 404) || body["data"] != nil {
					t.Fatal("old asset grant still authorizes history")
				}
			}
			if !reflect.DeepEqual(before, assetBrowserSnapshot(t, f)) {
				t.Fatal("old asset grant mutated business data")
			}
			assetBrowserRegrant(t, f, stableID)
			before = assetBrowserSnapshot(t, f)
			_, e := f.assets.AuthorizeAssetDownload(context.Background(), f.actor, f.org.Workspace.ID, stableID)
			if !errors.Is(e, ErrNotFound) {
				t.Fatal("actual typed nonready asset download authority not denied")
			}
			if !reflect.DeepEqual(before, assetBrowserSnapshot(t, f)) {
				t.Fatal("nonready asset download changed business data")
			}
		}
		if checkpoint == "versioned" {
			staleFrozen = assetBrowserSnapshot(t, f)
		}
		if checkpoint == "revoke-request" {
			suspended := "suspended"
			if e := f.platform.Service.ChangeMember(context.Background(), f.owner, f.org.Workspace.ID, f.actor, nil, &suspended); e != nil {
				t.Fatal("real asset browser revocation failed")
			}
			frozen = assetBrowserSnapshot(t, f)
		}
		if checkpoint == "revoked-verified" && !reflect.DeepEqual(frozen, assetBrowserSnapshot(t, f)) {
			t.Fatal("revoked browser changed asset business data")
		}
		ack, _ := json.Marshal(map[string]string{"event": "ack", "checkpoint": checkpoint})
		if _, e := child.input.Write(append(ack, '\n')); e != nil {
			t.Fatal("asset checkpoint acknowledgement failed")
		}
	}
	stage = "child_finish"
	select {
	case <-child.done:
		if child.err != nil {
			t.Fatal("asset browser child failed")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("asset browser did not finish")
	}
	stage = "report_validate"
	raw, err := os.ReadFile(reportPath)
	if err != nil || len(raw) > 1<<20 {
		t.Fatal("asset browser report unavailable")
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
		t.Fatal("asset browser required report not passed")
	}
	seen := map[string]bool{}
	for _, r := range report.Results {
		if !allowed[r.Name] || seen[r.Name] || r.Result != "pass" {
			t.Fatal("asset browser required assertions differ")
		}
		seen[r.Name] = true
	}
	for _, secret := range []string{f.password, f.actorCookie.Value, f.ownerCookie.Value, os.Getenv("AGRI_TEST_PG_RUNTIME_DSN"), os.Getenv("AGRI_TEST_GEO_RUNTIME_DSN")} {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("private material in asset report")
		}
	}
	dir := filepath.Join(f.root, ".local/evidence")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal("asset evidence directory unavailable")
	}
	if err = os.WriteFile(filepath.Join(dir, "asset-chain-browser.json"), raw, 0600); err != nil {
		t.Fatal("asset evidence write failed")
	}
}
