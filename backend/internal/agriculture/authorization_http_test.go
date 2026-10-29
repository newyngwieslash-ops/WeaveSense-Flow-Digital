package agriculture

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"yingce/backend/internal/kernel"
	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
)

// The authenticated caller is fixed by test setup, never read from request
// headers/body. This exercises real Gin handlers and SQLite service writes;
// ownership receipts remain explicit fixtures, not a live GIS acceptance test.
func authorizationHTTP(t *testing.T, s Service, trustedActor, method, path, body, origin string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	API{Service: s, User: func(_ *gin.Context) (*model.User, error) {
		if trustedActor == "" {
			return nil, kernel.Unauthorized("fixture anonymous")
		}
		return &model.User{ID: trustedActor}, nil
	}}.Register(router.Group("/api"))
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// These untrusted claims must never override the fixed platform session.
	req.Header.Set("X-User-ID", "owner")
	req.Header.Set("X-Tenant-ID", "forged_tenant")
	req.Header.Set("X-Operator-Code", "owner")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Vary") != "Cookie" {
		t.Fatalf("authorization response cacheable: %v", w.Header())
	}
	return w
}

func assertAuthorizationHTTP(t *testing.T, w *httptest.ResponseRecorder, status int, reason string) json.RawMessage {
	t.Helper()
	var envelope struct {
		Code   int             `json:"code"`
		Reason string          `json:"reason"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if w.Code != status || envelope.Reason != reason {
		t.Fatalf("response %d %s, want %d %s", w.Code, w.Body.String(), status, reason)
	}
	if status >= 400 {
		if envelope.Code != status || string(envelope.Data) != "null" {
			t.Fatalf("failure envelope: %s", w.Body.String())
		}
	} else if envelope.Code != 0 {
		t.Fatalf("success business error: %s", w.Body.String())
	}
	return envelope.Data
}

func TestAuthorizationHTTPTrustedSessionAndResourceNegatives(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	join(t, s, org, "bob", "viewer")
	if _, err := s.GrantResourceBinding(context.Background(), "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.farm.read", "agri.parcel.read", "agri.asset.read"}, nil); err != nil {
		t.Fatal(err)
	}
	path := "/api/agriculture/workspaces/" + org.Workspace.ID + "/authorize"
	for _, tc := range []struct {
		name, actor, action, kind, id string
		status                        int
		reason                        string
	}{
		{"anonymous", "", "agri.farm.read", "farm", "farm_a", 401, "unauthenticated"},
		{"allowed", "bob", "agri.farm.read", "farm", "farm_a", 200, ""},
		{"same_tenant_other_farm", "bob", "agri.farm.read", "farm", "farm_b", 404, "resource_unavailable"},
		{"viewer_mutation", "bob", "agri.parcel.update", "parcel", "parcel_a", 403, "forbidden"},
		{"independent_download", "bob", "agri.asset_download.read", "asset", "asset_a", 403, "forbidden"},
		{"composite_requires_all_farms", "bob", "agri.asset.read", "asset", "asset_multi", 404, "resource_unavailable"},
		{"unknown_action", "bob", "agri.farm.destroy", "farm", "farm_a", 400, "invalid_request"},
		{"nonmember", "carol", "agri.farm.read", "farm", "farm_a", 404, "resource_unavailable"},
		{"owner_no_implicit_content", "owner", "agri.farm.read", "farm", "farm_a", 404, "resource_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"action": tc.action, "resources": []ResourceRef{{tc.kind, tc.id}}})
			data := assertAuthorizationHTTP(t, authorizationHTTP(t, s, tc.actor, "POST", path, string(body), ""), tc.status, tc.reason)
			if tc.status == 200 {
				var decision AuthorizationDecision
				if err := json.Unmarshal(data, &decision); err != nil {
					t.Fatal(err)
				}
				if decision.ActorID != "bob" || decision.TenantID != org.Tenant.ID || decision.WorkspaceID != org.Workspace.ID || len(decision.FarmIDs) != 1 || decision.FarmIDs[0] != "farm_a" {
					t.Fatalf("forged headers altered decision: %+v", decision)
				}
			}
		})
	}
	s.Ownership = nil
	w := authorizationHTTP(t, s, "bob", "POST", path, `{"action":"agri.farm.read","resources":[{"kind":"farm","id":"farm_a"}]}`, "")
	assertAuthorizationHTTP(t, w, 503, "dependency_unavailable")
	for _, secret := range []string{"SQL", "SELECT", "gorm", "resolver", "panic", "stack", "Bearer"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("internal error leaked: %s", w.Body.String())
		}
	}
}

func TestAuthorizationHTTPStrictClaimsAndWritesHaveNoSideEffects(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	join(t, s, org, "bob", "viewer")
	ctx := context.Background()
	row, err := s.GrantResourceBinding(ctx, "owner", org.Workspace.ID, "bob", ResourceRef{"farm", "farm_a"}, []string{"agri.farm.read"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/agriculture/workspaces/" + org.Workspace.ID
	beforeEpoch := bindingEpoch(t, s, org.Tenant.ID)
	var beforeAudit int64
	if err = s.DB.Model(&model.AgricultureAuditEvent{}).Count(&beforeAudit).Error; err != nil {
		t.Fatal(err)
	}
	grant := `{"subjectId":"bob","resource":{"kind":"farm","id":"farm_b"},"capabilities":["agri.farm.read"]}`
	for _, field := range []string{"tenant_id", "tenantId", "user_id", "userId", "operator_code", "actorId", "roles"} {
		t.Run(field, func(t *testing.T) {
			for _, endpoint := range []string{"authorize", "resource-bindings"} {
				body := map[string]any{field: "owner"}
				if endpoint == "authorize" {
					body["action"] = "agri.farm.read"
					body["resources"] = []ResourceRef{{"farm", "farm_a"}}
				} else {
					body["subjectId"] = "bob"
					body["resource"] = ResourceRef{"farm", "farm_b"}
					body["capabilities"] = []string{"agri.farm.read"}
				}
				raw, _ := json.Marshal(body)
				assertAuthorizationHTTP(t, authorizationHTTP(t, s, "owner", "POST", base+"/"+endpoint, string(raw), ""), 400, "invalid_request")
			}
		})
	}
	assertAuthorizationHTTP(t, authorizationHTTP(t, s, "bob", "POST", base+"/resource-bindings", grant, ""), 403, "forbidden")
	assertAuthorizationHTTP(t, authorizationHTTP(t, s, "owner", "POST", base+"/resource-bindings", grant, "https://attacker.invalid"), 403, "forbidden")
	assertAuthorizationHTTP(t, authorizationHTTP(t, s, "owner", "POST", base+"/resource-bindings/"+row.ID+"/revoke", "{}", "https://attacker.invalid"), 403, "forbidden")
	assertAuthorizationHTTP(t, authorizationHTTP(t, s, "bob", "GET", base+"/resource-bindings", "", ""), 403, "forbidden")
	assertAuthorizationHTTP(t, authorizationHTTP(t, s, "", "GET", base+"/resource-bindings", "", ""), 401, "unauthenticated")
	assertAuthorizationHTTP(t, authorizationHTTP(t, s, "owner", "POST", base+"/resource-bindings/arb_foreign/revoke", "{}", ""), 404, "resource_unavailable")
	var rows []model.AgricultureResourceBinding
	if err = s.DB.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	var afterAudit int64
	if err = s.DB.Model(&model.AgricultureAuditEvent{}).Count(&afterAudit).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != row.ID || rows[0].State != "active" || rows[0].Capabilities != row.Capabilities || beforeEpoch != bindingEpoch(t, s, org.Tenant.ID) || beforeAudit != afterAudit {
		t.Fatal("rejected HTTP requests changed ACL/epoch/audit")
	}
}

func TestAuthorizationHTTPBindingCRUDAndForeignBindingID(t *testing.T) {
	s, org, _ := authorizationFixture(t)
	join(t, s, org, "bob", "viewer")
	base := "/api/agriculture/workspaces/" + org.Workspace.ID + "/resource-bindings"
	data := assertAuthorizationHTTP(t, authorizationHTTP(t, s, "owner", "POST", base, `{"subjectId":"bob","resource":{"kind":"farm","id":"farm_a"},"capabilities":["agri.farm.read"]}`, ""), 200, "")
	var row model.AgricultureResourceBinding
	if err := json.Unmarshal(data, &row); err != nil || row.ID == "" {
		t.Fatalf("grant response: %s %v", data, err)
	}
	data = assertAuthorizationHTTP(t, authorizationHTTP(t, s, "owner", "GET", base, "", ""), 200, "")
	var listed []model.AgricultureResourceBinding
	if err := json.Unmarshal(data, &listed); err != nil || len(listed) != 1 || listed[0].ID != row.ID {
		t.Fatalf("list response: %s %v", data, err)
	}
	other, err := s.CreateOrganization(context.Background(), "owner", "Other HTTP fixture", "http-other-org-00001")
	if err != nil {
		t.Fatal(err)
	}
	assertAuthorizationHTTP(t, authorizationHTTP(t, s, "owner", "POST", "/api/agriculture/workspaces/"+other.Workspace.ID+"/resource-bindings/"+row.ID+"/revoke", "{}", ""), 404, "resource_unavailable")
	assertAuthorizationHTTP(t, authorizationHTTP(t, s, "owner", "POST", base+"/"+row.ID+"/revoke", "{}", ""), 200, "")
	var stored model.AgricultureResourceBinding
	if err = s.DB.First(&stored, "id=?", row.ID).Error; err != nil || stored.State != "revoked" {
		t.Fatalf("revoke not persisted: %+v %v", stored, err)
	}
}
