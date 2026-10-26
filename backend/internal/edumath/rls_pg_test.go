package edumath

import (
	"fmt"
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

// pgTestDB: fresh database as the NON-superuser role from FLOW_TEST_PG_RLS_DSN.
func pgTestDB(t *testing.T) *gorm.DB {
	dsn := strings.TrimSpace(os.Getenv("FLOW_TEST_PG_RLS_DSN"))
	if dsn == "" {
		t.Skip("FLOW_TEST_PG_RLS_DSN is not configured")
	}
	base, err := database.Open(database.Config{Driver: "postgres", DSN: dsn})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("edu_rt_%d", time.Now().UnixNano())
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
	for _, id := range []string{"u_alice", "u_bob", "u_carol"} {
		if err := db.Create(&model.User{ID: id, Username: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// TestPGEduTablesForceRLS: every edu_* table has FORCE RLS, and a handler that
// forgets its tenant filter only sees rows of the caller's tenants — including a
// student, whose scope is limited to the institutions where they hold a role.
func TestPGEduTablesForceRLS(t *testing.T) {
	db := pgTestDB(t)
	var missing []string
	for _, m := range model.EduModels() {
		st := &gorm.Statement{DB: db}
		_ = st.Parse(m)
		var forced bool
		db.Raw("SELECT relforcerowsecurity AND relrowsecurity FROM pg_class WHERE relname = ?", st.Schema.Table).Scan(&forced)
		if !forced {
			missing = append(missing, st.Schema.Table)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("edu tables without FORCE RLS: %v", missing)
	}
	now := time.Now().UTC()
	alice, bob := model.PersonalTenantID("u_alice"), model.PersonalTenantID("u_bob")
	for _, u := range []string{"u_alice", "u_bob"} {
		if err := db.Transaction(func(tx *gorm.DB) error { return model.EnsurePersonalTenantTx(tx, u) }); err != nil {
			t.Fatal(err)
		}
	}
	db.Create(&model.EduProblem{ID: "epb_a", TenantID: alice, WorkspaceID: model.PersonalWorkspaceID("u_alice"), FamilyID: "SOLID-ANGLE-02", LatestRevision: 1, CreatedAt: now, UpdatedAt: now})
	db.Create(&model.EduProblem{ID: "epb_b", TenantID: bob, WorkspaceID: model.PersonalWorkspaceID("u_bob"), FamilyID: "SOLID-ANGLE-02", LatestRevision: 1, CreatedAt: now, UpdatedAt: now})
	db.Create(&model.EduRoleBinding{ID: "erb_c", TenantID: alice, UserID: "u_carol", Role: "student", Status: "active", CreatedAt: now})

	gin.SetMode(gin.TestMode)
	h := newHarnessDB(t, "", db)
	h.r.GET("/probe", Middleware(db, func(c *gin.Context) (*model.User, error) { return &model.User{ID: c.GetHeader("X-Test-User")}, nil }), func(c *gin.Context) {
		var ids []string
		reqDB(c, db).Model(&model.EduProblem{}).Order("id").Pluck("id", &ids) // deliberately unfiltered
		upd := reqDB(c, db).Model(&model.EduProblem{}).Where("1 = 1").Update("title", "x").RowsAffected
		c.JSON(200, gin.H{"ids": ids, "updated": upd})
	})
	for user, want := range map[string]string{"u_alice": "[epb_a]", "u_bob": "[epb_b]", "u_carol": "[epb_a]"} {
		_, out, raw, _ := h.call(user, "GET", "/probe", nil)
		if got := fmt.Sprint(out["ids"]); got != want || out["updated"].(float64) != 1 {
			t.Fatalf("%s saw %s (%s)", user, got, raw)
		}
	}
	// Carol is not a workspace member: staff endpoints refuse her.
	if code, _, _, _ := h.call("u_carol", "POST", "/api/edu/problems", map[string]any{"familyId": "SOLID-ANGLE-02", "structured": problem("2", "1")}); code != 403 {
		t.Fatalf("student creating problem: %d", code)
	}
}

// TestPGG1Chain runs the full G1 chain on Postgres as the non-superuser role.
func TestPGG1Chain(t *testing.T) {
	db := pgTestDB(t)
	runG1(t, newHarnessDB(t, startMathService(t), db))
}

// TestPGStudentLearningLoop runs the student learning loop on Postgres under FORCE RLS
// (savepoints around unique-index inserts must not abort the request transaction).
func TestPGStudentLearningLoop(t *testing.T) {
	db := pgTestDB(t)
	runLearning(t, newHarnessDB(t, startMathService(t), db))
}

// TestPGHelpRequestsAndDashboard runs help requests and the dashboard on Postgres under FORCE RLS.
func TestPGHelpRequestsAndDashboard(t *testing.T) {
	db := pgTestDB(t)
	runHelp(t, newHarnessDB(t, startMathService(t), db))
}

// TestPGFigurePredictionsAndUse runs prediction and figure-use recording on Postgres under FORCE RLS.
func TestPGFigurePredictionsAndUse(t *testing.T) {
	db := pgTestDB(t)
	runFigure(t, newHarnessDB(t, startMathService(t), db))
}
