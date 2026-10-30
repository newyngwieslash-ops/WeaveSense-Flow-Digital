package flow

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
)

// Real restricted app role: no session actor or tenant is supplied by request
// parameters. X-Test-Actor below selects only this test's already verified actors.
func TestFlowPostgresCallbackAndLegacyAdmission(t *testing.T) {
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_PG_MIGRATION_DSN") == "" || os.Getenv("AGRI_TEST_PG_RUNTIME_DSN") == "" {
		t.Skip("requires owned isolated platform PostgreSQL roles")
	}
	open := func(key string) *gorm.DB {
		db, err := database.Open(database.Config{Driver: "postgres", DSN: os.Getenv(key)})
		if err != nil {
			t.Fatal("fixture connection unavailable")
		}
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { pool.Close() })
		return db
	}
	owner, app := open("AGRI_TEST_PG_MIGRATION_DSN"), open("AGRI_TEST_PG_RUNTIME_DSN")
	if err := database.MigrateSchema(owner); err != nil {
		t.Fatal(err)
	}
	suffix := fmt.Sprintf("ad%d", time.Now().UnixNano())
	alice, bob := "a_"+suffix, "b_"+suffix
	now := time.Now().UTC()
	actors := map[string]*model.User{}
	for _, id := range []string{alice, bob} {
		u := &model.User{ID: id, Username: id, Status: "active", Email: id + "@fixture.invalid", EmailVerifiedAt: &now}
		if err := owner.Create(u).Error; err != nil {
			t.Fatal(err)
		}
		actors[id] = u
	}
	t.Run("first_personal_admission_and_disabled_no_resurrection", func(t *testing.T) {
		ids, err := UserTenantIDs(app, alice)
		if err != nil || len(ids) != 1 || ids[0] != model.PersonalTenantID(alice) {
			t.Fatalf("first-use admission failed: %v", err)
		}
		if err := UserTx(app, alice, func(tx *gorm.DB) error { _, err := ResolveScope(tx, alice, ""); return err }); err != nil {
			t.Fatal(err)
		}
		if err := owner.Model(&model.Tenant{}).Where("id=?", model.PersonalTenantID(alice)).Update("status", "disabled").Error; err != nil {
			t.Fatal(err)
		}
		if ids, err := UserTenantIDs(app, alice); err != nil || len(ids) != 0 {
			t.Fatalf("disabled personal was admitted: %v", err)
		}
		if err := UserTx(app, alice, func(*gorm.DB) error { return errors.New("must not run") }); !errors.Is(err, ErrForbidden) {
			t.Fatal("disabled user transaction admitted")
		}
	})
	who := func(c *gin.Context) (*model.User, error) { return actors[c.GetHeader("X-Test-Actor")], nil }
	cfg := Config{SigningKey: key, Issuer: "flow-platform"}
	router := gin.New()
	api := router.Group("/api", TenantMiddleware(app, who))
	RegisterP5Routes(api, app, cfg, who, nil)
	RegisterRoutes(api, app, cfg, who)
	reached := false
	api.POST("/internal/not-registered", func(c *gin.Context) { reached = true; c.Status(200) })
	request := func(actor, path, body, token string) (int, map[string]json.RawMessage) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("X-Test-Actor", actor)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		var out map[string]json.RawMessage
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	if code, _ := request("", "/api/internal/not-registered", `{}`, ""); code != 401 || reached {
		t.Fatal("unknown internal path bypassed admission")
	}
	// Organization creation must use its new server-generated tenant transaction,
	// not the authenticated user's existing transaction (Alice's personal is disabled).
	code, out := request(alice, "/api/intelligence/orgs", `{"name":"fixture","tenant_id":"forged"}`, "")
	if code != 200 {
		t.Fatalf("organization admission status %d", code)
	}
	var org struct {
		Tenant    model.Tenant    `json:"tenant"`
		Workspace model.Workspace `json:"workspace"`
	}
	if err := json.Unmarshal(out["data"], &org); err != nil {
		t.Fatal(err)
	}
	if org.Tenant.ID == "" || org.Tenant.ID == "forged" {
		t.Fatal("browser chose tenant")
	}
	scope := Scope{TenantID: org.Tenant.ID, WorkspaceID: org.Workspace.ID, Role: "admin"}
	sign := func(tn, ws string) string {
		tok, err := Sign(key, ExecutionContext{Issuer: "loom-engine", Audience: "flow-platform", TenantID: tn, WorkspaceID: ws, Actor: Actor{"service_principal", "loom-engine"}, PolicyVersion: "policy_v1"}, time.Now(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	body := fmt.Sprintf(`{"grant_actor":"user:%s","workspace_id":"%s","scope":"intel.collect","tenant_id":"forged"}`, alice, scope.WorkspaceID)
	if code, _ := request("", "/api/internal/flow/authorize", body, sign(scope.TenantID, scope.WorkspaceID)); code != 200 {
		t.Fatalf("valid signed callback failed %d", code)
	}
	if code, _ := request("", "/api/internal/flow/authorize", body, sign(model.PersonalTenantID(bob), scope.WorkspaceID)); code != 403 {
		t.Fatal("cross-tenant callback admitted")
	}
	if code, _ := request("", "/api/internal/flow/authorize", body, ""); code != 401 {
		t.Fatal("unsigned callback admitted")
	}
	if code, _ := request("", "/api/internal/flow/authorize", body, sign(scope.TenantID, "wrong_workspace")); code != 403 {
		t.Fatal("signed workspace restriction ignored")
	}
	// Actual ledger callback paths run with only the signed tenant, including
	// nested transaction/savepoint behavior and cross-tenant close rejection.
	fund(t, Billing{DB: owner}, alice, 10000)
	reserveBody := fmt.Sprintf(`{"grant_actor":"user:%s","workspace_id":"%s","key":"%s","amount_micro":1000,"ref_id":"fixture"}`, alice, scope.WorkspaceID, suffix)
	code, reserved := request("", "/api/internal/flow/budget/reserve", reserveBody, sign(scope.TenantID, scope.WorkspaceID))
	if code != 200 {
		t.Fatalf("real budget callback reserve status %d", code)
	}
	var reservation struct {
		ID string `json:"reservation_id"`
	}
	if err := json.Unmarshal(reserved["data"], &reservation); err != nil || reservation.ID == "" {
		t.Fatal("missing reservation reference")
	}
	closeBody := fmt.Sprintf(`{"reservation_id":%q,"outcome":"release"}`, reservation.ID)
	if code, _ := request("", "/api/internal/flow/budget/close", closeBody, sign(model.PersonalTenantID(bob), scope.WorkspaceID)); code != 404 {
		t.Fatal("cross-tenant budget close admitted")
	}
	if code, _ := request("", "/api/internal/flow/budget/close", closeBody, sign(scope.TenantID, scope.WorkspaceID)); code != 200 {
		t.Fatalf("real budget release failed %d", code)
	}
	event := Event{EventID: "event_" + suffix, OperationID: "op_" + suffix, JobID: "job_" + suffix, TenantID: scope.TenantID, WorkspaceID: scope.WorkspaceID, Sequence: 1, Type: "research.completed", OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{"research_run_id":"fixture","status":"succeeded"}`)}
	eventBytes, _ := json.Marshal(event)
	if code, _ := request("", "/api/internal/flow/events", string(eventBytes), sign(scope.TenantID, "wrong_workspace")); code != 403 {
		t.Fatal("event ignored signed workspace restriction")
	}

	if code, _ := request("", "/api/internal/flow/events", string(eventBytes), sign(scope.TenantID, scope.WorkspaceID)); code != 200 {
		t.Fatalf("real event callback failed %d", code)
	}
	if code, _ := request("", "/api/internal/flow/events", string(eventBytes), sign(model.PersonalTenantID(bob), scope.WorkspaceID)); code != 403 {
		t.Fatal("cross-tenant event admitted")
	}
	// A legacy invitation authorizes only its verified recipient and product.
	makeInvite := func(product string) string {
		var inv model.FlowInvite
		var token string
		err := TenantTx(app, scope.TenantID, func(tx *gorm.DB) error {
			var err error
			inv, token, err = CreateInvite(tx, scope, alice, actors[bob].Email, "viewer", time.Hour)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if product != "" {
			if err := owner.Model(&inv).Update("product", product).Error; err != nil {
				t.Fatal(err)
			}
		}
		return token
	}
	token := makeInvite("")
	if code, _ := request(alice, "/api/intelligence/invites/accept", fmt.Sprintf(`{"token":%q}`, token), ""); code != 400 {
		t.Fatal("wrong recipient accepted invitation")
	}
	if code, _ := request(bob, "/api/intelligence/invites/accept", fmt.Sprintf(`{"token":%q,"tenant_id":"forged"}`, token), ""); code != 200 {
		t.Fatalf("verified invitation failed %d", code)
	}
	if code, _ := request(bob, "/api/intelligence/invites/accept", fmt.Sprintf(`{"token":%q}`, token), ""); code != 400 {
		t.Fatal("invitation replay accepted")
	}
	agri := makeInvite("agriculture")
	if _, err := AcceptInvite(app, bob, agri); !errors.Is(err, ErrInviteInvalid) {
		t.Fatal("agriculture invitation consumed by legacy admission")
	}
	// Returning a connection to the pool must not retain callback/accept scope.
	var count int64
	if err := app.Model(&model.FlowEventInbox{}).Where("tenant_id=?", scope.TenantID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("callback/accept leaked scope into pooled app handle")
	}
}
