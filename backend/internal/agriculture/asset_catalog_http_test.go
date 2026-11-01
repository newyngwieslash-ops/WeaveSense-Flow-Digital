package agriculture

import (
	"context"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"yingce/backend/internal/model"
)

func assetCatalogFixtureMetadata(name string) map[string]any {
	return map[string]any{"name": name, "kind": "photo", "data_class": "demo", "farm_id": nil, "source": map[string]any{"kind": "customer_data", "source_id": nil, "reference": "customer_fixture"}, "rights_declaration": map[string]any{"owner": nil, "license_ref": nil, "permitted_uses": []string{}, "attribution": map[string]any{"required": false, "text": nil}, "valid_from": nil, "expiry": nil}, "sensitivity": "internal", "retention": map[string]any{"retain_until": nil, "legal_hold": false}}
}
func assetCatalogFixtureGrants(f *farmChainFixture) []FarmGrant {
	return []FarmGrant{{SubjectUserID: f.actor, Capabilities: []string{"agri.asset.create", "agri.asset.read", "agri.asset_download.read"}}}
}
func assetCatalogSnapshot(t *testing.T, f *farmChainFixture) map[string]string {
	t.Helper()
	out := f.snapshot(t)
	platform, e := chainPlatformSnapshot(f.seed)
	if e != nil {
		t.Fatal("asset snapshot governance failed")
	}
	for k, v := range platform {
		out[k] = v
	}
	for _, scope := range []struct{ tenant, workspace string }{{f.org.Tenant.ID, f.org.Workspace.ID}, {f.org.Tenant.ID, f.second}, {f.foreign.Tenant.ID, f.foreign.Workspace.ID}} {
		for _, db := range []struct {
			db     *gorm.DB
			tables []string
		}{{f.seed, []string{"flow_agriculture_asset_operations", "flow_agriculture_asset_events"}}, {f.geo, []string{"agriculture_asset_catalog", "agriculture_asset_catalog_versions", "agriculture_asset_catalog_commands", "agriculture_asset_catalog_outbox"}}} {
			e = db.db.Transaction(func(tx *gorm.DB) error {
				if e := tx.Exec("SELECT set_config('app.tenant_ids',?,true),set_config('app.workspace_id',?,true)", scope.tenant, scope.workspace).Error; e != nil {
					return e
				}
				for _, table := range db.tables {
					var raw string
					if e := tx.Raw("SELECT COALESCE(jsonb_agg(to_jsonb(v) ORDER BY to_jsonb(v)::text),'[]')::text FROM " + table + " v").Scan(&raw).Error; e != nil {
						return e
					}
					out[scope.workspace+":"+table] = raw
				}
				return nil
			})
			if e != nil {
				t.Fatal("asset scoped snapshot failed")
			}
		}
	}
	return out
}
func TestAssetCatalogHTTPPostgresChain(t *testing.T) {
	f := newFarmChainFixture(t, false)
	ws := f.org.Workspace.ID
	metadata := assetCatalogFixtureMetadata("Same named fixture")
	grants := assetCatalogFixtureGrants(f)
	var id string
	var contextBody, preview, operation, createBody, detail, list map[string]any
	previewInput := map[string]any{"workspace_id": ws, "action": "create", "expected_revision": 0, "metadata": metadata, "initial_grants": grants}
	t.Run("context_and_pending_creation", func(t *testing.T) {
		status, body := f.request(t, f.actorCookie, "GET", "/api/agri/v1/asset-context?workspace_id="+ws, nil, "")
		if status != 200 {
			t.Fatalf("asset context status=%d", status)
		}
		contextBody = body
		if farmData(t, body)["can_register_without_farm"] != true {
			t.Fatal("governance bootstrap capability absent")
		}
		status, preview = f.request(t, f.actorCookie, "POST", "/api/agri/v1/asset-previews", previewInput, "")
		if status != 200 {
			t.Fatalf("asset preview status=%d", status)
		}
		createBody = map[string]any{"workspace_id": ws, "preview_id": farmData(t, preview)["preview_id"], "expected_revision": 0, "metadata": metadata, "initial_grants": grants}
		status, operation = f.request(t, f.actorCookie, "POST", "/api/agri/v1/assets", createBody, "asset-fixture-create")
		if status != 201 {
			t.Fatalf("asset create status=%d", status)
		}
		result := farmData(t, operation)
		id, _ = result["asset_id"].(string)
		if !assetCatalogID.MatchString(id) || result["state"] != "completed" || result["revision"] != float64(1) {
			t.Fatal("asset registration receipt invalid")
		}
		status, detail = f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets/"+id+"/versions/1?workspace_id="+ws, nil, "")
		if status != 200 {
			t.Fatalf("asset first read status=%d", status)
		}
		asset := farmData(t, detail)
		if asset["status"] != "validating" || asset["entity"] != nil || asset["farm_id"] != nil || asset["rights"].(map[string]any)["status"] != "pending" {
			t.Fatal("metadata declaration falsely became ready/approved")
		}
		for _, cap := range asset["allowed_actions"].([]any) {
			if cap == "agri.asset_download.read" {
				t.Fatal("metadata-only asset offered download")
			}
		}
	})
	if id == "" {
		t.Fatal("asset creation prerequisite failed")
	}
	t.Run("idempotent_receipt_and_real_frontend_contract", func(t *testing.T) {
		before := assetCatalogSnapshot(t, f)
		status, body := f.request(t, f.actorCookie, "POST", "/api/agri/v1/assets", createBody, "asset-fixture-create")
		if status != 201 || !reflect.DeepEqual(farmData(t, body), farmData(t, operation)) || !reflect.DeepEqual(before, assetCatalogSnapshot(t, f)) {
			t.Fatal("asset replay changed domain")
		}
		status, list = f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets?workspace_id="+ws, nil, "")
		if status != 200 || len(list["data"].([]any)) != 1 {
			t.Fatal("scoped asset list missing")
		}
		path := filepath.Join(f.private, "asset-http-contract.json")
		chainPrivate(t, path, map[string]any{"context": contextBody, "list": list, "detail": detail, "historical": detail, "preview": preview, "operation": operation, "preview_input": previewInput})
		bun, e := exec.LookPath("bun")
		if e != nil {
			t.Fatal("locked frontend parser runtime absent")
		}
		cmd := exec.Command(bun, filepath.Join(f.root, "services/agri-gis/frontend/agriculture/asset-catalog/http-contract-check.mjs"), path)
		cmd.Dir = f.root
		out, e := cmd.Output()
		if e != nil {
			t.Fatal("actual asset frontend contract rejected HTTP DTO")
		}
		var result struct {
			Passed bool `json:"passed"`
			Checks int  `json:"checks"`
		}
		if json.Unmarshal(out, &result) != nil || !result.Passed || result.Checks != 7 {
			t.Fatal("actual asset frontend checks incomplete")
		}
	})
	t.Run("cross_scope_and_invalid_source_no_writes", func(t *testing.T) {
		before := assetCatalogSnapshot(t, f)
		for _, other := range []string{f.second, f.foreign.Workspace.ID} {
			status, _ := f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets/"+id+"/versions/1?workspace_id="+other, nil, "")
			if status != 404 {
				t.Fatalf("foreign catalog status=%d", status)
			}
		}
		bad := assetCatalogFixtureMetadata("Unknown source")
		bad["source"].(map[string]any)["source_id"] = "foreign_fixture_source"
		status, _ := f.request(t, f.actorCookie, "POST", "/api/agri/v1/asset-previews", map[string]any{"workspace_id": ws, "action": "create", "expected_revision": 0, "metadata": bad, "initial_grants": grants}, "")
		if status != 404 {
			t.Fatalf("unknown scoped source status=%d", status)
		}
		if !reflect.DeepEqual(before, assetCatalogSnapshot(t, f)) {
			t.Fatal("rejected catalog request persisted business mutation")
		}
	})
	t.Run("version_conflict_and_explicit_reauthorization", func(t *testing.T) {
		revised := assetCatalogFixtureMetadata("Same named fixture")
		revised["sensitivity"] = "confidential"
		input := map[string]any{"workspace_id": ws, "action": "version", "asset_id": id, "expected_revision": 1, "metadata": revised}
		status, first := f.request(t, f.actorCookie, "POST", "/api/agri/v1/asset-previews", input, "")
		if status != 200 {
			t.Fatalf("version preview status=%d", status)
		}
		status, second := f.request(t, f.actorCookie, "POST", "/api/agri/v1/asset-previews", input, "")
		if status != 200 {
			t.Fatal("concurrent version preview absent")
		}
		write := map[string]any{"workspace_id": ws, "preview_id": farmData(t, first)["preview_id"], "expected_revision": 1, "metadata": revised}
		status, body := f.request(t, f.actorCookie, "POST", "/api/agri/v1/assets/"+id+"/versions", write, "asset-version-two")
		if status != 200 || farmData(t, body)["revision"] != float64(2) {
			t.Fatalf("version append status=%d", status)
		}
		status, _ = f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets/"+id+"/versions/2?workspace_id="+ws, nil, "")
		if status != 403 {
			t.Fatalf("old exact grant autoextended status=%d", status)
		}
		write["preview_id"] = farmData(t, second)["preview_id"]
		status, _ = f.request(t, f.actorCookie, "POST", "/api/agri/v1/assets/"+id+"/versions", write, "asset-concurrent-stale")
		if status != 409 {
			t.Fatalf("persisted expected-revision conflict status=%d", status)
		}
		rows, e := f.platform.Service.ListBindings(context.Background(), f.owner, ws)
		if e != nil {
			t.Fatal(e)
		}
		var binding model.AgricultureResourceBinding
		for _, b := range rows {
			if b.ResourceKind == "asset" && b.ResourceID == id && b.SubjectID == f.actor {
				binding = b
			}
		}
		if binding.ReceiptRevision != 1 {
			t.Fatal("original grant mutated")
		}
		_, e = f.platform.Service.GrantResourceBinding(context.Background(), f.owner, ws, f.actor, ResourceRef{"asset", id}, grants[0].Capabilities, nil, &binding.AuthVersion)
		if e != nil {
			t.Fatal("explicit typed asset regrant failed")
		}
		for version := int64(1); version <= 2; version++ {
			path := "/api/agri/v1/assets/" + id + "/versions/1?workspace_id=" + ws
			if version == 2 {
				path = "/api/agri/v1/assets/" + id + "/versions/2?workspace_id=" + ws
			}
			status, body = f.request(t, f.actorCookie, "GET", path, nil, "")
			if status != 200 {
				t.Fatalf("explicit regrant version read status=%d", status)
			}
			data := farmData(t, body)
			if data["version"] != float64(version) || data["revision"] != float64(2) || data["entity"] != nil {
				t.Fatal("asset history version context lost")
			}
			expected := "internal"
			if version == 2 {
				expected = "confidential"
			}
			if data["metadata"].(map[string]any)["sensitivity"] != expected {
				t.Fatal("immutable metadata overwritten")
			}
		}
	})
	t.Run("farm_scoped_creation_requires_grant_and_plan", func(t *testing.T) {
		farm, _ := f.create(t, "asset-parent", "fixture-north")
		data := assetCatalogFixtureMetadata("Farm fixture asset")
		data["farm_id"] = farm
		request := map[string]any{"workspace_id": ws, "action": "create", "expected_revision": 0, "metadata": data, "initial_grants": grants}
		before := assetCatalogSnapshot(t, f)
		status, _ := f.request(t, f.actorCookie, "POST", "/api/agri/v1/asset-previews", request, "")
		if status != 403 && status != 404 {
			t.Fatalf("farm read silently granted asset create status=%d", status)
		}
		if !reflect.DeepEqual(before, assetCatalogSnapshot(t, f)) {
			t.Fatal("ungranted farm asset preview wrote domain")
		}
		rows, e := f.platform.Service.ListBindings(context.Background(), f.owner, ws)
		if e != nil {
			t.Fatal(e)
		}
		var revision int64
		for _, b := range rows {
			if b.ResourceKind == "farm" && b.ResourceID == farm && b.SubjectID == f.actor {
				revision = b.AuthVersion
			}
		}
		_, e = f.platform.Service.GrantResourceBinding(context.Background(), f.owner, ws, f.actor, ResourceRef{"farm", farm}, []string{"agri.asset.create"}, nil, &revision)
		if e != nil {
			t.Fatal("explicit parent asset capability failed")
		}
		status, approved := f.request(t, f.actorCookie, "POST", "/api/agri/v1/asset-previews", request, "")
		if status != 200 {
			t.Fatalf("authorized governed farm asset preview status=%d", status)
		}
		write := map[string]any{"workspace_id": ws, "preview_id": farmData(t, approved)["preview_id"], "expected_revision": 0, "metadata": data, "initial_grants": grants}
		status, registered := f.request(t, f.actorCookie, "POST", "/api/agri/v1/assets", write, "asset-parent-register")
		if status != 201 {
			t.Fatalf("farm asset registration status=%d", status)
		}
		farmAsset := farmData(t, registered)["asset_id"].(string)
		binding := func(kind, resource string) model.AgricultureResourceBinding {
			rows, e := f.platform.Service.ListBindings(context.Background(), f.owner, ws)
			if e != nil {
				t.Fatal(e)
			}
			for _, b := range rows {
				if b.SubjectID == f.actor && b.ResourceKind == kind && b.ResourceID == resource {
					return b
				}
			}
			t.Fatal("binding absent")
			return model.AgricultureResourceBinding{}
		}
		grantParent := func(farmID string, caps []string, expiry *time.Time) {
			b := binding("farm", farmID)
			if _, e := f.platform.Service.GrantResourceBinding(context.Background(), f.owner, ws, f.actor, ResourceRef{"farm", farmID}, caps, expiry, &b.AuthVersion); e != nil {
				t.Fatal("parent explicit grant failed")
			}
		}
		denyRead := func(asset string) {
			before := assetCatalogSnapshot(t, f)
			status, _ := f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets/"+asset+"/versions/1?workspace_id="+ws, nil, "")
			if status != 403 && status != 404 {
				t.Fatalf("ungranted inherited read status=%d", status)
			}
			if !reflect.DeepEqual(before, assetCatalogSnapshot(t, f)) {
				t.Fatal("denied inherited read wrote domain")
			}
		}
		direct := binding("asset", farmAsset)
		if e := f.platform.Service.RevokeBinding(context.Background(), f.owner, ws, direct.ID, &direct.AuthVersion); e != nil {
			t.Fatal(e)
		}
		// Read is not implied by create, and unrelated farms never authorize.
		denyRead(farmAsset)
		other, _ := f.create(t, "asset-other-parent", "fixture-north")
		grantParent(other, grants[0].Capabilities, nil)
		denyRead(farmAsset)
		grantParent(farm, []string{"agri.farm.read"}, nil)
		denyRead(farmAsset)
		grantParent(farm, grants[0].Capabilities, nil)
		status, body := f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets/"+farmAsset+"/versions/1?workspace_id="+ws, nil, "")
		if status != 200 {
			t.Fatalf("explicit parent read status=%d", status)
		}
		for _, cap := range farmData(t, body)["allowed_actions"].([]any) {
			if cap == "agri.asset_download.read" {
				t.Fatal("parent download cap bypassed validating status")
			}
		}
		status, page := f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets?workspace_id="+ws, nil, "")
		found := false
		if status == 200 {
			for _, row := range page["data"].([]any) {
				if row.(map[string]any)["asset_id"] == farmAsset {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("parent-authorized exact asset absent from list")
		}
		data["sensitivity"] = "confidential"
		versionInput := map[string]any{"workspace_id": ws, "action": "version", "asset_id": farmAsset, "expected_revision": 1, "metadata": data}
		status, approved = f.request(t, f.actorCookie, "POST", "/api/agri/v1/asset-previews", versionInput, "")
		if status != 200 {
			t.Fatalf("parent version preview status=%d", status)
		}
		status, _ = f.request(t, f.actorCookie, "POST", "/api/agri/v1/assets/"+farmAsset+"/versions", map[string]any{"workspace_id": ws, "preview_id": farmData(t, approved)["preview_id"], "expected_revision": 1, "metadata": data}, "asset-parent-version")
		if status != 200 {
			t.Fatalf("parent version confirm status=%d", status)
		}
		status, _ = f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets/"+farmAsset+"/versions/2?workspace_id="+ws, nil, "")
		if status != 200 {
			t.Fatalf("explicit parent did not span version status=%d", status)
		}
		// Effective expiry is enforced by real wall-clock time, not edited rows.
		expiry := time.Now().UTC().Add(1200 * time.Millisecond)
		grantParent(farm, grants[0].Capabilities, &expiry)
		time.Sleep(time.Until(expiry) + 20*time.Millisecond)
		denyRead(farmAsset)
		grantParent(farm, grants[0].Capabilities, nil)
		parent := binding("farm", farm)
		if e := f.platform.Service.RevokeBinding(context.Background(), f.owner, ws, parent.ID, &parent.AuthVersion); e != nil {
			t.Fatal(e)
		}
		denyRead(farmAsset)
		// No-farm governance cannot be delegated via another farm's broad cap.
		roles := []string{"farm_manager"}
		if e := f.platform.Service.ChangeMember(context.Background(), f.owner, ws, f.actor, &roles, nil); e != nil {
			t.Fatal(e)
		}
		defer func() {
			roles := []string{"tenant_admin", "farm_manager"}
			if e := f.platform.Service.ChangeMember(context.Background(), f.owner, ws, f.actor, &roles, nil); e != nil {
				t.Fatal(e)
			}
		}()
		nofarm := binding("asset", id)
		before = assetCatalogSnapshot(t, f)
		_, e = f.platform.Service.GrantResourceBinding(context.Background(), f.actor, ws, f.actor, ResourceRef{"asset", id}, grants[0].Capabilities, nil, &nofarm.AuthVersion)
		if !errors.Is(e, ErrForbidden) {
			t.Fatal("no-farm manager issued unauthorized grant")
		}
		if !reflect.DeepEqual(before, assetCatalogSnapshot(t, f)) {
			t.Fatal("no-farm unauthorized governance wrote state")
		}

	})
	t.Run("revocation_no_content_or_mutation", func(t *testing.T) {
		rows, e := f.platform.Service.ListBindings(context.Background(), f.owner, ws)
		if e != nil {
			t.Fatal(e)
		}
		var binding model.AgricultureResourceBinding
		for _, b := range rows {
			if b.ResourceKind == "asset" && b.ResourceID == id && b.SubjectID == f.actor {
				binding = b
			}
		}
		if e = f.platform.Service.RevokeBinding(context.Background(), f.owner, ws, binding.ID, &binding.AuthVersion); e != nil {
			t.Fatal("asset revoke failed")
		}
		before := assetCatalogSnapshot(t, f)
		status, _ := f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets/"+id+"/versions/1?workspace_id="+ws, nil, "")
		if status != 404 {
			t.Fatalf("revoked asset content status=%d", status)
		}
		status, body := f.request(t, f.actorCookie, "GET", "/api/agri/v1/assets?workspace_id="+ws, nil, "")
		if status != 200 || len(body["data"].([]any)) != 0 {
			t.Fatal("revoked asset still listed")
		}
		if !reflect.DeepEqual(before, assetCatalogSnapshot(t, f)) {
			t.Fatal("revoked catalog request mutated domain")
		}
	})
}
