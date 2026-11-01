package agriculture

// DT024 exercises the actual platform PostgreSQL runtime and persisted login
// sessions. Domain receipts below are explicit fixtures, not a live GIS provider.
import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"yingce/backend/internal/auth"
	"yingce/backend/internal/browsersecurity"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
	"yingce/backend/internal/repository"
	platformservice "yingce/backend/internal/service"
)

// Test-only domain facts mint a fresh receipt per lookup within the production
// 60-second bound. No platform clock/expiry/authorization check is overridden.
type isolationFreshFacts struct {
	rows map[ResourceRef]ResourceOwnership
}

func (f *isolationFreshFacts) Resolve(_ context.Context, _ OwnershipContext, refs []ResourceRef) ([]ResourceOwnership, error) {
	out := make([]ResourceOwnership, 0, len(refs))
	now := time.Now().UTC()
	for _, ref := range refs {
		value, ok := f.rows[ref]
		if !ok {
			return nil, ErrNotFound
		}
		value.IssuedAt = now
		value.ExpiresAt = now.Add(time.Minute)
		out = append(out, value)
	}
	return out, nil
}

type isolationRoute struct{ ID, Method, Path, Body, Kind string }

// Each registered method/path must have exactly one executed matrix row. Do not
// replace this with a minimum route count: a new route requires reviewed cases.
func isolationRoutes(actor, inviteToken string) []isolationRoute {
	return []isolationRoute{
		{"P01", "GET", "/api/agriculture/workspaces", "", "list"},
		{"P02", "POST", "/api/agriculture/workspaces/:workspaceId/authorize", `{"action":"agri.parcel.update","resources":[{"kind":"parcel","id":"matrix_parcel"}]}`, "scope"},
		{"P03", "GET", "/api/agriculture/workspaces/:workspaceId/resource-bindings", "", "scope"},
		{"P04", "POST", "/api/agriculture/workspaces/:workspaceId/resource-bindings", fmt.Sprintf(`{"subjectId":%q,"resource":{"kind":"farm","id":"matrix_farm"},"capabilities":["agri.farm.read"],"expectedRevision":0}`, actor), "scope"},
		{"P05", "POST", "/api/agriculture/workspaces/:workspaceId/resource-bindings/:bindingId/revoke", `{"expectedRevision":1}`, "scope"},
		{"P06", "POST", "/api/agriculture/organizations", `{"name":"Matrix legitimate organization"}`, "organization"},
		{"P07", "POST", "/api/agriculture/invites/accept", fmt.Sprintf(`{"token":%q}`, inviteToken), "accept"},
		{"P08", "GET", "/api/agriculture/workspaces/:workspaceId/members", "", "scope"},
		{"P09", "POST", "/api/agriculture/workspaces/:workspaceId/invites", `{"email":"matrix-new@fixture.invalid","role":"viewer"}`, "scope"},
		{"P10", "POST", "/api/agriculture/workspaces/:workspaceId/invites/:inviteId/revoke", "", "scope"},
		{"P11", "PATCH", "/api/agriculture/workspaces/:workspaceId/members/:userId", `{"roles":["editor"]}`, "scope"},
		{"P12", "PATCH", "/api/agriculture/workspaces/:workspaceId/organization", `{"status":"disabled"}`, "scope"},
		{"P13", "POST", "/api/agri/v1/workspaces/:workspace_id/authorization", `{"action":"agri.parcel.update","resources":[{"kind":"parcel","id":"matrix_parcel"}]}`, "scope"},
		{"P14", "GET", "/api/agri/v1/workspaces/:workspace_id/resource-bindings", "", "scope"},
		{"P15", "POST", "/api/agri/v1/workspaces/:workspace_id/resource-bindings", fmt.Sprintf(`{"subject_user_id":%q,"resource":{"kind":"farm","id":"matrix_farm"},"capabilities":["agri.farm.read"],"expected_revision":0}`, actor), "scope"},
		{"P16", "POST", "/api/agri/v1/workspaces/:workspace_id/resource-bindings/:binding_id/revoke", `{"expected_revision":1}`, "scope"},
		{"P17", "GET", "/api/agri/v1/workspaces/:workspace_id/objects/:object_id/versions/:version", "", "closed_provider"},
		{"P18", "HEAD", "/api/agri/v1/workspaces/:workspace_id/objects/:object_id/versions/:version", "", "closed_provider"},
		{"P19", "GET", "/api/agri/v1/workspaces/:workspace_id/jobs/:job_id/events", "", "closed_provider"},
		{"I01", "POST", authorityCheckPath, `{}`, "internal"},
		{"I02", "POST", jobAuthorityCheckPath, `{}`, "internal"},
		{"P20", "GET", "/api/agri/v1/context", "", "farm_context"},
		{"P21", "GET", "/api/agri/v1/farms", "", "farm_list"},
		{"P22", "GET", "/api/agri/v1/farms/:farm_id", "", "farm_detail"},
		{"P23", "GET", "/api/agri/v1/farm-operations/:operation_id", "", "farm_operation"},
		{"P24", "POST", "/api/agri/v1/farm-previews", `{"workspace_id":"{{workspace}}","action":"create","farm":{},"initial_grants":[]}`, "farm_write"},
		{"P25", "POST", "/api/agri/v1/farms", `{"workspace_id":"{{workspace}}","preview_id":"fop_matrix","farm":{},"initial_grants":[]}`, "farm_write"},
		{"P26", "PATCH", "/api/agri/v1/farms/:farm_id", `{"workspace_id":"{{workspace}}","preview_id":"fop_matrix","expected_revision":1,"farm":{"name":"Fixture name"}}`, "farm_write"},
		{"P27", "POST", "/api/agri/v1/farms/:farm_id/archive", `{"workspace_id":"{{workspace}}","preview_id":"fop_matrix","expected_revision":1,"reason":"Fixture archive"}`, "farm_write"},
		{"I03", "POST", farmAuthorityPath, `{}`, "internal"},
	}
}

type isolationIdentity struct {
	Cookie  *http.Cookie
	CSRF    string
	Preauth []*http.Cookie
}
type isolationObservation struct {
	Route     string `json:"route"`
	Case      string `json:"case"`
	Status    int    `json:"status"`
	Unchanged bool   `json:"business_snapshot_unchanged"`
}

func TestAgricultureIsolationMatrixPostgres(t *testing.T) {
	if os.Getenv("AGRI_ISOLATION_REQUIRED") == "1" && (os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_PG_RUNTIME_DSN") == "" || os.Getenv("AGRI_TEST_PG_MIGRATION_DSN") == "") {
		t.Fatal("required isolation PostgreSQL configuration missing")
	}
	s, seed, owner, actor := postgresFixture(t)
	ctx := context.Background()
	org, e := s.CreateOrganization(ctx, owner, "Matrix main private tenant", "matrix-main-fixture-001")
	if e != nil {
		t.Fatal(e)
	}
	foreign, e := s.CreateOrganization(ctx, owner, "Matrix foreign private tenant", "matrix-foreign-001")
	if e != nil {
		t.Fatal(e)
	}
	invite, e := s.CreateInvite(ctx, owner, org.Workspace.ID, actor+"@fixture.invalid", "viewer")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AcceptInvite(ctx, actor, invite.Token); e != nil {
		t.Fatal(e)
	}
	wrongInvite, e := s.CreateInvite(ctx, owner, org.Workspace.ID, "different-recipient@fixture.invalid", "viewer")
	if e != nil {
		t.Fatal(e)
	}
	second := flow.NewID("ws_matrix_")
	e = flow.TenantTx(seed, org.Tenant.ID, func(tx *gorm.DB) error {
		for _, v := range []any{&model.Workspace{ID: second, TenantID: org.Tenant.ID, Product: Product, Name: "Matrix unjoined private workspace", Slug: "matrix-unjoined"}, &model.WorkspaceMembership{ID: flow.NewID("wm_"), WorkspaceID: second, UserID: owner, Role: "owner"}} {
			if e := tx.Create(v).Error; e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	facts := &isolationFreshFacts{rows: map[ResourceRef]ResourceOwnership{}}
	for _, ref := range []ResourceRef{{"farm", "matrix_farm"}, {"parcel", "matrix_parcel"}} {
		facts.rows[ref] = ResourceOwnership{Resource: ref, TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, FarmIDs: []string{"matrix_farm"}, Revision: 1, Status: "active", ReceiptID: "matrix_fixture_" + ref.ID, IssuedAt: now, ExpiresAt: now.Add(time.Minute)}
	}
	s.Ownership = facts
	binding, e := s.GrantResourceBinding(ctx, owner, org.Workspace.ID, actor, ResourceRef{"farm", "matrix_farm"}, []string{"agri.farm.read", "agri.parcel.read"}, nil, revisionPointer(0))
	if e != nil {
		t.Fatal(e)
	}
	svc := platformservice.New(repository.New(s.DB), t.TempDir())
	t.Cleanup(func() { svc.Close() })
	component := os.Getenv("AGRI_ISOLATION_AUTH_COMPONENT_ONLY") == "1"
	if component {
		t.Log("explicit auth-service component mode: full PostgreSQL/Redis application startup is NOT acceptance")
	} else if e := svc.ValidateRuntime(); e != nil {
		t.Fatal("full isolation matrix runtime unavailable; real Redis required")
	}
	login := func(id string) *http.Cookie {
		t.Helper()
		password := auth.RandomToken()
		hash, e := auth.HashPassword(password)
		if e != nil {
			t.Fatal(e)
		}
		if e = seed.Model(&model.User{}).Where("id=?", id).Update("password_hash", hash).Error; e != nil {
			t.Fatal(e)
		}
		result, e := svc.Login(auth.LoginRequest{Username: id, Password: password})
		if e != nil {
			t.Fatalf("real service login failed: %T", e)
		}
		t.Cleanup(func() { _ = seed.Where("user_id=?", id).Delete(&model.AuthSession{}).Error })
		return &http.Cookie{Name: auth.SessionCookieName, Value: result.Session, Path: "/", HttpOnly: true}
	}
	expiredCookie := login(actor)
	if e = seed.Model(&model.AuthSession{}).Where("user_id=?", actor).Update("expires_at", time.Now().Add(-time.Minute)).Error; e != nil {
		t.Fatal(e)
	}
	actorCookie, ownerCookie := login(actor), login(owner)
	router := gin.New()
	server := httptest.NewUnstartedServer(router)
	origin := "http://" + server.Listener.Addr().String()
	policy, e := browsersecurity.Parse(origin, true)
	if e != nil {
		t.Fatal(e)
	}
	router.Use(policy.Middleware())
	api := API{Service: s, User: func(c *gin.Context) (*model.User, error) {
		cookie, _ := c.Cookie(auth.SessionCookieName)
		return svc.CurrentUser(cookie)
	}}
	api.Register(router.Group("/api"))
	gisKeys, _ := identityTestKeys(t, time.Now().UTC(), 0x71)
	s.RegisterInternalAuthority(router, gisKeys)
	NewFarmPlatform(s, nil).RegisterInternal(router, gisKeys)
	server.Start()
	t.Cleanup(server.Close)
	identity := func(cookie *http.Cookie) isolationIdentity {
		t.Helper()
		r, _ := http.NewRequest("GET", server.URL+"/api/auth/csrf", nil)
		if cookie != nil {
			r.AddCookie(cookie)
		}
		r.Header.Set("Origin", origin)
		resp, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		var envelope struct {
			Data struct {
				Token string `json:"token"`
			} `json:"data"`
		}
		if e = json.NewDecoder(resp.Body).Decode(&envelope); e != nil || resp.StatusCode != 200 || len(envelope.Data.Token) != 64 {
			t.Fatal("actual CSRF issuer failed")
		}
		return isolationIdentity{Cookie: cookie, CSRF: envelope.Data.Token, Preauth: resp.Cookies()}
	}
	anonymous, viewer, admin := identity(nil), identity(actorCookie), identity(ownerCookie)
	expired := identity(expiredCookie)
	forged := identity(&http.Cookie{Name: auth.SessionCookieName, Value: "forged-not-a-session", Path: "/"})
	routes := isolationRoutes(actor, wrongInvite.Token)
	want, got := []string{}, []string{}
	for _, r := range routes {
		want = append(want, r.Method+" "+r.Path)
	}
	for _, r := range router.Routes() {
		got = append(got, r.Method+" "+r.Path)
	}
	sort.Strings(want)
	sort.Strings(got)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("registered route matrix differs: want=%v got=%v", want, got)
	}
	observations := []isolationObservation{}
	t.Cleanup(func() {
		if path := os.Getenv("AGRI_ISOLATION_MATRIX_REPORT"); path != "" {
			body, _ := json.MarshalIndent(map[string]any{"suite": "platform_postgres_route_matrix", "passed": !t.Failed(), "auth_component_only": component, "production_acceptance": false, "data_class": "fixture", "exact_routes": want, "observations": observations, "domain_provider": "explicit_fixture_receipts; object and job event production providers unavailable"}, "", "  ")
			if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				t.Error(e)
				return
			}
			if e := os.WriteFile(path, append(body, '\n'), 0600); e != nil {
				t.Error(e)
			}
		}
	})
	expand := func(path, workspace string) string {
		return strings.NewReplacer(":workspaceId", workspace, ":workspace_id", workspace, ":bindingId", binding.ID, ":binding_id", binding.ID, ":inviteId", wrongInvite.Invite.ID, ":userId", owner, ":object_id", "matrix_object", ":version", "1", ":job_id", "matrix_job", ":farm_id", "farm_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ":operation_id", "fop_matrix").Replace(path)
	}
	call := func(t *testing.T, row isolationRoute, scenario string, id isolationIdentity, workspace, body string, status int, extra http.Header) []byte {
		t.Helper()
		before, e := chainPlatformSnapshot(seed)
		if e != nil {
			t.Fatal("before snapshot failed")
		}
		path := expand(row.Path, workspace)
		if strings.HasPrefix(row.Kind, "farm_") && row.Method == "GET" {
			path += "?workspace_id=" + workspace
		}
		body = strings.ReplaceAll(body, "{{workspace}}", workspace)
		r, e := http.NewRequest(row.Method, server.URL+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(browsersecurity.CSRFHeader, id.CSRF)
		r.Header.Set("X-Idempotency-Key", "matrix-request-001")
		r.Header.Set("Idempotency-Key", "matrix-request-001")
		// Untrusted headers must never select the authenticated identity or scope.
		r.Header.Set("X-Tenant-ID", foreign.Tenant.ID)
		r.Header.Set("X-User-ID", owner)
		r.Header.Set("X-Role", "tenant_admin")
		if id.Cookie != nil {
			r.AddCookie(id.Cookie)
		}
		for _, cookie := range id.Preauth {
			r.AddCookie(cookie)
		}
		for k, v := range extra {
			r.Header[k] = v
		}
		resp, e := server.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if e != nil {
			t.Fatal(e)
		}
		after, e := chainPlatformSnapshot(seed)
		if e != nil {
			t.Fatal("after snapshot failed")
		}
		unchanged := reflect.DeepEqual(before, after)
		observations = append(observations, isolationObservation{row.ID, scenario, resp.StatusCode, unchanged})
		if resp.StatusCode != status {
			t.Fatalf("%s expected=%d observed=%d", scenario, status, resp.StatusCode)
		}
		if !unchanged {
			t.Fatal("request changed business snapshot")
		}
		if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
			t.Fatal("response lacks no-store")
		}
		if status >= 400 {
			for _, secret := range []string{wrongInvite.Token, actorCookie.Value, ownerCookie.Value, foreign.Tenant.ID, foreign.Workspace.ID, "Matrix foreign private tenant", "Matrix unjoined private workspace", "matrix_fixture_"} {
				if strings.Contains(string(raw), secret) {
					t.Fatal("denial exposed protected fixture details")
				}
			}
			if strings.Contains(string(raw), "csrf_invalid") || strings.Contains(string(raw), "origin_denied") {
				t.Fatal("scope test stopped at CSRF instead of authorization")
			}
		}
		return raw
	}
	for _, row := range routes {
		row := row
		t.Run(row.ID, func(t *testing.T) {
			t.Run("anonymous", func(t *testing.T) { call(t, row, "anonymous", anonymous, org.Workspace.ID, row.Body, 401, nil) })
			t.Run("forged_session", func(t *testing.T) { call(t, row, "forged_session", forged, org.Workspace.ID, row.Body, 401, nil) })
			if row.Kind == "internal" {
				return
			}
			t.Run("expired_session", func(t *testing.T) { call(t, row, "expired_session", expired, org.Workspace.ID, row.Body, 401, nil) })
			switch row.Kind {
			case "farm_context", "farm_list", "farm_detail", "farm_operation", "farm_write":
				t.Run("foreign_tenant", func(t *testing.T) { call(t, row, "foreign_tenant", viewer, foreign.Workspace.ID, row.Body, 404, nil) })
				t.Run("unjoined_same_tenant_workspace", func(t *testing.T) { call(t, row, "unjoined_same_tenant_workspace", viewer, second, row.Body, 404, nil) })
				status := map[string]int{"farm_context": 503, "farm_list": 503, "farm_detail": 404, "farm_operation": 404, "farm_write": 403}[row.Kind]
				// Real farm facts/current callbacks are exercised by the independent
				// double-PG chain. Never label missing-provider 503 as role denial.
				t.Run("current_scope_boundary", func(t *testing.T) {
					call(t, row, "current_scope_boundary", viewer, org.Workspace.ID, row.Body, status, nil)
				})
			case "scope":
				t.Run("foreign_tenant", func(t *testing.T) { call(t, row, "foreign_tenant", viewer, foreign.Workspace.ID, row.Body, 404, nil) })
				t.Run("unjoined_same_tenant_workspace", func(t *testing.T) { call(t, row, "unjoined_same_tenant_workspace", viewer, second, row.Body, 404, nil) })
				t.Run("viewer_role", func(t *testing.T) { call(t, row, "viewer_role", viewer, org.Workspace.ID, row.Body, 403, nil) })
			case "closed_provider":
				t.Run("production_provider_closed", func(t *testing.T) {
					call(t, row, "production_provider_closed", viewer, org.Workspace.ID, row.Body, 503, nil)
				})
			case "organization":
				t.Run("forged_body_identity", func(t *testing.T) {
					call(t, row, "forged_body_identity", viewer, org.Workspace.ID, `{"name":"not created","tenant_id":"foreign","user_id":"owner"}`, 400, nil)
				})
			case "accept":
				t.Run("wrong_recipient", func(t *testing.T) { call(t, row, "wrong_recipient", viewer, org.Workspace.ID, row.Body, 400, nil) })
				t.Run("accepted_token_replay_by_other_user", func(t *testing.T) {
					call(t, row, "accepted_token_replay_by_other_user", admin, org.Workspace.ID, fmt.Sprintf(`{"token":%q}`, invite.Token), 400, nil)
				})
			case "list":
				t.Run("scope_filter", func(t *testing.T) {
					raw := call(t, row, "scope_filter", viewer, org.Workspace.ID, "", 200, nil)
					if !strings.Contains(string(raw), org.Workspace.ID) || strings.Contains(string(raw), foreign.Workspace.ID) || strings.Contains(string(raw), second) {
						t.Fatal("workspace visibility filter incorrect")
					}
				})
			}
		})
	}
	// Positive authorization controls: existing valid viewer can read an explicitly
	// granted parcel, but the same actual session cannot update it in matrix rows.
	for _, row := range routes {
		if row.ID == "P02" || row.ID == "P13" {
			t.Run(row.ID+"_positive_read", func(t *testing.T) {
				body := strings.Replace(row.Body, "agri.parcel.update", "agri.parcel.read", 1)
				call(t, row, "explicit_read_control", viewer, org.Workspace.ID, body, 200, nil)
			})
		}
	}

	// Positive management reads prove role-denial rows are not rejected because
	// of missing route/data or invalid payload. Organization replay uses its real
	// idempotency key and does not impose a fictional viewer-create prohibition.
	for _, row := range routes {
		if row.ID == "P03" || row.ID == "P08" || row.ID == "P14" {
			row := row
			t.Run(row.ID+"_admin_control", func(t *testing.T) { call(t, row, "admin_control", admin, org.Workspace.ID, row.Body, 200, nil) })
		}
	}
	t.Run("P06_idempotent_owner_control", func(t *testing.T) {
		call(t, routes[5], "idempotent_owner_control", admin, org.Workspace.ID, `{"name":"Matrix main private tenant"}`, 200, http.Header{"X-Idempotency-Key": []string{"matrix-main-fixture-001"}})
	})
	// The service callback tests use signed service assertions, never the browser
	// cookie as an internal credential. Prepare a real authorized analyst snapshot.
	analyst := []string{"analyst"}
	if e = s.ChangeMember(ctx, owner, org.Workspace.ID, actor, &analyst, nil); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GrantResourceBinding(ctx, owner, org.Workspace.ID, actor, ResourceRef{"farm", "matrix_farm"}, []string{"agri.farm.read", "agri.parcel.read", "agri.analysis.submit"}, nil, revisionPointer(binding.AuthVersion)); e != nil {
		t.Fatal(e)
	}
	snapshot, decision, e := s.NewJobAuthority(ctx, actor, org.Workspace.ID, "matrix_operation", []ResourceRef{{"parcel", "matrix_parcel"}}, []byte(`{"fixture":true}`))
	if e != nil {
		t.Fatal(e)
	}
	digest, e := snapshot.Digest()
	if e != nil {
		t.Fatal(e)
	}
	signed := func(t *testing.T, row isolationRoute, scenario, capability string, body any, scope OwnershipContext, keys *ServiceKeyring, status int) {
		t.Helper()
		raw, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		token, canonical, e := issueServiceAssertion(keys, capability, scope, raw, time.Now().UTC())
		if e != nil {
			t.Fatal(e)
		}
		call(t, row, scenario, anonymous, org.Workspace.ID, string(canonical), status, http.Header{"Authorization": []string{"Bearer " + token}})
	}
	scope := OwnershipContext{TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID}
	wrongKeys, _ := identityTestKeys(t, time.Now().UTC(), 0x72)
	authorityBody := authorityCheckBody{ActorUserID: actor, TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, AuthorizationVersion: decision.AuthorizationVersion}
	jobBody := jobAuthorityCheckBody{Snapshot: snapshot, SnapshotHash: digest, Phase: "claim"}
	farmBody := farmAuthorityRequest{ActorUserID: actor, TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, Action: "farm.context", AuthorizationVersion: decision.AuthorizationVersion, OperationID: "req_matrix", RequestHash: zeroFarmHash, GrantPlanHash: zeroFarmHash, FarmIDs: []string{}, RequestPayloadHash: zeroFarmHash}
	for _, row := range routes {
		if row.Kind != "internal" {
			continue
		}
		row := row
		capability := "authority.check"
		var body any = authorityBody
		if row.ID == "I02" {
			capability = "job.authority.check"
			body = jobBody
		}
		if row.ID == "I03" {
			capability, body = "farm.operation.check", farmBody
		}
		t.Run(row.ID+"_signed_control", func(t *testing.T) { signed(t, row, "signed_control", capability, body, scope, gisKeys, 200) })
		t.Run(row.ID+"_wrong_signer", func(t *testing.T) { signed(t, row, "wrong_signer", capability, body, scope, wrongKeys, 401) })
		t.Run(row.ID+"_signed_scope_mismatch", func(t *testing.T) {
			signed(t, row, "signed_scope_mismatch", capability, body, OwnershipContext{TenantID: foreign.Tenant.ID, WorkspaceID: foreign.Workspace.ID}, gisKeys, 400)
		})
		t.Run(row.ID+"_wrong_direction", func(t *testing.T) { signed(t, row, "wrong_direction", "ownership.resolve", body, scope, gisKeys, 401) })
	}
	t.Run("I01_stale_epoch", func(t *testing.T) {
		stale := authorityBody
		stale.AuthorizationVersion++
		signed(t, routes[19], "stale_epoch", "authority.check", stale, scope, gisKeys, 403)
	})
	t.Run("I01_unjoined_workspace", func(t *testing.T) {
		b := authorityBody
		b.WorkspaceID = second
		signed(t, routes[19], "unjoined_workspace", "authority.check", b, OwnershipContext{TenantID: org.Tenant.ID, WorkspaceID: second}, gisKeys, 403)
	})
	t.Run("I02_substituted_snapshot", func(t *testing.T) {
		b := jobBody
		b.Snapshot.ActorUserID = owner
		signed(t, routes[20], "substituted_snapshot", "job.authority.check", b, scope, gisKeys, 400)
	})

	// Membership withdrawal is committed before each denial snapshot, not hidden
	// inside a rollback transaction. Login session stays unchanged.
	suspended := "suspended"
	if e = s.ChangeMember(ctx, owner, org.Workspace.ID, actor, nil, &suspended); e != nil {
		t.Fatal(e)
	}
	for _, row := range routes {
		if row.Kind == "scope" || strings.HasPrefix(row.Kind, "farm_") {
			row := row
			t.Run(row.ID+"_revoked_member", func(t *testing.T) {
				call(t, row, "revoked_member_same_session", viewer, org.Workspace.ID, row.Body, 404, nil)
			})
		}
	}

	t.Run("I01_revoked_actor", func(t *testing.T) {
		signed(t, routes[19], "revoked_actor", "authority.check", authorityBody, scope, gisKeys, 403)
	})
	t.Run("I02_revoked_actor", func(t *testing.T) {
		signed(t, routes[20], "revoked_actor", "job.authority.check", jobBody, scope, gisKeys, 403)
	})
	t.Run("I03_revoked_actor", func(t *testing.T) {
		signed(t, routes[29], "revoked_actor", "farm.operation.check", farmBody, scope, gisKeys, 403)
	})

	if e = svc.Logout(actorCookie.Value); e != nil {
		t.Fatal(e)
	}
	for _, row := range routes {
		if row.Kind != "internal" {
			row := row
			t.Run(row.ID+"_logged_out", func(t *testing.T) {
				call(t, row, "logged_out_same_cookie", viewer, org.Workspace.ID, row.Body, 401, nil)
			})
		}
	}
}
