package agriculture

import (
	"context"
	"encoding/json"
	"gorm.io/gorm"
	"net/http"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"yingce/backend/internal/model"
)

func parcelChainInput(code string) map[string]any {
	return map[string]any{"external_code": code, "name": "Parcel fixture " + code, "source_crs": "EPSG:4326", "coordinate_order": "longitude_latitude", "geometry": map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{110, 20}, {110.001, 20}, {110.001, 20.001}, {110, 20.001}, {110, 20}}}}}
}
func parcelChainGrant(t *testing.T, f *farmChainFixture, farm string) {
	t.Helper()
	rows, e := f.platform.Service.ListBindings(context.Background(), f.owner, f.org.Workspace.ID)
	if e != nil {
		t.Fatal("list grant fixture failed")
	}
	var revision int64
	for _, row := range rows {
		if row.SubjectID == f.actor && row.ResourceKind == "farm" && row.ResourceID == farm {
			revision = row.AuthVersion
		}
	}
	_, e = f.platform.Service.GrantResourceBinding(context.Background(), f.owner, f.org.Workspace.ID, f.actor, ResourceRef{"farm", farm}, []string{"agri.farm.read", "agri.farm.update", "agri.farm.archive", "agri.parcel.create", "agri.parcel.read", "agri.parcel.update"}, nil, &revision)
	if e != nil {
		t.Fatal("explicit parcel parent grant failed")
	}
}
func parcelChainSnapshot(t *testing.T, f *farmChainFixture) map[string]string {
	t.Helper()
	out, e := chainPlatformSnapshot(f.seed)
	if e != nil {
		t.Fatal("platform snapshot failed")
	}
	for _, scope := range []struct{ tenant, workspace string }{{f.org.Tenant.ID, f.org.Workspace.ID}, {f.org.Tenant.ID, f.second}, {f.foreign.Tenant.ID, f.foreign.Workspace.ID}} {
		for _, database := range []struct {
			db     *gorm.DB
			tables []string
		}{{f.seed, []string{"flow_agriculture_parcel_operations", "flow_agriculture_parcel_events", "flow_agriculture_parcel_references"}}, {f.geo, []string{"agriculture_parcels", "agriculture_parcel_revisions", "agriculture_parcel_commands", "agriculture_parcel_outbox"}}} {
			e = database.db.Transaction(func(tx *gorm.DB) error {
				if e := tx.Exec("SELECT set_config('app.tenant_ids',?,true),set_config('app.workspace_id',?,true)", scope.tenant, scope.workspace).Error; e != nil {
					return e
				}
				for _, table := range database.tables {
					var raw string
					if e := tx.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(v) ORDER BY to_jsonb(v)::text),'[]')::text FROM " + table + " v").Scan(&raw).Error; e != nil {
						return e
					}
					out[scope.workspace+":"+table] = raw
				}
				return nil
			})
			if e != nil {
				t.Fatal("scoped parcel snapshot failed")
			}
		}
	}
	return out
}

func TestParcelHTTPPostgresChain(t *testing.T) {
	f := newFarmChainFixture(t, false)
	farm, _ := f.create(t, "parcel-parent", "fixture-north")
	parcelChainGrant(t, f, farm)
	ws := f.org.Workspace.ID
	var id string
	var preview, mapOperation, mapContext, mapList, mapDetail, mapHistory, mapParents map[string]any
	input := parcelChainInput("first")
	previewInput := map[string]any{"workspace_id": ws, "action": "create", "farm_id": farm, "expected_revision": 0, "expected_farm_revision": 1, "parcel": input, "reason": "Initial fixture boundary"}
	var confirm map[string]any
	t.Run("preview_create_receipt_and_replay", func(t *testing.T) {
		status, body := f.request(t, f.actorCookie, "POST", "/api/agri/v1/parcel-previews", previewInput, "")
		if status != 200 {
			t.Fatalf("parcel preview status=%d", status)
		}
		preview = body
		confirm = map[string]any{"workspace_id": ws, "preview_id": farmData(t, body)["preview_id"], "expected_revision": 0, "expected_farm_revision": 1, "parcel": input, "reason": "Initial fixture boundary"}
		status, body = f.request(t, f.actorCookie, "POST", "/api/agri/v1/farms/"+farm+"/parcels", confirm, "parcel-fixture-create")
		if status != 201 {
			t.Fatalf("parcel create status=%d", status)
		}
		mapOperation = body
		data := farmData(t, body)
		id, _ = data["parcel_id"].(string)
		if !parcelIDPattern.MatchString(id) || data["state"] != "completed" || data["geometry_revision"] != float64(1) {
			t.Fatal("parcel typed completion invalid")
		}
		before := parcelChainSnapshot(t, f)
		status, again := f.request(t, f.actorCookie, "POST", "/api/agri/v1/farms/"+farm+"/parcels", confirm, "parcel-fixture-create")
		if status != 201 || !reflect.DeepEqual(data, farmData(t, again)) || !reflect.DeepEqual(before, parcelChainSnapshot(t, f)) {
			t.Fatal("parcel idempotent replay mutated domain")
		}
	})
	if id == "" {
		t.Fatal("parcel chain prerequisite create failed")
	}
	t.Run("context_list_detail_history_current", func(t *testing.T) {
		for _, item := range []struct {
			path   string
			target *map[string]any
		}{{"/parcel-farms", &mapParents}, {"/farms/" + farm + "/parcel-context", &mapContext}, {"/farms/" + farm + "/parcels", &mapList}, {"/parcels/" + id, &mapDetail}, {"/parcels/" + id + "/history", &mapHistory}} {
			status, body := f.request(t, f.actorCookie, "GET", "/api/agri/v1"+item.path+"?workspace_id="+ws, nil, "")
			if status != 200 {
				t.Fatalf("parcel read contract status=%d", status)
			}
			*item.target = body
		}
		detail := farmData(t, mapDetail)
		if detail["geometry_revision"] != float64(1) || detail["current_geometry_revision"] != float64(1) || detail["valid_to"] != nil {
			t.Fatal("parcel initial version missing")
		}
		if len(mapList["data"].([]any)) != 1 || len(mapHistory["data"].([]any)) != 1 {
			t.Fatal("parcel list/history incomplete")
		}
	})
	t.Run("real_response_frontend_contract", func(t *testing.T) {
		bun, lookupErr := exec.LookPath("bun")
		if lookupErr != nil {
			t.Fatal("locked Bun required for real parcel response parser")
		}
		path := filepath.Join(f.private, "parcel-http-contract.json")
		chainPrivate(t, path, map[string]any{"parents": mapParents, "context": mapContext, "list": mapList, "detail": mapDetail, "history": mapHistory, "preview": preview, "operation": mapOperation, "preview_input": previewInput})
		cmd := exec.Command(bun, filepath.Join(f.root, "services/agri-gis/frontend/agriculture/parcels/http-contract-check.mjs"), path)
		cmd.Dir = f.root
		out, e := cmd.Output()
		if e != nil {
			t.Fatal("real parcel frontend parser rejected response")
		}
		var result struct {
			Passed bool `json:"passed"`
			Checks int  `json:"checks"`
		}
		if json.Unmarshal(out, &result) != nil || !result.Passed || result.Checks != 8 {
			t.Fatal("parcel frontend contract did not execute all checks")
		}
	})
	t.Run("reject_foreign_workspace_and_invalid_geometry_without_writes", func(t *testing.T) {
		before := parcelChainSnapshot(t, f)
		for _, workspace := range []string{f.second, f.foreign.Workspace.ID} {
			status, _ := f.request(t, f.actorCookie, "GET", "/api/agri/v1/parcels/"+id+"?workspace_id="+workspace, nil, "")
			if status != http.StatusNotFound {
				t.Fatalf("foreign parcel scope status=%d", status)
			}
		}
		invalid := parcelChainInput("bad")
		invalid["geometry"] = map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{110, 20}, {110.001, 20.001}, {110, 20.001}, {110.001, 20}, {110, 20}}}}
		status, _ := f.request(t, f.actorCookie, "POST", "/api/agri/v1/parcel-previews", map[string]any{"workspace_id": ws, "action": "create", "farm_id": farm, "expected_revision": 0, "expected_farm_revision": 1, "parcel": invalid, "reason": "Invalid fixture geometry"}, "")
		if status != 422 {
			t.Fatalf("invalid parcel geometry status=%d", status)
		}
		if !reflect.DeepEqual(before, parcelChainSnapshot(t, f)) {
			t.Fatal("rejected parcel request wrote domain")
		}
	})
	t.Run("revision_history_and_stale_preview_conflict", func(t *testing.T) {
		geometry := parcelChainInput("larger")["geometry"].(map[string]any)
		geometry["coordinates"] = [][][]float64{{{110, 20}, {110.002, 20}, {110.002, 20.001}, {110, 20.001}, {110, 20}}}
		payload := map[string]any{"workspace_id": ws, "action": "revise", "parcel_id": id, "expected_revision": 1, "geometry": geometry, "source_crs": "EPSG:4326", "coordinate_order": "longitude_latitude", "reason": "Boundary correction"}
		status, body := f.request(t, f.actorCookie, "POST", "/api/agri/v1/parcel-previews", payload, "")
		if status != 200 {
			t.Fatalf("revise preview status=%d", status)
		}
		payload["preview_id"] = farmData(t, body)["preview_id"]
		delete(payload, "action")
		delete(payload, "parcel_id")
		status, body = f.request(t, f.actorCookie, "POST", "/api/agri/v1/parcels/"+id+"/revisions", payload, "parcel-revise-one")
		if status != 200 || farmData(t, body)["geometry_revision"] != float64(2) {
			t.Fatalf("revise status=%d", status)
		}
		for _, selector := range []string{"revision=1", "at=" + farmData(t, mapDetail)["valid_from"].(string)} {
			status, body = f.request(t, f.actorCookie, "GET", "/api/agri/v1/parcels/"+id+"?workspace_id="+ws+"&"+selector, nil, "")
			if status != 200 {
				t.Fatalf("history read status=%d", status)
			}
			data := farmData(t, body)
			if data["geometry_revision"] != float64(1) || data["current_geometry_revision"] != float64(2) || data["geometry_hash"] != farmData(t, mapDetail)["geometry_hash"] || data["valid_to"] == nil {
				t.Fatal("immutable historical geometry lost")
			}
			caps := data["allowed_actions"].([]any)
			if len(caps) != 1 || caps[0] != "agri.parcel.read" {
				t.Fatal("historical write action exposed")
			}
		}
		stale := map[string]any{"workspace_id": ws, "action": "revise", "parcel_id": id, "expected_revision": 1, "geometry": geometry, "source_crs": "EPSG:4326", "coordinate_order": "longitude_latitude", "reason": "Stale fixture"}
		before := parcelChainSnapshot(t, f)
		status, _ = f.request(t, f.actorCookie, "POST", "/api/agri/v1/parcel-previews", stale, "")
		if status != 409 || !reflect.DeepEqual(before, parcelChainSnapshot(t, f)) {
			t.Fatal("stale preview not rejected without mutation")
		}
	})
	t.Run("direct_grants_mixed_context_cursor_and_version_staleness", func(t *testing.T) {
		status, page := f.request(t, f.actorCookie, "GET", "/api/agri/v1/parcels/"+id+"/history?workspace_id="+ws+"&limit=1", nil, "")
		cursor, ok := page["next_cursor"].(string)
		if status != 200 || !ok || cursor == "" || page["has_more"] != true {
			t.Fatal("real bounded history cursor absent")
		}
		before := parcelChainSnapshot(t, f)
		for _, other := range []string{f.second, f.foreign.Workspace.ID} {
			status, _ = f.request(t, f.actorCookie, "GET", "/api/agri/v1/parcels/"+id+"/history?workspace_id="+other+"&limit=1&cursor="+cursor, nil, "")
			if status != 404 {
				t.Fatalf("cross workspace cursor status=%d", status)
			}
		}
		if !reflect.DeepEqual(before, parcelChainSnapshot(t, f)) {
			t.Fatal("cross scope cursor wrote domain")
		}
		rows, e := f.platform.Service.ListBindings(context.Background(), f.owner, ws)
		if e != nil {
			t.Fatal("parent grants unavailable")
		}
		var parentRevision int64
		for _, b := range rows {
			if b.SubjectID == f.actor && b.ResourceKind == "farm" && b.ResourceID == farm {
				parentRevision = b.AuthVersion
			}
		}
		_, e = f.platform.Service.GrantResourceBinding(context.Background(), f.owner, ws, f.actor, ResourceRef{"farm", farm}, []string{"agri.parcel.create"}, nil, &parentRevision)
		if e != nil {
			t.Fatal("create-only parent grant failed")
		}
		zero := int64(0)
		direct, e := f.platform.Service.GrantResourceBinding(context.Background(), f.owner, ws, f.actor, ResourceRef{"parcel", id}, []string{"agri.parcel.read", "agri.parcel.update"}, nil, &zero)
		if e != nil || direct.ReceiptRevision != 2 {
			t.Fatal("explicit parcel grant did not bind current revision")
		}
		status, ctxBody := f.request(t, f.actorCookie, "GET", "/api/agri/v1/farms/"+farm+"/parcel-context?workspace_id="+ws, nil, "")
		if status != 200 {
			t.Fatalf("mixed direct/parent context status=%d", status)
		}
		caps := farmData(t, ctxBody)["allowed_actions"].([]any)
		if !reflect.DeepEqual(caps, []any{"agri.parcel.create", "agri.parcel.read"}) {
			t.Fatal("mixed create/read capability projection incomplete")
		}
		status, parentPage := f.request(t, f.actorCookie, "GET", "/api/agri/v1/parcel-farms?workspace_id="+ws, nil, "")
		if status != 200 {
			t.Fatal("direct grant parent discovery failed")
		}
		parents := parentPage["data"].([]any)
		if len(parents) != 1 {
			t.Fatal("direct grant parent directory missing")
		}
		parent := parents[0].(map[string]any)
		if parent["farm_id"] != farm || len(parent) != 4 || parent["name"] != nil || parent["geometry"] != nil {
			t.Fatal("parent discovery disclosed ungranted farm content")
		}
		status, directPage := f.request(t, f.actorCookie, "GET", "/api/agri/v1/farms/"+farm+"/parcels?workspace_id="+ws, nil, "")
		if status != 200 || len(directPage["data"].([]any)) != 1 {
			t.Fatal("direct-grant-only list missing")
		}
		before = parcelChainSnapshot(t, f)
		status, _ = f.request(t, f.actorCookie, "GET", "/api/agri/v1/parcels/"+id+"/history?workspace_id="+ws+"&limit=1&cursor="+cursor, nil, "")
		if status != 409 || !reflect.DeepEqual(before, parcelChainSnapshot(t, f)) {
			t.Fatal("old epoch history cursor not rejected")
		}
		// A cursor is also bound to its actor, even where both actors have valid
		// explicit read grants. This is not merely a missing-permission denial.
		roles := []string{"farm_manager"}
		if e = f.platform.Service.ChangeMember(context.Background(), f.owner, ws, f.owner, &roles, nil); e != nil {
			t.Fatal("owner fixture explicit business role failed")
		}
		_, e = f.platform.Service.GrantResourceBinding(context.Background(), f.owner, ws, f.owner, ResourceRef{"parcel", id}, []string{"agri.parcel.read"}, nil, &zero)
		if e != nil {
			t.Fatal("second actor explicit read grant failed")
		}
		status, page = f.request(t, f.actorCookie, "GET", "/api/agri/v1/parcels/"+id+"/history?workspace_id="+ws+"&limit=1", nil, "")
		if status != 200 {
			t.Fatal("fresh actor-bound cursor unavailable")
		}
		cursor = page["next_cursor"].(string)
		before = parcelChainSnapshot(t, f)
		status, _ = f.request(t, f.ownerCookie, "GET", "/api/agri/v1/parcels/"+id+"/history?workspace_id="+ws+"&limit=1&cursor="+cursor, nil, "")
		if status != 400 || !reflect.DeepEqual(before, parcelChainSnapshot(t, f)) {
			t.Fatal("cross actor cursor replay accepted")
		}
		payload := map[string]any{"workspace_id": ws, "action": "revise", "parcel_id": id, "expected_revision": 2, "geometry": parcelChainInput("direct")["geometry"], "source_crs": "EPSG:4326", "coordinate_order": "longitude_latitude", "reason": "Explicit direct grant revision"}
		status, body := f.request(t, f.actorCookie, "POST", "/api/agri/v1/parcel-previews", payload, "")
		if status != 200 {
			t.Fatalf("direct revise preview status=%d", status)
		}
		payload["preview_id"] = farmData(t, body)["preview_id"]
		delete(payload, "action")
		delete(payload, "parcel_id")
		status, body = f.request(t, f.actorCookie, "POST", "/api/agri/v1/parcels/"+id+"/revisions", payload, "parcel-direct-revise")
		if status != 200 || farmData(t, body)["geometry_revision"] != float64(3) {
			t.Fatalf("direct grant receipt completion status=%d", status)
		}
		before = parcelChainSnapshot(t, f)
		status, _ = f.request(t, f.actorCookie, "GET", "/api/agri/v1/parcels/"+id+"?workspace_id="+ws, nil, "")
		if status != 403 && status != 404 {
			t.Fatalf("stale direct grant read status=%d", status)
		}
		if !reflect.DeepEqual(before, parcelChainSnapshot(t, f)) {
			t.Fatal("stale direct grant changed state")
		}
	})
	t.Run("revoke_current_grant_blocks_content_and_preview", func(t *testing.T) {
		rows, e := f.platform.Service.ListBindings(context.Background(), f.owner, ws)
		if e != nil {
			t.Fatal(e)
		}
		var binding model.AgricultureResourceBinding
		for _, b := range rows {
			if b.SubjectID == f.actor && b.ResourceKind == "farm" && b.ResourceID == farm {
				binding = b
			}
		}
		if binding.ID == "" {
			t.Fatal("explicit parcel parent grant absent")
		}
		if e = f.platform.Service.RevokeBinding(context.Background(), f.owner, ws, binding.ID, &binding.AuthVersion); e != nil {
			t.Fatal("fixture revoke failed")
		}
		before := parcelChainSnapshot(t, f)
		status, _ := f.request(t, f.actorCookie, "GET", "/api/agri/v1/parcels/"+id+"?workspace_id="+ws, nil, "")
		if status != 403 && status != 404 {
			t.Fatalf("revoked parcel read status=%d", status)
		}
		status, _ = f.request(t, f.actorCookie, "POST", "/api/agri/v1/parcel-previews", previewInput, "")
		if status != 403 && status != 404 {
			t.Fatalf("revoked parcel preview status=%d", status)
		}
		if !reflect.DeepEqual(before, parcelChainSnapshot(t, f)) {
			t.Fatal("revoked request mutated domain")
		}
	})
}
