package agriculture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"gorm.io/gorm"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
	"yingce/backend/internal/flow"
)

// All content and approval records below are synthetic private fixtures, not
// licensed imagery, a production rights review or a pilot acceptance claim.
func assetDownloadRegistry(status string) map[string]any {
	return map[string]any{"schema_version": "1.0.0", "sources": []any{map[string]any{
		"id": "fixture-source", "kind": "fixture", "enabled": true, "data_class": "demo", "source_ref": "fixture:source", "parent_ids": []string{},
		"rights": map[string]any{"status": status, "owner": "Fixture author", "license_ref": "fixture-license", "commercial_use": "allowed", "redistribution": "allowed", "permitted_uses": []string{"ingest", "analysis", "export"}, "attribution": map[string]any{"required": true, "text": "Synthetic fixture"}, "valid_from": "2020-01-01T00:00:00Z", "expiry": "2099-01-01T00:00:00Z", "review_ref": "fixture-review", "evidence": []any{map[string]any{"ref": "fixture-proof", "sha256": "1111111111111111111111111111111111111111111111111111111111111111"}}},
	}}}
}
func assetDownloadWriteRegistry(t *testing.T, f *farmChainFixture, status string) {
	t.Helper()
	path := filepath.Join(f.private, "asset-rights-next.json")
	chainPrivate(t, path, assetDownloadRegistry(status))
	if e := os.Rename(path, filepath.Join(f.private, "asset-rights.json")); e != nil {
		t.Fatal("private fixture rights replacement failed")
	}
}
func assetDownloadSetup(f *farmChainFixture) {
	t := f.t
	chainPrivate(t, filepath.Join(f.private, "asset-rights.json"), assetDownloadRegistry("approved"))
	chainPrivate(t, filepath.Join(f.private, "asset-scopes.json"), map[string]any{"fixture-source": []any{map[string]string{"tenant_id": f.org.Tenant.ID, "workspace_id": f.org.Workspace.ID, "source_kind": "customer_data"}}})
	if e := os.Mkdir(filepath.Join(f.private, "asset-storage"), 0700); e != nil {
		t.Fatal("private fixture storage unavailable")
	}
}
func assetDownloadCreate(t *testing.T, f *farmChainFixture, farm string) string {
	t.Helper()
	value := assetCatalogFixtureMetadata("Synthetic private byte fixture")
	value["source"] = map[string]any{"kind": "customer_data", "source_id": "fixture-source", "reference": "fixture:source"}
	value["rights_declaration"] = map[string]any{"owner": "Fixture claimant", "license_ref": "fixture-license", "permitted_uses": []string{"ingest", "analysis", "export"}, "attribution": map[string]any{"required": true, "text": "Synthetic fixture"}, "valid_from": "2020-01-01T00:00:00Z", "expiry": "2099-01-01T00:00:00Z"}
	if farm != "" {
		value["farm_id"] = farm
	}
	grants := assetCatalogFixtureGrants(f)
	status, body := f.request(t, f.actorCookie, "POST", "/api/agri/v1/asset-previews", map[string]any{"workspace_id": f.org.Workspace.ID, "action": "create", "expected_revision": 0, "metadata": value, "initial_grants": grants}, "")
	if status != 200 {
		t.Fatalf("actual byte fixture preview status=%d", status)
	}
	status, body = f.request(t, f.actorCookie, "POST", "/api/agri/v1/assets", map[string]any{"workspace_id": f.org.Workspace.ID, "preview_id": farmData(t, body)["preview_id"], "expected_revision": 0, "metadata": value, "initial_grants": grants}, flow.NewID("fixture_"))
	if status != 201 {
		t.Fatalf("actual byte fixture creation status=%d", status)
	}
	id, _ := farmData(t, body)["asset_id"].(string)
	if !assetCatalogID.MatchString(id) {
		t.Fatal("byte fixture asset missing")
	}
	return id
}
func assetDownloadPublish(t *testing.T, f *farmChainFixture, id string, revision int64) (string, []byte) {
	t.Helper()
	object := flow.NewID("object_")
	version := flow.NewID("av_")
	// Flow IDs are not the catalog's 32-hex version format; hash fresh entropy.
	sum := sha256.Sum256([]byte(version))
	version = "av_" + hex.EncodeToString(sum[:16])
	content := []byte(fmt.Sprintf("Synthetic private catalog bytes, not production imagery; revision %d.", revision))
	digest := sha256.Sum256(content)
	key := fmt.Sprintf("tenants/%s/workspaces/%s/objects/%s/v1", f.org.Tenant.ID, f.org.Workspace.ID, object)
	path := filepath.Join(f.private, "asset-storage", filepath.FromSlash(key))
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal("fixture entity directory failed")
	}
	if e := os.WriteFile(path, content, 0600); e != nil {
		t.Fatal("fixture entity write failed")
	}
	e := f.geo.Transaction(func(tx *gorm.DB) error {
		if e := tx.Exec("INSERT INTO agriculture_private_objects (tenant_id,workspace_id,object_id,version,resource_kind,resource_id,resource_revision,category,object_key,content_sha256,size_bytes,media_type,data_class,distribution_allowed) VALUES(?,?,?,1,'asset',?,?,'original',?,?,?,'application/octet-stream','demo',true)", f.org.Tenant.ID, f.org.Workspace.ID, object, id, revision, key, hex.EncodeToString(digest[:]), len(content)).Error; e != nil {
			return e
		}
		result := tx.Exec("UPDATE agriculture_asset_catalog SET current_revision=?,current_version_id=?,status='ready',updated_at=clock_timestamp() WHERE tenant_id=? AND workspace_id=? AND asset_id=? AND current_revision=?", revision, version, f.org.Tenant.ID, f.org.Workspace.ID, id, revision-1)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("fixture head transition missing")
		}
		result = tx.Exec("INSERT INTO agriculture_asset_catalog_versions (tenant_id,workspace_id,asset_id,version,asset_version_id,metadata_json,metadata_hash,rights_snapshot,object_id,object_version,created_by) SELECT tenant_id,workspace_id,asset_id,?,?,metadata_json,metadata_hash,rights_snapshot,?,1,? FROM agriculture_asset_catalog_versions WHERE tenant_id=? AND workspace_id=? AND asset_id=? AND version=?", revision, version, object, f.owner, f.org.Tenant.ID, f.org.Workspace.ID, id, revision-1)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("fixture immutable entity version missing")
		}
		return nil
	})
	if e != nil {
		t.Fatalf("trusted fixture publication failed: %T", e)
	}
	return object, content
}

type assetDownloadResponse struct {
	status int
	header http.Header
	body   []byte
	err    error
}

func assetDownloadRequest(f *farmChainFixture, method, workspace, object, byteRange string) assetDownloadResponse {
	req, e := http.NewRequest(method, f.server.URL+"/api/agri/v1/workspaces/"+workspace+"/objects/"+object+"/versions/1", nil)
	if e != nil {
		return assetDownloadResponse{err: e}
	}
	req.AddCookie(f.actorCookie)
	req.Header.Set("Origin", f.origin)
	if byteRange != "" {
		req.Header.Set("Range", byteRange)
	}
	response, e := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if e != nil {
		return assetDownloadResponse{err: e}
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return assetDownloadResponse{response.StatusCode, response.Header, body, e}
}
func assetDownloadDenied(t *testing.T, response assetDownloadResponse, content []byte) {
	t.Helper()
	if response.err != nil || (response.status != 401 && response.status != 403 && response.status != 404) || bytes.Contains(response.body, content) {
		t.Fatalf("actual entity rejection invalid status=%d transport_error=%t", response.status, response.err != nil)
	}
}
func assetDownloadRevoke(t *testing.T, f *farmChainFixture, id string) {
	t.Helper()
	rows, e := f.platform.Service.ListBindings(context.Background(), f.owner, f.org.Workspace.ID)
	if e != nil {
		t.Fatal("binding inventory unavailable")
	}
	for _, row := range rows {
		if row.ResourceKind == "asset" && row.ResourceID == id && row.SubjectID == f.actor {
			if e := f.platform.Service.RevokeBinding(context.Background(), f.owner, f.org.Workspace.ID, row.ID, &row.AuthVersion); e != nil {
				t.Fatal("explicit binding revocation failed")
			}
			return
		}
	}
	t.Fatal("binding missing")
}
func assetDownloadBarrier(t *testing.T, f *farmChainFixture, object string, withdraw func(), content []byte) {
	t.Helper()
	for _, name := range []string{"asset-read-arm", "asset-read-started", "asset-read-release"} {
		if _, e := os.Lstat(filepath.Join(f.private, name)); !os.IsNotExist(e) {
			t.Fatal("read barrier already exists")
		}
	}
	if e := os.WriteFile(filepath.Join(f.private, "asset-read-arm"), []byte("armed"), 0600); e != nil {
		t.Fatal("read barrier arm failed")
	}
	done := make(chan assetDownloadResponse, 1)
	go func() { done <- assetDownloadRequest(f, "GET", f.org.Workspace.ID, object, "") }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if data, e := os.ReadFile(filepath.Join(f.private, "asset-read-started")); e == nil && string(data) == "read" {
			break
		}
		select {
		case <-done:
			t.Fatal("object read returned before real storage barrier")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("real storage read barrier not reached")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("entity bytes escaped before authority recheck")
	default:
	}
	withdraw()
	frozen := assetBrowserSnapshot(t, f)
	if e := os.WriteFile(filepath.Join(f.private, "asset-read-release"), []byte("release"), 0600); e != nil {
		t.Fatal("read barrier release failed")
	}
	select {
	case response := <-done:
		assetDownloadDenied(t, response, content)
	case <-time.After(10 * time.Second):
		t.Fatal("withdrawing object read did not finish")
	}
	if !reflect.DeepEqual(frozen, assetBrowserSnapshot(t, f)) {
		t.Fatal("denied inflight read mutated domain")
	}
	for _, name := range []string{"asset-read-arm", "asset-read-started", "asset-read-release"} {
		if e := os.Remove(filepath.Join(f.private, name)); e != nil {
			t.Fatal("owned read barrier cleanup failed")
		}
	}
}
func TestAssetCatalogDownloadPostgresChain(t *testing.T) {
	for _, key := range []string{"AGRI_IDENTITY_PYTHON", "AGRI_TEST_PG_MIGRATION_DSN", "AGRI_TEST_PG_RUNTIME_DSN", "AGRI_TEST_GEO_MIGRATION_DSN", "AGRI_TEST_GEO_RUNTIME_DSN", "REDIS_URL"} {
		if os.Getenv(key) == "" {
			if os.Getenv("AGRI_ASSET_CHAIN_REQUIRED") == "1" {
				t.Fatalf("required asset download dependency missing: %s", key)
			}
			t.Skip("actual asset download dual PostgreSQL and Redis dependencies not configured")
		}
	}
	t.Setenv("AGRI_FARM_CHAIN_REQUIRED", "1")
	t.Setenv("AGRI_FARM_AUTH_COMPONENT_ONLY", "0")
	f := newFarmChainFixtureWithAssetObjects(t, false, assetDownloadSetup)
	var id, object string
	var content []byte
	t.Run("real_unscoped_ready_entity_requires_fresh_grant", func(t *testing.T) {
		id = assetDownloadCreate(t, f, "")
		object, content = assetDownloadPublish(t, f, id, 2)
		before := assetBrowserSnapshot(t, f)
		assetDownloadDenied(t, assetDownloadRequest(f, "GET", f.org.Workspace.ID, object, ""), content)
		if !reflect.DeepEqual(before, assetBrowserSnapshot(t, f)) {
			t.Fatal("stale entity grant mutated business state")
		}
		assetBrowserRegrant(t, f, id)
		response := assetDownloadRequest(f, "GET", f.org.Workspace.ID, object, "")
		if response.err != nil || response.status != 200 || !bytes.Equal(response.body, content) {
			t.Fatalf("real entity bytes differ status=%d", response.status)
		}
	})
	if id == "" || object == "" || t.Failed() {
		t.Fatal("actual ready entity prerequisite failed")
	}
	t.Run("get_head_range_and_immutable_content_hash", func(t *testing.T) {
		before := assetBrowserSnapshot(t, f)
		for _, item := range []struct {
			method, rng string
			status      int
			body        []byte
		}{{"GET", "", 200, content}, {"HEAD", "", 200, nil}, {"GET", "bytes=2-6", 206, content[2:7]}} {
			response := assetDownloadRequest(f, item.method, f.org.Workspace.ID, object, item.rng)
			if response.err != nil || response.status != item.status || !bytes.Equal(response.body, item.body) || response.header.Get("Cache-Control") != "private, no-store" || response.header.Get("ETag") != "" {
				t.Fatalf("real byte/range/head contract method=%s status=%d transport_error=%t body_matches=%t cache_matches=%t etag_absent=%t", item.method, response.status, response.err != nil, bytes.Equal(response.body, item.body), response.header.Get("Cache-Control") == "private, no-store", response.header.Get("ETag") == "")
			}
		}
		response := assetDownloadRequest(f, "GET", f.org.Workspace.ID, object, "bytes=9999-")
		if response.status != 416 || bytes.Contains(response.body, content) {
			t.Fatal("out-of-range bytes not rejected")
		}
		if !reflect.DeepEqual(before, assetBrowserSnapshot(t, f)) {
			t.Fatal("byte reads mutated immutable data")
		}
	})
	t.Run("foreign_tenant_and_same_tenant_workspace_denied", func(t *testing.T) {
		before := assetBrowserSnapshot(t, f)
		for _, ws := range []string{f.foreign.Workspace.ID, f.second} {
			assetDownloadDenied(t, assetDownloadRequest(f, "GET", ws, object, ""), content)
		}
		if !reflect.DeepEqual(before, assetBrowserSnapshot(t, f)) {
			t.Fatal("cross-scope download changed domain")
		}
	})
	t.Run("historical_entity_requires_current_version_grant", func(t *testing.T) {
		next, body := assetDownloadPublish(t, f, id, 3)
		assetDownloadDenied(t, assetDownloadRequest(f, "GET", f.org.Workspace.ID, object, ""), content)
		assetBrowserRegrant(t, f, id)
		for _, item := range []struct {
			id   string
			body []byte
		}{{object, content}, {next, body}} {
			response := assetDownloadRequest(f, "GET", f.org.Workspace.ID, item.id, "")
			if response.status != 200 || !bytes.Equal(response.body, item.body) {
				t.Fatalf("historical/current bytes differ status=%d", response.status)
			}
		}
	})
	t.Run("signed_download_stale_epoch_denied", func(t *testing.T) {
		token, path, e := f.assets.IssueAssetDownload(context.Background(), f.actor, f.org.Workspace.ID, id, 2, object, 1, "GET")
		if e != nil {
			t.Fatal("real typed delegation issue failed")
		}
		assetBrowserRegrant(t, f, id)
		before := assetBrowserSnapshot(t, f)
		req, _ := http.NewRequest("GET", f.gisURL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		response, e := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if e != nil {
			t.Fatal("stale delegation request transport failed")
		}
		body, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()
		assetDownloadDenied(t, assetDownloadResponse{status: response.StatusCode, body: body, err: e}, content)
		if !reflect.DeepEqual(before, assetBrowserSnapshot(t, f)) {
			t.Fatal("stale delegation mutated business data")
		}
	})
	t.Run("farm_scoped_real_entity_bytes", func(t *testing.T) {
		farm, _ := f.create(t, "asset-bytes-parent", "fixture-north")
		grantParent := func(parent string, caps []string, expires *time.Time) (string, int64) {
			rows, e := f.platform.Service.ListBindings(context.Background(), f.owner, f.org.Workspace.ID)
			if e != nil {
				t.Fatal("farm binding inventory failed")
			}
			var revision int64
			for _, r := range rows {
				if r.SubjectID == f.actor && r.ResourceKind == "farm" && r.ResourceID == parent {
					revision = r.AuthVersion
				}
			}
			binding, e := f.platform.Service.GrantResourceBinding(context.Background(), f.owner, f.org.Workspace.ID, f.actor, ResourceRef{"farm", parent}, caps, expires, &revision)
			if e != nil {
				t.Fatal("explicit farm asset capabilities failed")
			}
			return binding.ID, binding.AuthVersion
		}
		grantParent(farm, []string{"agri.asset.create", "agri.asset.read", "agri.asset_download.read"}, nil)
		farmAsset := assetDownloadCreate(t, f, farm)
		farmObject, farmContent := assetDownloadPublish(t, f, farmAsset, 2)
		// Remove the direct branch entirely: each success below must use the
		// exact nonempty typed parent farm, never a stale direct grant.
		assetDownloadRevoke(t, f, farmAsset)
		next, nextContent := assetDownloadPublish(t, f, farmAsset, 3)
		entities := []struct {
			object  string
			content []byte
		}{{farmObject, farmContent}, {next, nextContent}}
		allowed := func() {
			for _, entity := range entities {
				response := assetDownloadRequest(f, "GET", f.org.Workspace.ID, entity.object, "")
				if response.err != nil || response.status != 200 || !bytes.Equal(response.body, entity.content) {
					t.Fatalf("explicit parent current/history real bytes differ status=%d", response.status)
				}
			}
		}
		denied := func() {
			before := assetBrowserSnapshot(t, f)
			for _, entity := range entities {
				assetDownloadDenied(t, assetDownloadRequest(f, "GET", f.org.Workspace.ID, entity.object, ""), entity.content)
			}
			if !reflect.DeepEqual(before, assetBrowserSnapshot(t, f)) {
				t.Fatal("denied inherited current/history download changed domain")
			}
		}
		allowed()
		grantParent(farm, []string{"agri.farm.read"}, nil)
		denied()
		other, _ := f.create(t, "asset-bytes-unrelated-parent", "fixture-north")
		grantParent(other, []string{"agri.asset_download.read"}, nil)
		denied()
		expires := time.Now().UTC().Add(3 * time.Second)
		grantParent(farm, []string{"agri.asset_download.read"}, &expires)
		allowed()
		if wait := time.Until(expires.Add(100 * time.Millisecond)); wait > 0 {
			time.Sleep(wait)
		}
		denied()
		bindingID, authVersion := grantParent(farm, []string{"agri.asset_download.read"}, nil)
		allowed()
		assetDownloadWriteRegistry(t, f, "revoked")
		denied()
		assetDownloadWriteRegistry(t, f, "approved")
		allowed()
		if e := f.platform.Service.RevokeBinding(context.Background(), f.owner, f.org.Workspace.ID, bindingID, &authVersion); e != nil {
			t.Fatal("explicit parent download revocation failed")
		}
		denied()
	})
	t.Run("rights_withdrawal_during_read_emits_no_bytes", func(t *testing.T) {
		assetDownloadBarrier(t, f, object, func() { assetDownloadWriteRegistry(t, f, "revoked") }, content)
		assetDownloadDenied(t, assetDownloadRequest(f, "GET", f.org.Workspace.ID, object, ""), content)
		assetDownloadWriteRegistry(t, f, "approved")
	})
	t.Run("binding_revocation_during_read_emits_no_bytes", func(t *testing.T) {
		assetDownloadBarrier(t, f, object, func() { assetDownloadRevoke(t, f, id) }, content)
		assetDownloadDenied(t, assetDownloadRequest(f, "GET", f.org.Workspace.ID, object, ""), content)
	})
	t.Run("real_logout_during_read_emits_no_bytes", func(t *testing.T) {
		assetBrowserRegrant(t, f, id)
		assetDownloadBarrier(t, f, object, func() {
			status, _ := f.request(t, f.actorCookie, "POST", "/api/auth/logout", map[string]any{}, "")
			if status != 200 {
				t.Fatalf("real logout status=%d", status)
			}
		}, content)
		assetDownloadDenied(t, assetDownloadRequest(f, "GET", f.org.Workspace.ID, object, ""), content)
	})
}
