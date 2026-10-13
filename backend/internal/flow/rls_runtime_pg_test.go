package flow

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
)

// pgTestDB creates a fresh database as the NON-superuser role from
// FLOW_TEST_PG_RLS_DSN (needs CREATEDB) and migrates it.
func pgTestDB(t *testing.T) *gorm.DB {
	dsn := strings.TrimSpace(os.Getenv("FLOW_TEST_PG_RLS_DSN"))
	if dsn == "" {
		t.Skip("FLOW_TEST_PG_RLS_DSN is not configured")
	}
	base, err := database.Open(database.Config{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("flow_rt_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE DATABASE " + name).Error; err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	u.Path = "/" + name
	db, err := database.Open(database.Config{Driver: "postgres", DSN: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s, e := db.DB(); e == nil {
			s.Close()
		}
		base.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
	})
	if err := database.MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestRuntimeRLSBlocksUnfilteredHandlerQueries proves the policies are enforced
// on live requests: a handler that "forgets" its tenant filter still only sees
// and changes the caller's tenants' rows.
func TestRuntimeRLSBlocksUnfilteredHandlerQueries(t *testing.T) {
	db := pgTestDB(t)
	for _, id := range []string{"u_alice", "u_bob"} {
		if err := db.Create(&model.User{ID: id, Username: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	scopes := map[string]Scope{}
	for _, u := range []string{"u_alice", "u_bob"} {
		s, err := ResolveScope(db, u, "")
		if err != nil {
			t.Fatal(err)
		}
		scopes[u] = s
		db.Create(&model.FlowNotification{ID: "n_" + u, TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, EventID: "ev_" + u, Type: "x", Title: "t", CreatedAt: time.Now()})
		conv := model.FlowAgentConversation{ID: "cv_" + u, TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, UserID: u, SubjectKind: SubjectWorkspace, SubjectID: s.WorkspaceID, EvidenceScope: "{}", ConfirmedPlans: "[]"}
		db.Create(&conv)
	}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	user := func(c *gin.Context) (*model.User, error) {
		id := c.GetHeader("X-Test-User")
		if id == "" {
			return nil, fmt.Errorf("no user")
		}
		return &model.User{ID: id}, nil
	}
	api := r.Group("/api", TenantMiddleware(db, user))
	RegisterP5Routes(api, db, Config{}, user, nil)
	RegisterP7Routes(api, db, user, &MCPGateway{DB: db})
	RegisterAgentRoutes(api, db, nil, user)
	// Deliberately buggy handler: no tenant filter at all.
	api.GET("/probe", func(c *gin.Context) {
		db := reqDB(c, db)
		var notes, convs int64
		db.Model(&model.FlowNotification{}).Count(&notes)
		db.Model(&model.FlowAgentConversation{}).Count(&convs)
		upd := db.Model(&model.FlowNotification{}).Where("1 = 1").Update("title", "touched by "+c.GetHeader("X-Test-User"))
		c.JSON(200, gin.H{"notes": notes, "convs": convs, "updated": upd.RowsAffected})
	})
	call := func(u, method, path string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("X-Test-User", u)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	_, p := call("u_alice", "GET", "/api/probe")
	if p["notes"].(float64) != 1 || p["convs"].(float64) != 1 || p["updated"].(float64) != 1 {
		t.Fatalf("unfiltered handler saw other tenants: %v", p)
	}
	var bobNote model.FlowNotification
	db.First(&bobNote, "id = ?", "n_u_bob")
	if bobNote.Title != "t" {
		t.Fatalf("alice's unfiltered update reached bob's row: %q", bobNote.Title)
	}
	// Real handlers keep working under RLS.
	if code, body := call("u_alice", "GET", "/api/agent/conversations"); code != 200 || len(body["data"].([]any)) != 1 {
		t.Fatalf("conversations %d %v", code, body)
	}
	if code, _ := call("u_alice", "GET", "/api/agent/conversations/cv_u_bob/messages"); code != 404 {
		t.Fatalf("cross-tenant conversation read: %d", code)
	}
	if code, _ := call("u_bob", "POST", "/api/intelligence/notifications/all/read"); code != 200 {
		t.Fatal("mark read failed")
	}
	if code, body := call("u_alice", "GET", "/api/intelligence/members"); code != 200 || body["data"] == nil {
		t.Fatalf("members %d %v", code, body)
	}
	// System context (no tenant set) still sees everything.
	var all int64
	db.Model(&model.FlowNotification{}).Count(&all)
	if all != 2 {
		t.Fatalf("system context sees %d", all)
	}
}
