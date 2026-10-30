package flow

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"yingce/backend/internal/model"
)

func TestFlowAdmissionMissingContextAndUnknownInternalFailClosed(t *testing.T) {
	db := testDB(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if err := reqDB(c, db).Find(&[]model.Tenant{}).Error; !errors.Is(err, ErrForbidden) {
		t.Fatalf("missing request context admitted: %v", err)
	}
	r := gin.New()
	api := r.Group("/api", TenantMiddleware(db, func(*gin.Context) (*model.User, error) { return nil, nil }))
	hit := false
	api.POST("/internal/unregistered", func(c *gin.Context) { hit = true; c.Status(200) })
	RegisterP5Routes(api, db, Config{SigningKey: key}, func(*gin.Context) (*model.User, error) { return nil, nil }, nil)
	for _, path := range []string{"/api/internal/unregistered", "/api/internal/flow/authorize"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"tenant_id":"tn_forged"}`))
		r.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("anonymous admission %s = %d", path, w.Code)
		}
	}
	if hit {
		t.Fatal("unknown internal route bypassed identity")
	}
}

func TestFlowLegacyInviteRequiresCurrentVerifiedRecipient(t *testing.T) {
	for _, mode := range []string{"unverified", "wrong_email", "disabled", "agriculture", "valid"} {
		t.Run(mode, func(t *testing.T) {
			db := testDB(t)
			tn, ws, err := CreateOrganization(db, "u_alice", "fixture")
			if err != nil {
				t.Fatal(err)
			}
			inv, tok, err := CreateInvite(db, Scope{TenantID: tn.ID, WorkspaceID: ws.ID, Role: "admin"}, "u_alice", "bob@x", "viewer", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unverified":
				db.Model(&model.User{}).Where("id=?", "u_bob").Update("email_verified_at", nil)
			case "wrong_email":
				db.Model(&model.User{}).Where("id=?", "u_bob").Update("email", "other@x")
			case "disabled":
				db.Model(&model.User{}).Where("id=?", "u_bob").Update("status", "disabled")
			case "agriculture":
				db.Model(&inv).Update("product", "agriculture")
			}
			got, err := AcceptInvite(db, "u_bob", tok)
			if mode == "valid" {
				if err != nil || got.TenantID != tn.ID || got.Role != "viewer" {
					t.Fatalf("valid acceptance failed: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInviteInvalid) {
				t.Fatalf("recipient boundary failed: %v", err)
			}
			var count int64
			db.Model(&model.WorkspaceMembership{}).Where("workspace_id=? AND user_id=?", ws.ID, "u_bob").Count(&count)
			if count != 0 {
				t.Fatal("invalid invitation created membership")
			}
		})
	}
}

func TestFlowCallbackSignedWorkspaceCannotBeReplaced(t *testing.T) {
	db := testDB(t)
	sc, err := ResolveScope(db, "u_alice", "")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{SigningKey: key, Issuer: "flow-platform"}
	user := func(*gin.Context) (*model.User, error) { return nil, nil }
	r := gin.New()
	api := r.Group("/api", TenantMiddleware(db, user))
	RegisterP5Routes(api, db, cfg, user, nil)
	RegisterRoutes(api, db, cfg, user)
	tok, err := Sign(key, ExecutionContext{Issuer: "loom-engine", Audience: "flow-platform", TenantID: sc.TenantID, WorkspaceID: "signed_other_workspace", Actor: Actor{"service_principal", "loom-engine"}, PolicyVersion: "policy_v1"}, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"authorize":      `{"grant_actor":"user:u_alice","workspace_id":"` + sc.WorkspaceID + `","scope":"intel.collect"}`,
		"budget/reserve": `{"grant_actor":"user:u_alice","workspace_id":"` + sc.WorkspaceID + `","key":"test","amount_micro":1}`,
		"summarize":      `{"grant_actor":"user:u_alice","workspace_id":"` + sc.WorkspaceID + `","key":"test"}`,
		"events":         `{"event_id":"fixture","operation_id":"fixture","tenant_id":"` + sc.TenantID + `","workspace_id":"` + sc.WorkspaceID + `"}`,
	}
	for path, body := range cases {
		t.Run(path, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest("POST", "/api/internal/flow/"+path, strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+tok)
			r.ServeHTTP(w, req)
			if w.Code != 403 {
				t.Fatalf("signed workspace mismatch status %d", w.Code)
			}
		})
	}
}
