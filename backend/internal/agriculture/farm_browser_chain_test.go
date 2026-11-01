package agriculture

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/auth"
	"yingce/backend/internal/browsersecurity"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/handler"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	platformservice "yingce/backend/internal/service"
)

type farmChainFixture struct {
	t                                                     *testing.T
	root, private, owner, actor, password, origin, second string
	org, foreign                                          OrganizationResult
	platform                                              *FarmPlatform
	seed, geo                                             *gorm.DB
	server                                                *httptest.Server
	actorCookie, ownerCookie                              *http.Cookie
	keys                                                  *ServiceKeyring
	gisURL                                                string
	gatewayPort                                           int
}

func newFarmChainFixture(t *testing.T, browser bool) *farmChainFixture {
	t.Helper()
	required := os.Getenv("AGRI_FARM_CHAIN_REQUIRED") == "1"
	for _, key := range []string{"AGRI_IDENTITY_PYTHON", "AGRI_TEST_PG_MIGRATION_DSN", "AGRI_TEST_PG_RUNTIME_DSN", "AGRI_TEST_GEO_MIGRATION_DSN", "AGRI_TEST_GEO_RUNTIME_DSN"} {
		if os.Getenv(key) == "" {
			if required {
				t.Fatalf("required farm dependency missing: %s", key)
			}
			t.Skip("owned dual PostgreSQL farm dependencies not configured")
		}
	}
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" {
		t.Fatal("farm fixture requires isolated databases")
	}
	component := os.Getenv("AGRI_FARM_AUTH_COMPONENT_ONLY") == "1"
	if browser || !component {
		for _, key := range []string{"REDIS_URL"} {
			if os.Getenv(key) == "" {
				t.Fatal("full farm runtime requires Redis")
			}
		}
	}
	root, e := filepath.Abs("../../..")
	if e != nil {
		t.Fatal(e)
	}
	f := &farmChainFixture{t: t, root: root, private: t.TempDir()}
	s, seed, owner, actor := postgresFixture(t)
	f.seed, f.owner, f.actor = seed, owner, actor
	ctx := context.Background()
	f.org, e = s.CreateOrganization(ctx, owner, "Farm chain main", "farm-chain-main-0001")
	if e != nil {
		t.Fatal("main organization setup failed")
	}
	f.foreign, e = s.CreateOrganization(ctx, owner, "Farm chain foreign", "farm-chain-foreign-0001")
	if e != nil {
		t.Fatal("foreign organization setup failed")
	}
	f.second = objectPostgresSecondWorkspace(t, seed, f.org, owner)
	invite, e := s.CreateInvite(ctx, owner, f.org.Workspace.ID, actor+"@fixture.invalid", "tenant_admin")
	if e != nil {
		t.Fatal("farm admission invitation failed")
	}
	if _, e = s.AcceptInvite(ctx, actor, invite.Token); e != nil {
		t.Fatal("farm admission failed")
	}
	roles := []string{"tenant_admin", "farm_manager"}
	if e = s.ChangeMember(ctx, owner, f.org.Workspace.ID, actor, &roles, nil); e != nil {
		t.Fatal("explicit farm business role setup failed")
	}
	f.password = auth.RandomToken()
	hash, e := auth.HashPassword(f.password)
	if e != nil {
		t.Fatal(e)
	}
	if e = seed.Model(&model.User{}).Where("id IN ?", []string{owner, actor}).Update("password_hash", hash).Error; e != nil {
		t.Fatal("private fixture credentials failed")
	}
	t.Cleanup(func() { seed.Where("user_id IN ?", []string{owner, actor}).Delete(&model.AuthSession{}) })
	makeKeys := func(id string) (*ServiceKeyring, ServiceIdentityKey) {
		pub, priv, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		now := time.Now().UTC().Truncate(time.Second)
		key := ServiceIdentityKey{ID: id, PublicKey: pub, PrivateKey: priv, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour)}
		ring, e := NewServiceKeyring([]ServiceIdentityKey{key}, id)
		if e != nil {
			t.Fatal(e)
		}
		return ring, key
	}
	platformKeys, platformKey := makeKeys("farm_platform")
	f.keys = platformKeys
	gisKeys, gisKey := makeKeys("farm_gis")
	f.platform = NewFarmPlatform(s, nil)
	callbackRouter := gin.New()
	callbackRouter.POST("/unavailable", func(c *gin.Context) { c.Status(404) })
	callback := httptest.NewServer(callbackRouter)
	t.Cleanup(callback.Close)
	keyJSON := func(k ServiceIdentityKey, private bool) map[string]any {
		out := map[string]any{"kid": k.ID, "public_key": base64.StdEncoding.EncodeToString(k.PublicKey), "not_before": k.NotBefore.Format(time.RFC3339), "not_after": k.NotAfter.Format(time.RFC3339), "revoked": false}
		if private {
			out["seed"] = base64.StdEncoding.EncodeToString(k.PrivateKey.Seed())
		}
		return out
	}
	config := filepath.Join(f.private, "farm-gis.json")
	dictionary := map[string]any{"schema_version": "agriculture-farm-dictionary/1", "dictionary_version": "fixture-v1", "regions": []any{map[string]any{"code": "fixture-north", "name": "Northern fixture", "timezones": []string{"Asia/Shanghai"}, "area_units": []string{"m2", "ha", "mu"}}, map[string]any{"code": "fixture-south", "name": "Southern fixture", "timezones": []string{"Asia/Shanghai", "UTC"}, "area_units": []string{"m2", "ha", "mu"}}}}
	chainPrivate(t, config, map[string]any{"platform": callback.URL, "platform_keys": []any{keyJSON(platformKey, false)}, "callback_key": keyJSON(gisKey, true), "geo_migration_dsn": os.Getenv("AGRI_TEST_GEO_MIGRATION_DSN"), "geo_runtime_dsn": os.Getenv("AGRI_TEST_GEO_RUNTIME_DSN"), "dictionary": dictionary})
	child := chainStart(t, os.Getenv("AGRI_IDENTITY_PYTHON"), filepath.Join(root, "services/agri-gis/backend"), filepath.Join(root, "services/agri-gis/backend/tests/farm_http_fixture.py"), config)
	var ready struct {
		Port       int  `json:"port"`
		FarmRoutes bool `json:"farm_routes"`
	}
	chainLine(t, child, 60*time.Second, &ready)
	if ready.Port < 1 || ready.Port > 65535 || !ready.FarmRoutes {
		t.Fatal("farm GIS readiness invalid")
	}
	f.gisURL = fmt.Sprintf("http://127.0.0.1:%d", ready.Port)
	t.Setenv("CANVAS_ALLOWED_PRIVATE_UPSTREAM_HOSTS", "127.0.0.1")
	client, e := NewGISClient(f.gisURL, f.keys)
	if e != nil {
		t.Fatal(e)
	}
	client.client.Transport = farmChainTransport{base: client.client.Transport, t: t}
	f.platform.Client = client
	f.platform.Service.Ownership = client
	f.platform.Service.RegisterInternalAuthority(callbackRouter, gisKeys)
	f.platform.RegisterInternal(callbackRouter, gisKeys)
	f.geo, e = gorm.Open(postgres.Open(os.Getenv("AGRI_TEST_GEO_MIGRATION_DSN")), &gorm.Config{Logger: logger.Discard})
	if e != nil {
		t.Fatal("GIS assertion connection unavailable")
	}
	pool, _ := f.geo.DB()
	t.Cleanup(func() { pool.Close() })
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	f.gatewayPort = listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	f.origin = fmt.Sprintf("http://127.0.0.1:%d", f.gatewayPort)
	keyPath := filepath.Join(f.private, "csrf.key")
	key := make([]byte, 32)
	if _, e = rand.Read(key); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(keyPath, key, 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("FLOW_AGRICULTURE_ENABLED", "true")
	t.Setenv("FLOW_AGRICULTURE_EXTERNAL_ORIGIN", f.origin)
	t.Setenv("FLOW_AGRICULTURE_ALLOW_HTTP_LOOPBACK_DEV", "true")
	t.Setenv("FLOW_AGRICULTURE_CSRF_KEY_FILE", keyPath)
	policy, e := browsersecurity.FromEnv()
	if e != nil {
		t.Fatal(e)
	}
	svc := platformservice.New(repository.New(s.DB), filepath.Join(f.private, "platform"))
	t.Cleanup(func() { _ = svc.Close() })
	if !component {
		if e = svc.ValidateRuntime(); e != nil {
			t.Fatal("full farm runtime validation failed")
		}
	}
	handler.ConfigureRuntime(svc)
	t.Cleanup(func() { handler.ConfigureRuntime(nil) })
	router := gin.New()
	router.Use(policy.Middleware())
	handler.RegisterAuthRoutes(router.Group("/api"), svc)
	API{Service: f.platform.Service, Farms: f.platform, User: func(c *gin.Context) (*model.User, error) {
		cookie, _ := c.Cookie(auth.SessionCookieName)
		return svc.CurrentUser(cookie)
	}}.Register(router.Group("/api"))
	f.server = httptest.NewServer(router)
	t.Cleanup(f.server.Close)
	login := func(id string) *http.Cookie {
		result, e := svc.Login(auth.LoginRequest{Username: id, Password: f.password})
		if e != nil {
			t.Fatal("persisted farm session login failed")
		}
		return &http.Cookie{Name: auth.SessionCookieName, Value: result.Session, Path: "/", HttpOnly: true}
	}
	f.actorCookie, f.ownerCookie = login(actor), login(owner)
	return f
}

func (f *farmChainFixture) request(t *testing.T, cookie *http.Cookie, method, path string, payload any, key string) (int, map[string]any) {
	t.Helper()
	var body []byte
	if payload != nil {
		var e error
		body, e = json.Marshal(payload)
		if e != nil {
			t.Fatal(e)
		}
	}
	request, e := http.NewRequest(method, f.server.URL+path, bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	request.Header.Set("Origin", f.origin)
	request.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	if method != "GET" && method != "HEAD" {
		csrf, _ := http.NewRequest("GET", f.server.URL+"/api/auth/csrf", nil)
		if cookie != nil {
			csrf.AddCookie(cookie)
		}
		csrf.Header.Set("Origin", f.origin)
		response, e := f.server.Client().Do(csrf)
		if e != nil {
			t.Fatal(e)
		}
		var v struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		e = json.NewDecoder(response.Body).Decode(&v)
		response.Body.Close()
		if e != nil || response.StatusCode != 200 {
			t.Fatal("real CSRF failed")
		}
		for _, c := range response.Cookies() {
			request.AddCookie(c)
		}
		request.Header.Set(browsersecurity.CSRFHeader, v.Data.Token)
	}
	response, e := f.server.Client().Do(request)
	if e != nil {
		t.Fatal("farm HTTP failed")
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if e != nil {
		t.Fatal(e)
	}
	var out map[string]any
	if json.Unmarshal(raw, &out) != nil {
		t.Fatalf("farm response not JSON, status=%d", response.StatusCode)
	}
	return response.StatusCode, out
}

func (f *farmChainFixture) snapshot(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, scope := range []struct{ tenant, workspace string }{{f.org.Tenant.ID, f.org.Workspace.ID}, {f.org.Tenant.ID, f.second}, {f.foreign.Tenant.ID, f.foreign.Workspace.ID}} {
		for _, database := range []struct {
			name   string
			db     *gorm.DB
			tables []string
		}{{"platform", f.seed, []string{"flow_agriculture_resource_bindings", "flow_agriculture_authorization_epochs", "flow_agriculture_farm_operations", "flow_agriculture_farm_events", "flow_command_outbox", "flow_operation_projections"}}, {"geo", f.geo, []string{"agriculture_farms", "agriculture_farm_revisions", "agriculture_farm_commands", "agriculture_farm_outbox", "agriculture_farm_dictionaries"}}} {
			e := database.db.Transaction(func(tx *gorm.DB) error {
				if e := tx.Exec("SELECT set_config('app.tenant_ids',?,true),set_config('app.workspace_id',?,true)", scope.tenant, scope.workspace).Error; e != nil {
					return e
				}
				for _, table := range database.tables {
					var raw string
					if e := tx.Raw("SELECT coalesce(jsonb_agg(to_jsonb(f) ORDER BY to_jsonb(f)::text),'[]')::text FROM (SELECT * FROM "+table+" WHERE tenant_id=?) f", scope.tenant).Scan(&raw).Error; e != nil {
						return e
					}
					out[database.name+":"+scope.workspace+":"+table] = raw
				}
				return nil
			})
			if e != nil {
				t.Fatalf("farm snapshot failed: %T", e)
			}
		}
	}
	return out
}

func (f *farmChainFixture) deny(t *testing.T, cookie *http.Cookie, method, path string, payload any, key string, want int) {
	t.Helper()
	before := f.snapshot(t)
	status, body := f.request(t, cookie, method, path, payload, key)
	if status != want {
		t.Fatalf("farm denial expected %d, got %d", want, status)
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) {
		t.Fatal("rejected farm request changed business state")
	}
	if body["data"] != nil {
		t.Fatal("farm denial disclosed data")
	}
}

type farmChainTransport struct {
	base http.RoundTripper
	t    *testing.T
}

type farmChainOfflineTransport struct{}

func (farmChainOfflineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("fixture transport unavailable")
}

func (x farmChainTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	base := x.base
	if base == nil {
		base = http.DefaultTransport
	}
	res, err := base.RoundTrip(r)
	if err == nil && res.StatusCode >= 400 {
		// Fixed endpoint classification and status only: no body, IDs or credentials.
		stage := "resource"
		if r.URL.Path == "/internal/agri/v1/farms/preview-create" {
			stage = "preview_create"
		}
		x.t.Logf("farm GIS response stage=%s status=%d", stage, res.StatusCode)
	}
	return res, err
}

func farmInput(code, region string) map[string]any {
	return map[string]any{"external_code": code, "name": "Farm fixture " + code, "operator_name": "Fixture cooperative", "region_code": region, "dictionary_version": "fixture-v1", "timezone": "Asia/Shanghai", "area_unit": "ha", "source_crs": "EPSG:4326", "coordinate_order": "longitude_latitude", "data_class": "demo", "geometry": map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{110, 20}, {110.01, 20}, {110.01, 20.01}, {110, 20.01}, {110, 20}}}}}
}
func (f *farmChainFixture) grants() []FarmGrant {
	return []FarmGrant{{SubjectUserID: f.actor, Capabilities: []string{"agri.farm.read", "agri.farm.update", "agri.farm.archive"}}}
}
func farmData(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	data, ok := value["data"].(map[string]any)
	if !ok {
		t.Fatal("expected canonical farm data")
	}
	return data
}
func (f *farmChainFixture) preview(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, _ := json.Marshal(payload)
	var decoded farmWriteBody
	if strictFarmJSON(raw, &decoded, 1<<20) != nil {
		t.Fatal("farm fixture preview failed exact DTO decode")
	}
	check := FarmPayload{Action: decoded.Action, FarmID: decoded.FarmID, ExpectedRevision: decoded.ExpectedRevision, Farm: decoded.Farm, InitialGrants: decoded.InitialGrants, Reason: decoded.Reason}
	if validateFarmPayload(&check) != nil {
		t.Fatal("farm fixture preview failed payload validation")
	}
	status, body := f.request(t, f.actorCookie, "POST", "/api/agri/v1/farm-previews", payload, "")
	if status != 200 {
		t.Fatalf("real farm preview failed status=%d", status)
	}
	id, ok := farmData(t, body)["preview_id"].(string)
	if !ok || id == "" {
		t.Fatal("preview id absent")
	}
	return id
}
func (f *farmChainFixture) create(t *testing.T, code, region string) (string, map[string]any) {
	t.Helper()
	input := farmInput(code, region)
	preview := f.preview(t, map[string]any{"workspace_id": f.org.Workspace.ID, "action": "create", "farm": input, "initial_grants": f.grants()})
	body := map[string]any{"workspace_id": f.org.Workspace.ID, "preview_id": preview, "farm": input, "initial_grants": f.grants()}
	status, response := f.request(t, f.actorCookie, "POST", "/api/agri/v1/farms", body, "fixture-create-"+code)
	if status != 201 {
		t.Fatalf("actual farm create failed status=%d", status)
	}
	data := farmData(t, response)
	if data["state"] != "completed" {
		t.Fatal("farm creation not completed")
	}
	id, ok := data["farm_id"].(string)
	if !ok || id == "" {
		t.Fatal("actual farm id absent")
	}
	return id, body
}

func TestFarmHTTPPostgresChain(t *testing.T) {
	f := newFarmChainFixture(t, false)
	var north, south string
	var replayBody map[string]any
	t.Run("create_explicit_grants_and_regions", func(t *testing.T) {
		north, replayBody = f.create(t, "north", "fixture-north")
		south, _ = f.create(t, "south", "fixture-south")
		for _, id := range []string{north, south} {
			status, body := f.request(t, f.actorCookie, "GET", "/api/agri/v1/farms/"+id+"?workspace_id="+f.org.Workspace.ID, nil, "")
			if status != 200 {
				t.Fatalf("created farm read status=%d", status)
			}
			data := farmData(t, body)
			if data["data_class"] != "demo" || data["revision"] != float64(1) {
				t.Fatal("farm identity/version/data class missing")
			}
		}
		status, body := f.request(t, f.actorCookie, "GET", "/api/agri/v1/farms?workspace_id="+f.org.Workspace.ID, nil, "")
		if status != 200 || len(body["data"].([]any)) != 2 {
			t.Fatal("authorized multi farm list missing")
		}
	})
	if north == "" || south == "" {
		t.Fatal("real creation precondition failed")
	}
	t.Run("governance_without_content_grant", func(t *testing.T) {
		f.deny(t, f.ownerCookie, "GET", "/api/agri/v1/farms/"+north+"?workspace_id="+f.org.Workspace.ID, nil, "", 404)
		status, body := f.request(t, f.ownerCookie, "GET", "/api/agri/v1/farms?workspace_id="+f.org.Workspace.ID, nil, "")
		if status != 200 || len(body["data"].([]any)) != 0 {
			t.Fatal("owner gained implicit content")
		}
	})
	t.Run("cross_tenant_and_workspace", func(t *testing.T) {
		for _, ws := range []string{f.second, f.foreign.Workspace.ID} {
			f.deny(t, f.actorCookie, "GET", "/api/agri/v1/farms/"+north+"?workspace_id="+ws, nil, "", 404)
			f.deny(t, f.actorCookie, "GET", "/api/agri/v1/farms?workspace_id="+ws, nil, "", 404)
		}
		f.deny(t, nil, "GET", "/api/agri/v1/farms?workspace_id="+f.org.Workspace.ID, nil, "", 401)
	})
	t.Run("preview_hash_expiry_and_scope", func(t *testing.T) {
		input := farmInput("preview", "fixture-north")
		preview := f.preview(t, map[string]any{"workspace_id": f.org.Workspace.ID, "action": "create", "farm": input, "initial_grants": f.grants()})
		changed := farmInput("changed", "fixture-north")
		f.deny(t, f.actorCookie, "POST", "/api/agri/v1/farms", map[string]any{"workspace_id": f.org.Workspace.ID, "preview_id": preview, "farm": changed, "initial_grants": f.grants()}, "preview-hash-key", 409)
		// Preview expiry is an immutable fact. Advance the platform's existing
		// injectable clock rather than rewriting facts or disabling the trigger.
		originalClock := f.platform.Service.Now
		defer func() { f.platform.Service.Now = originalClock }()
		future := time.Now().Add(6 * time.Minute)
		f.platform.Service.Now = func() time.Time { return future }
		f.deny(t, f.actorCookie, "POST", "/api/agri/v1/farms", map[string]any{"workspace_id": f.org.Workspace.ID, "preview_id": preview, "farm": input, "initial_grants": f.grants()}, "expired-preview-key", 409)
		f.platform.Service.Now = originalClock
		for _, invalid := range []map[string]any{func() map[string]any {
			v := farmInput("invalid", "fixture-north")
			v["geometry"] = map[string]any{"type": "Polygon", "coordinates": [][][]float64{{{110, 20}, {111, 21}, {110, 21}, {111, 20}, {110, 20}}}}
			return v
		}(), func() map[string]any { v := farmInput("invalid", "fixture-north"); delete(v, "source_crs"); return v }()} {
			before := f.snapshot(t)
			status, _ := f.request(t, f.actorCookie, "POST", "/api/agri/v1/farm-previews", map[string]any{"workspace_id": f.org.Workspace.ID, "action": "create", "farm": invalid, "initial_grants": f.grants()}, "")
			if status != 400 && status != 422 {
				t.Fatalf("invalid geometry/CRS not rejected: %d", status)
			}
			if !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("invalid preview left business state")
			}
		}
	})
	t.Run("idempotent_and_conflicting_requests", func(t *testing.T) {
		before := f.snapshot(t)
		status, body := f.request(t, f.actorCookie, "POST", "/api/agri/v1/farms", replayBody, "fixture-create-north")
		if status != 201 || farmData(t, body)["farm_id"] != north || !reflect.DeepEqual(before, f.snapshot(t)) {
			t.Fatal("same-key replay changed business state")
		}
		changed := map[string]any{}
		for k, v := range replayBody {
			changed[k] = v
		}
		changed["farm"] = farmInput("another", "fixture-north")
		f.deny(t, f.actorCookie, "POST", "/api/agri/v1/farms", changed, "fixture-create-north", 409)
	})
	t.Run("revision_race_and_archive", func(t *testing.T) {
		input := farmInput("north", "fixture-north")
		delete(input, "external_code")
		delete(input, "data_class")
		input["name"] = "Updated fixture"
		payload := map[string]any{"workspace_id": f.org.Workspace.ID, "action": "update", "farm_id": north, "expected_revision": 1, "farm": input}
		one, two := f.preview(t, payload), f.preview(t, payload)
		body := map[string]any{"workspace_id": f.org.Workspace.ID, "preview_id": one, "expected_revision": 1, "farm": input}
		status, _ := f.request(t, f.actorCookie, "PATCH", "/api/agri/v1/farms/"+north, body, "update-one-key")
		if status != 200 {
			t.Fatalf("actual update status=%d", status)
		}
		body["preview_id"] = two
		f.deny(t, f.actorCookie, "PATCH", "/api/agri/v1/farms/"+north, body, "update-two-key", 409)
		preview := f.preview(t, map[string]any{"workspace_id": f.org.Workspace.ID, "action": "archive", "farm_id": north, "expected_revision": 2, "reason": "fixture archival"})
		status, _ = f.request(t, f.actorCookie, "POST", "/api/agri/v1/farms/"+north+"/archive", map[string]any{"workspace_id": f.org.Workspace.ID, "preview_id": preview, "expected_revision": 2, "reason": "fixture archival"}, "archive-key")
		if status != 200 {
			t.Fatalf("actual archive status=%d", status)
		}
		beforeReplay := f.snapshot(t)
		status, replay := f.request(t, f.actorCookie, "POST", "/api/agri/v1/farms/"+north+"/archive", map[string]any{"workspace_id": f.org.Workspace.ID, "preview_id": preview, "expected_revision": 2, "reason": "fixture archival"}, "archive-key")
		if status != 200 || farmData(t, replay)["state"] != "completed" || !reflect.DeepEqual(beforeReplay, f.snapshot(t)) {
			t.Fatal("archive replay failed or changed business state")
		}
		status, out := f.request(t, f.actorCookie, "GET", "/api/agri/v1/farms/"+north+"?workspace_id="+f.org.Workspace.ID, nil, "")
		if status != 200 || farmData(t, out)["status"] != "archived" {
			t.Fatal("archived history not readable with current grant")
		}
		if !reflect.DeepEqual(farmData(t, out)["allowed_actions"], []any{"agri.farm.read"}) {
			t.Fatal("archived farm still writable")
		}
	})
	t.Run("current_revocation_blocks_replay", func(t *testing.T) {
		suspended, active := "suspended", "active"
		if e := f.platform.Service.ChangeMember(context.Background(), f.owner, f.org.Workspace.ID, f.actor, nil, &suspended); e != nil {
			t.Fatal(e)
		}
		defer func() {
			if e := f.platform.Service.ChangeMember(context.Background(), f.owner, f.org.Workspace.ID, f.actor, nil, &active); e != nil {
				t.Error(e)
			}
		}()
		// Existing workspace admission deliberately conceals unavailable scopes.
		f.deny(t, f.actorCookie, "GET", "/api/agri/v1/farms/"+south+"?workspace_id="+f.org.Workspace.ID, nil, "", 404)
		f.deny(t, f.actorCookie, "POST", "/api/agri/v1/farms", replayBody, "fixture-create-north", 404)
	})
	t.Run("signed_workspace_profiles_current_authority", func(t *testing.T) {
		previewInput := farmInput("withdrawn-preview", "fixture-north")
		previewID := f.preview(t, map[string]any{"workspace_id": f.org.Workspace.ID, "action": "create", "farm": previewInput, "initial_grants": f.grants()})
		pendingInput := farmInput("withdrawn-pending", "fixture-north")
		pendingID := f.preview(t, map[string]any{"workspace_id": f.org.Workspace.ID, "action": "create", "farm": pendingInput, "initial_grants": f.grants()})
		// Real confirmation/queue/unknown-result persistence under a controlled
		// transport failure, never a fabricated success or seeded domain farm.
		func() {
			previous := f.platform.Client.client.Transport
			f.platform.Client.client.Transport = farmChainOfflineTransport{}
			defer func() { f.platform.Client.client.Transport = previous }()
			status, body := f.request(t, f.actorCookie, "POST", "/api/agri/v1/farms", map[string]any{"workspace_id": f.org.Workspace.ID, "preview_id": pendingID, "farm": pendingInput, "initial_grants": f.grants()}, "withdrawn-pending-key")
			if status != 202 || farmData(t, body)["state"] != "pending" {
				t.Fatal("transport failure did not retain a real pending operation")
			}
		}()
		operations := map[string]model.AgricultureFarmOperation{}
		for action, id := range map[string]string{"farm.preview.create": previewID, "farm.create": pendingID, "farm.receipt": replayBody["preview_id"].(string)} {
			var op model.AgricultureFarmOperation
			if e := flow.TenantTx(f.seed, f.org.Tenant.ID, func(tx *gorm.DB) error { return tx.Where("id=?", id).First(&op).Error }); e != nil {
				t.Fatal("actual intent unavailable")
			}
			operations[action] = op
		}
		// Fresh cryptographically valid contexts are deliberately signed by this
		// test issuer after withdrawal. GIS must call the real current platform
		// authority rather than treat a valid signature as an authorization grant.
		var epoch int64
		if e := flow.TenantTx(f.seed, f.org.Tenant.ID, func(tx *gorm.DB) error {
			var e error
			epoch, e = currentAuthorizationVersion(tx, f.org.Tenant.ID)
			return e
		}); e != nil {
			t.Fatal(e)
		}
		// Prove each command profile has an existing, hash-bound intent which
		// production current-authority logic accepts BEFORE membership withdrawal.
		for action, op := range operations {
			body, err := farmCommandBody(op, action == "farm.receipt")
			if err != nil {
				t.Fatal("actual command body unavailable")
			}
			var value any
			_ = json.Unmarshal(body, &value)
			_, digest, err := farmCanonical(value)
			if err != nil {
				t.Fatal("actual command hash unavailable")
			}
			err = f.platform.Service.withWorkspace(context.Background(), f.actor, f.org.Workspace.ID, false, func(tx *gorm.DB, scope workspaceContext) error {
				return f.platform.checkAuthority(tx, scope, farmAuthorityRequest{ActorUserID: f.actor, TenantID: f.org.Tenant.ID, WorkspaceID: f.org.Workspace.ID, Action: action, OperationID: op.ID, RequestHash: op.RequestHash, GrantPlanHash: op.GrantPlanHash, AuthorizationVersion: epoch, FarmIDs: []string{}, RequestPayloadHash: digest})
			})
			if err != nil {
				t.Fatalf("valid-before-withdrawal authority failed action=%s", action)
			}
		}
		suspended, active := "suspended", "active"
		if e := f.platform.Service.ChangeMember(context.Background(), f.owner, f.org.Workspace.ID, f.actor, nil, &suspended); e != nil {
			t.Fatal(e)
		}
		defer func() {
			_ = f.platform.Service.ChangeMember(context.Background(), f.owner, f.org.Workspace.ID, f.actor, nil, &active)
		}()
		for _, item := range []struct {
			action, path string
			body         map[string]any
			ids          []string
		}{{"farm.context", "/internal/agri/v1/farms/context", map[string]any{}, []string{}}, {"farm.list", "/internal/agri/v1/farms/list", map[string]any{"farm_ids": []string{south}, "status": "active", "limit": 10, "after_id": "", "search": ""}, []string{south}}, {"farm.history.read", "/internal/agri/v1/farms/history", map[string]any{"farm_id": north}, []string{north}}, {"farm.preview.create", "/internal/agri/v1/farms/preview-create", map[string]any{}, []string{}}, {"farm.create", "/internal/agri/v1/farms/commands/create", map[string]any{}, []string{}}, {"farm.receipt", "/internal/agri/v1/farms/commands/receipt", map[string]any{}, []string{}}} {
			op := flow.NewID("fop_test_")
			requestHash, grantHash := zeroFarmHash, zeroFarmHash
			if item.action == "farm.preview.create" || item.action == "farm.create" || item.action == "farm.receipt" {
				intent := operations[item.action]
				op, requestHash, grantHash = intent.ID, intent.RequestHash, intent.GrantPlanHash
				raw, err := farmCommandBody(intent, item.action == "farm.receipt")
				if err != nil || json.Unmarshal(raw, &item.body) != nil {
					t.Fatal("actual command body unavailable")
				}
			}
			raw, _, _ := farmCanonical(item.body)
			bound, canonical, e := bindIdentityRequest("POST", item.path, raw, op, false)
			if e != nil {
				t.Fatal(e)
			}
			now := time.Now().UTC().Truncate(time.Second)
			until := now.Add(30 * time.Second)
			claims := map[string]any{"schema_version": "1.0", "issuer": "flow-platform", "subject": "flow-platform", "audience": "agri-gis", "actor": identityActor{Kind: "user", ID: f.actor}, "tenant_id": f.org.Tenant.ID, "workspace_id": f.org.Workspace.ID, "action": item.action, "authorization_version": epoch, "policy_version": authorizationPolicyVersion, "operation_id": op, "request_hash": requestHash, "grant_plan_hash": grantHash, "farm_ids": item.ids, "issued_at": now.Format(time.RFC3339), "expires_at": until.Format(time.RFC3339), "jti": flow.NewID("jti_"), "trace_id": flow.NewID("trace_"), "request_id": flow.NewID("req_"), "request": bound}
			token, e := f.keys.sign(farmWorkspaceType, claims, now, until)
			if e != nil {
				t.Fatal(e)
			}
			before := f.snapshot(t)
			request, _ := http.NewRequest("POST", f.gisURL+item.path, bytes.NewReader(canonical))
			request.Header.Set("Authorization", "Bearer "+token)
			request.Header.Set("Content-Type", "application/json")
			response, e := http.DefaultClient.Do(request)
			if e != nil {
				t.Fatal(e)
			}
			raw, _ = io.ReadAll(io.LimitReader(response.Body, 65536))
			response.Body.Close()
			if response.StatusCode != 403 {
				t.Fatalf("signed withdrawn %s status=%d", item.action, response.StatusCode)
			}
			if strings.Contains(string(raw), f.actor) || strings.Contains(string(raw), south) || !reflect.DeepEqual(before, f.snapshot(t)) {
				t.Fatal("signed denial leaked details or changed business state")
			}
		}
	})
}

const farmBrowserRequiredChecks = "production_nginx_configuration_valid real_react_login_shared_vue_session http_only_platform_cookie north_created_with_explicit_grants south_created_with_explicit_grants duplicate_click_single_create created_farms_survive_reload explicit_region_unit_crs_demo openlayers_real_canvas invalid_geometry_no_write missing_crs_no_preview duplicate_external_code_rejected north_ui_update_revision_two concurrent_real_api_update_revision_three stale_revision_409_preserves_draft archive_ui_history_readonly foreign_workspace_denied same_tenant_other_workspace_denied same_cookie_after_revocation revoked_old_page_clears_content revoked_history_navigation_no_content all_database_checkpoints_acknowledged no_internal_credentials_identity_spoofing_or_external_requests"

func TestFarmBrowserPostgresChain(t *testing.T) {
	for _, key := range []string{"CHROME_BIN", "NGINX_BIN", "NGINX_MIME_TYPES", "REDIS_URL"} {
		if os.Getenv(key) == "" {
			if os.Getenv("AGRI_FARM_CHAIN_REQUIRED") == "1" {
				t.Fatalf("required farm browser dependency missing: %s", key)
			}
			t.Skip("actual farm browser dependencies not configured")
		}
	}
	f := newFarmChainFixture(t, true)
	for _, path := range []string{"web/dist-agriculture/index.html", "services/agri-gis/frontend/dist-agriculture/index.html", "scripts/agriculture/farm-browser-e2e.mjs"} {
		if _, e := os.Stat(filepath.Join(f.root, path)); e != nil {
			t.Fatal("actual farm browser source/build missing")
		}
	}
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal("Node required")
	}
	reportPath := filepath.Join(f.private, "farm-browser-report.json")
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		raw, err := os.ReadFile(reportPath)
		if err != nil || len(raw) > 1<<20 {
			return
		}
		var v struct {
			Result  string `json:"result"`
			Stage   string `json:"stage"`
			Results []struct {
				Name   string `json:"name"`
				Result string `json:"result"`
			} `json:"results"`
		}
		if json.Unmarshal(raw, &v) != nil || (v.Result != "pass" && v.Result != "fail") {
			return
		}
		stages := strings.Fields("preflight react_login create_two_regions create_north_form create_north_preview create_north_confirm create_north_reload create_south_form create_south_preview create_south_confirm create_south_reload invalid_input_negatives ui_update_and_stale_revision archive_history cross_scope_negatives awaiting_revocation complete")
		stageKnown := false
		for _, stage := range stages {
			if stage == v.Stage {
				stageKnown = true
			}
		}
		if !stageKnown {
			return
		}
		allowed := map[string]bool{}
		for _, name := range strings.Fields(farmBrowserRequiredChecks) {
			allowed[name] = true
		}
		for _, check := range v.Results {
			if !allowed[check.Name] || check.Result != "pass" {
				return
			}
			delete(allowed, check.Name)
		}
		clean, _ := json.Marshal(map[string]any{"schema_version": "1.0", "result": v.Result, "stage": v.Stage, "results": v.Results, "diagnostic_only": true, "data_class": "fixture", "production_acceptance": false})
		dir := filepath.Join(f.root, ".local/evidence")
		if os.MkdirAll(dir, 0700) == nil {
			_ = os.WriteFile(filepath.Join(dir, "farm-chain-browser.json"), clean, 0600)
		}
	})
	config := filepath.Join(f.private, "farm-browser.json")
	chainPrivate(t, config, map[string]any{"platform_url": f.server.URL, "gateway_port": f.gatewayPort, "username": f.actor, "password": f.password, "workspace_id": f.org.Workspace.ID, "foreign_workspace_id": f.foreign.Workspace.ID, "second_workspace_id": f.second, "actor_id": f.actor, "dictionary_version": "fixture-v1", "region_north": "fixture-north", "region_south": "fixture-south", "area_units": []string{"m2", "ha", "mu"}, "report_path": reportPath})
	child := chainStart(t, node, f.root, filepath.Join(f.root, "scripts/agriculture/farm-browser-e2e.mjs"), config)
	expected := []string{"created", "updated", "archived", "revoke-request", "revoked-verified"}
	var frozen map[string]string
	for _, checkpoint := range expected {
		var event struct {
			Event      string `json:"event"`
			Checkpoint string `json:"checkpoint"`
		}
		chainLine(t, child, 90*time.Second, &event)
		if event.Event != "checkpoint" || event.Checkpoint != checkpoint {
			t.Fatal("unexpected farm browser checkpoint")
		}
		if checkpoint == "created" || checkpoint == "updated" || checkpoint == "archived" {
			type state struct {
				Code      string `gorm:"column:external_code"`
				Status    string
				Revision  int64
				DataClass string `gorm:"column:data_class"`
			}
			var rows []state
			e := f.geo.Transaction(func(tx *gorm.DB) error {
				if e := tx.Exec("SELECT set_config('app.tenant_ids',?,true),set_config('app.workspace_id',?,true)", f.org.Tenant.ID, f.org.Workspace.ID).Error; e != nil {
					return e
				}
				return tx.Raw("SELECT external_code,status,revision,data_class FROM agriculture_farms WHERE tenant_id=? AND workspace_id=? ORDER BY external_code", f.org.Tenant.ID, f.org.Workspace.ID).Scan(&rows).Error
			})
			if e != nil || len(rows) != 2 {
				t.Fatal("browser checkpoint did not create exactly two actual farms")
			}
			northRevision := int64(1)
			northStatus := "active"
			if checkpoint == "updated" {
				northRevision = 3
			}
			if checkpoint == "archived" {
				northRevision = 4
				northStatus = "archived"
			}
			if rows[0].Code != "browser-north" || rows[0].Revision != northRevision || rows[0].Status != northStatus || rows[1].Code != "browser-south" || rows[1].Revision != 1 || rows[1].Status != "active" || rows[0].DataClass != "demo" || rows[1].DataClass != "demo" {
				t.Fatal("actual farm lifecycle differs from browser checkpoint")
			}
			var grants int64
			e = flow.TenantTx(f.seed, f.org.Tenant.ID, func(tx *gorm.DB) error {
				return tx.Model(&model.AgricultureResourceBinding{}).Where("tenant_id=? AND workspace_id=? AND subject_id=? AND state='active'", f.org.Tenant.ID, f.org.Workspace.ID, f.actor).Count(&grants).Error
			})
			if e != nil || grants != 2 {
				t.Fatal("browser farms lack exact explicit grants")
			}
		}
		if checkpoint == "revoke-request" {
			suspended := "suspended"
			if e = f.platform.Service.ChangeMember(context.Background(), f.owner, f.org.Workspace.ID, f.actor, nil, &suspended); e != nil {
				t.Fatal("real browser membership revocation failed")
			}
			frozen = f.snapshot(t)
		}
		if checkpoint == "revoked-verified" && !reflect.DeepEqual(frozen, f.snapshot(t)) {
			t.Fatal("withdrawn browser changed domain state")
		}
		ack, _ := json.Marshal(map[string]string{"event": "ack", "checkpoint": checkpoint})
		if _, e = child.input.Write(append(ack, '\n')); e != nil {
			t.Fatal("browser acknowledgment failed")
		}
	}
	select {
	case <-child.done:
		if child.err != nil {
			t.Fatal("actual farm browser failed")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("farm browser did not finish")
	}
	raw, e := os.ReadFile(reportPath)
	if e != nil || len(raw) > 1024*1024 {
		t.Fatal("farm browser report missing")
	}
	var report struct {
		Result  string `json:"result"`
		Stage   string `json:"stage"`
		Results []struct {
			Name   string `json:"name"`
			Result string `json:"result"`
		} `json:"results"`
	}
	if json.Unmarshal(raw, &report) != nil || report.Result != "pass" || report.Stage != "complete" || len(report.Results) == 0 {
		t.Fatal("farm browser report not passed")
	}
	requiredChecks := strings.Fields(farmBrowserRequiredChecks)
	checks := make(map[string]bool, len(requiredChecks))
	for _, name := range requiredChecks {
		checks[name] = false
	}
	if len(report.Results) != len(checks) {
		t.Fatal("farm browser check set incomplete")
	}
	for _, r := range report.Results {
		seen, known := checks[r.Name]
		if r.Result != "pass" || !known || seen {
			t.Fatal("farm browser assertion failed")
		}
		checks[r.Name] = true
	}
	for _, secret := range []string{f.password, f.actorCookie.Value, f.ownerCookie.Value, os.Getenv("AGRI_TEST_PG_RUNTIME_DSN"), os.Getenv("AGRI_TEST_GEO_RUNTIME_DSN")} {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatal("farm browser report contains private material")
		}
	}
	out := filepath.Join(f.root, ".local/evidence")
	if e = os.MkdirAll(out, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(out, "farm-chain-browser.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}
