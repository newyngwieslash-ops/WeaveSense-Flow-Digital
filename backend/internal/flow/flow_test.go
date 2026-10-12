package flow

import (
	"errors"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/database"
	"yingce/backend/internal/model"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var key = []byte(strings.Repeat("k", 32))

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.User{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"u_alice", "u_bob"} {
		if err := db.Create(&model.User{ID: id, Username: id}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrationMapsExistingUsersToPersonalTenants(t *testing.T) {
	db := testDB(t)
	var n int64
	db.Model(&model.Tenant{}).Where("kind = 'personal'").Count(&n)
	if n != 2 {
		t.Fatalf("want 2 personal tenants, got %d", n)
	}
	// idempotent re-run
	if err := database.MigrateSchema(db); err != nil {
		t.Fatal(err)
	}
}

func TestScopeIsolationAndGuessedWorkspaceRejected(t *testing.T) {
	db := testDB(t)
	a, err := ResolveScope(db, "u_alice", "")
	if err != nil || a.TenantID != model.PersonalTenantID("u_alice") || a.Role != "admin" {
		t.Fatalf("alice scope %+v %v", a, err)
	}
	if _, err := ResolveScope(db, "u_bob", a.WorkspaceID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob must not access alice workspace, got %v", err)
	}
	// new user gets lazily-created tenant
	db.Create(&model.User{ID: "u_carol", Username: "u_carol"})
	if c, err := ResolveScope(db, "u_carol", ""); err != nil || c.TenantID == a.TenantID {
		t.Fatalf("carol %+v %v", c, err)
	}
	// revoked tenant membership => no access
	db.Model(&model.Membership{}).Where("user_id = ?", "u_alice").Update("status", "revoked")
	if _, err := ResolveScope(db, "u_alice", a.WorkspaceID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("revoked member must be rejected, got %v", err)
	}
}

func TestTokenSignVerify(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	c := ExecutionContext{Issuer: "flow-platform", Audience: "loom-engine", TenantID: "t1", WorkspaceID: "w1",
		Actor: Actor{"user", "u"}, Scopes: []string{"intel.collect"}, PolicyVersion: "policy_v1"}
	tok, err := Sign(key, c, now, time.Hour) // clamped to 5m
	if err != nil {
		t.Fatal(err)
	}
	got, err := Verify(key, tok, "loom-engine", now.Add(time.Minute))
	if err != nil || got.TenantID != "t1" || !got.HasScope("intel.collect") {
		t.Fatalf("verify %v %+v", err, got)
	}
	if _, err := Verify(key, tok, "loom-engine", now.Add(6*time.Minute)); !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("want expired, got %v", err)
	}
	if _, err := Verify(key, tok, "other", now); !errors.Is(err, ErrBadToken) {
		t.Fatal("audience must be checked")
	}
	if _, err := Verify([]byte(strings.Repeat("x", 32)), tok, "loom-engine", now); !errors.Is(err, ErrBadToken) {
		t.Fatal("signature must be checked")
	}
	parts := strings.Split(tok, ".")
	if _, err := Verify(key, parts[0]+"."+b64([]byte(`{"aud":"loom-engine","tenant_id":"t2","workspace_id":"w","exp":9999999999}`))+"."+parts[2], "loom-engine", now); err == nil {
		t.Fatal("tampered body accepted")
	}
}

func TestConsumeEventIdempotentAndMonotonic(t *testing.T) {
	db := testDB(t)
	ev := Event{EventID: "evt1", OperationID: "op1", JobID: "job1", TenantID: "t1", WorkspaceID: "w1", Sequence: 2, Type: "collection.completed", OccurredAt: time.Now()}
	if ok, err := ConsumeEvent(db, "c", ev); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, err := ConsumeEvent(db, "c", ev); ok || err != nil {
		t.Fatal("duplicate must be ignored", ok, err)
	}
	old := ev
	old.EventID, old.Sequence, old.Type = "evt0", 1, "collection.accepted"
	ConsumeEvent(db, "c", old)
	var p model.FlowOperationProjection
	db.First(&p, "operation_id = ?", "op1")
	if p.Status != "succeeded" || p.Sequence != 2 {
		t.Fatalf("stale event regressed projection: %+v", p)
	}
	foreign := ev
	foreign.EventID, foreign.TenantID, foreign.Sequence = "evt9", "t2", 3
	if _, err := ConsumeEvent(db, "c", foreign); err == nil {
		t.Fatal("cross-tenant event must fail")
	}
}
