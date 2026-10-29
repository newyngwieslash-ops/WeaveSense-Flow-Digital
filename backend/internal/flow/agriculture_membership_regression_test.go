package flow

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// DT010 regression specifications against the existing real SQLite repository.
// These tests intentionally expose upstream defects; they are not PostgreSQL
// RLS/admission acceptance and do not assert an email-recipient policy yet.
func agricultureMembershipFixture(t *testing.T) (*gorm.DB, model.Tenant, model.Workspace, Scope) {
	t.Helper()
	db := testDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	tenant, workspace, err := CreateOrganization(db, "u_alice", "Agriculture regression")
	if err != nil {
		t.Fatal(err)
	}
	ownerScope, err := ResolveScope(db, "u_alice", workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	return db, tenant, workspace, ownerScope
}

func TestAgricultureMembershipOwnerRemovalPreservesWorkspace(t *testing.T) {
	db, tenant, workspace, owner := agricultureMembershipFixture(t)
	if err := RemoveMember(db, owner, "u_alice"); !errors.Is(err, ErrForbidden) {
		t.Errorf("owner removal must be refused, got %v", err)
	}
	var membership model.Membership
	if err := db.First(&membership, "tenant_id = ? AND user_id = ?", tenant.ID, "u_alice").Error; err != nil {
		t.Fatal(err)
	}
	if membership.Role != "owner" || membership.Status != "active" {
		t.Errorf("owner membership changed: role=%s status=%s", membership.Role, membership.Status)
	}
	if _, err := ResolveScope(db, "u_alice", workspace.ID); err != nil {
		t.Errorf("refused owner removal destroyed workspace access: %v", err)
	}
}

func TestAgricultureMembershipInactiveTenantExcludedFromWorkspaceList(t *testing.T) {
	db, tenant, workspace, _ := agricultureMembershipFixture(t)
	if err := db.Model(&model.Tenant{}).Where("id = ?", tenant.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveScope(db, "u_alice", workspace.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("fixture must actually deny the disabled tenant: %v", err)
	}
	workspaces, err := ListWorkspaces(db, "u_alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range workspaces {
		if row.TenantID == tenant.ID {
			t.Errorf("disabled tenant remains in workspace discovery: %s", row.ID)
		}
	}
}

func TestAgricultureMembershipInactiveTenantExcludedFromDatabaseScope(t *testing.T) {
	db, tenant, _, _ := agricultureMembershipFixture(t)
	if err := db.Model(&model.Tenant{}).Where("id = ?", tenant.ID).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	ids, err := UserTenantIDs(db, "u_alice")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == tenant.ID {
			t.Error("disabled tenant remains in ordinary request/worker database scope")
		}
	}
}

func TestAgricultureMembershipMissingWorkspaceCannotBeReportedAsEditor(t *testing.T) {
	db, tenant, workspace, owner := agricultureMembershipFixture(t)
	if err := db.Create(&model.Membership{ID: NewID("mb_"), TenantID: tenant.ID, UserID: "u_bob", Role: "member", Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveScope(db, "u_bob", workspace.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("fixture must lack workspace access: %v", err)
	}
	rows, err := ListMembers(db, owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.UserID == "u_bob" && row.Role == "editor" {
			t.Error("tenant-only member was presented as an editor without workspace membership")
		}
	}
}

func TestAgricultureMembershipInviteCannotReadmitInactiveMembership(t *testing.T) {
	db, tenant, workspace, owner := agricultureMembershipFixture(t)
	if err := db.Create(&model.Membership{ID: NewID("mb_"), TenantID: tenant.ID, UserID: "u_bob", Role: "member", Status: "removed"}).Error; err != nil {
		t.Fatal(err)
	}
	invite, token, err := CreateInvite(db, owner, "u_alice", "", "viewer", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if scope, err := AcceptInvite(db, "u_bob", token); err == nil {
		t.Errorf("inactive membership returned successful admission: %+v", scope)
	}
	var persisted model.FlowInvite
	if err := db.First(&persisted, "id = ?", invite.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.AcceptedAt != nil || persisted.AcceptedBy != "" {
		t.Error("rejected inactive membership consumed the invite")
	}
	var count int64
	if err := db.Model(&model.WorkspaceMembership{}).Where("workspace_id = ? AND user_id = ?", workspace.ID, "u_bob").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Error("inactive member gained a workspace membership through invitation")
	}
	var member model.Membership
	if err := db.First(&member, "tenant_id = ? AND user_id = ?", tenant.ID, "u_bob").Error; err != nil {
		t.Fatal(err)
	}
	if member.Status != "removed" {
		t.Error("ordinary invitation silently reactivated a removed member")
	}
}

func TestAgricultureMembershipInviteReturnsOnlyPersistedRole(t *testing.T) {
	db, tenant, workspace, owner := agricultureMembershipFixture(t)
	for _, row := range []any{
		&model.Membership{ID: NewID("mb_"), TenantID: tenant.ID, UserID: "u_bob", Role: "member", Status: "active"},
		&model.WorkspaceMembership{ID: NewID("wm_"), WorkspaceID: workspace.ID, UserID: "u_bob", Role: "viewer"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	_, token, err := CreateInvite(db, owner, "u_alice", "", "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	returned, acceptErr := AcceptInvite(db, "u_bob", token)
	persisted, err := ResolveScope(db, "u_bob", workspace.ID)
	if err != nil {
		t.Fatalf("invitation damaged existing membership: %v", err)
	}
	if acceptErr == nil && returned != persisted {
		t.Errorf("invitation reports ungranted authority: response=%+v persisted=%+v", returned, persisted)
	}
}

func TestAgricultureMembershipWorkspaceAdminCannotChangeOtherWorkspace(t *testing.T) {
	db, tenant, workspace, _ := agricultureMembershipFixture(t)
	other := model.Workspace{ID: NewID("ws_"), TenantID: tenant.ID, Slug: "restricted", Name: "Restricted"}
	for _, row := range []any{
		&other,
		&model.User{ID: "u_carol", Username: "u_carol", Status: model.UserStatusActive},
		&model.Membership{ID: NewID("mb_"), TenantID: tenant.ID, UserID: "u_bob", Role: "member", Status: "active"},
		&model.Membership{ID: NewID("mb_"), TenantID: tenant.ID, UserID: "u_carol", Role: "member", Status: "active"},
		&model.WorkspaceMembership{ID: NewID("wm_"), WorkspaceID: workspace.ID, UserID: "u_bob", Role: "admin"},
		&model.WorkspaceMembership{ID: NewID("wm_"), WorkspaceID: workspace.ID, UserID: "u_carol", Role: "viewer"},
		&model.WorkspaceMembership{ID: NewID("wm_"), WorkspaceID: other.ID, UserID: "u_carol", Role: "analyst"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	actor, err := ResolveScope(db, "u_bob", workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveScope(db, "u_bob", other.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("fixture administrator unexpectedly accesses other workspace: %v", err)
	}
	// Scope currently omits actor ID and tenant governance role. A secure tenant-
	// wide operation needs a verified actor parameter, not scope.Role alone.
	// Refusing the operation or limiting it to the authorized workspace is safe.
	_ = SetMemberRole(db, actor, "u_carol", "editor")
	var otherMembership model.WorkspaceMembership
	if err := db.First(&otherMembership, "workspace_id = ? AND user_id = ?", other.ID, "u_carol").Error; err != nil {
		t.Fatal(err)
	}
	if otherMembership.Role != "analyst" {
		t.Errorf("workspace-only administrator changed inaccessible workspace role to %s", otherMembership.Role)
	}
	if err := SetMemberRole(db, actor, "u_carol", "admin"); err != nil {
		t.Fatal(err)
	}
	var tenantMembership model.Membership
	if err := db.First(&tenantMembership, "tenant_id = ? AND user_id = ?", tenant.ID, "u_carol").Error; err != nil {
		t.Fatal(err)
	}
	if tenantMembership.Role != "member" {
		t.Error("workspace role update escalated tenant governance")
	}
}

func TestAgricultureMembershipRemovalPreservesOtherWorkspace(t *testing.T) {
	db, tenant, workspace, owner := agricultureMembershipFixture(t)
	other := model.Workspace{ID: NewID("ws_"), TenantID: tenant.ID, Slug: "other", Name: "Other"}
	for _, row := range []any{
		&other,
		&model.Membership{ID: NewID("mb_"), TenantID: tenant.ID, UserID: "u_bob", Role: "member", Status: "active"},
		&model.WorkspaceMembership{ID: NewID("wm_"), WorkspaceID: workspace.ID, UserID: "u_bob", Role: "viewer"},
		&model.WorkspaceMembership{ID: NewID("wm_"), WorkspaceID: other.ID, UserID: "u_bob", Role: "analyst"},
	} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveMember(db, owner, "u_bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveScope(db, "u_bob", workspace.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("selected workspace grant remained: %v", err)
	}
	remaining, err := ResolveScope(db, "u_bob", other.ID)
	if err != nil || remaining.Role != "analyst" {
		t.Fatalf("removal damaged another workspace: %+v %v", remaining, err)
	}
}

func TestAgricultureMembershipListErrorsAreNotInventedRoles(t *testing.T) {
	for _, table := range []string{"flow_workspace_memberships", "users"} {
		t.Run(table, func(t *testing.T) {
			db, _, _, owner := agricultureMembershipFixture(t)
			if err := db.Migrator().DropTable(table); err != nil {
				t.Fatal(err)
			}
			if rows, err := ListMembers(db, owner); err == nil || rows != nil {
				t.Fatalf("database failure returned invented member data: %+v %v", rows, err)
			}
		})
	}
}

func TestAgricultureMembershipInviteDatabaseFailureRollsBack(t *testing.T) {
	db, tenant, _, owner := agricultureMembershipFixture(t)
	invite, token, err := CreateInvite(db, owner, "u_alice", "", "viewer", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_workspace_insert BEFORE INSERT ON flow_workspace_memberships BEGIN SELECT RAISE(ABORT, 'test-database-failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := AcceptInvite(db, "u_bob", token); err == nil || errors.Is(err, ErrInviteInvalid) || errors.Is(err, ErrForbidden) {
		t.Fatalf("database failure disguised as invalid invitation: %v", err)
	}
	var persisted model.FlowInvite
	if err := db.First(&persisted, "id = ?", invite.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.AcceptedAt != nil {
		t.Error("failed transaction consumed invitation")
	}
	var count int64
	if err := db.Model(&model.Membership{}).Where("tenant_id = ? AND user_id = ?", tenant.ID, "u_bob").Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("failed transaction left a new membership: %d %v", count, err)
	}
}

func TestAgricultureMembershipInviteExpiredOrDisabledRejected(t *testing.T) {
	for _, condition := range []string{"expired", "disabled", "revoked"} {
		t.Run(condition, func(t *testing.T) {
			db, tenant, _, owner := agricultureMembershipFixture(t)
			invite, token, err := CreateInvite(db, owner, "u_alice", "", "viewer", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			switch condition {
			case "expired":
				err = db.Model(&invite).Update("expires_at", time.Now().UTC()).Error
			case "disabled":
				err = db.Model(&tenant).Update("status", "disabled").Error
			case "revoked":
				err = db.Model(&invite).Update("revoked_at", time.Now().UTC()).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := AcceptInvite(db, "u_bob", token); err == nil {
				t.Fatal("invalid invitation admitted")
			}
			var persisted model.FlowInvite
			if err := db.First(&persisted, "id = ?", invite.ID).Error; err != nil || persisted.AcceptedAt != nil {
				t.Fatalf("rejected invitation changed: %+v %v", persisted, err)
			}
		})
	}
}

func TestAgricultureMembershipNoActiveTenantsCannotBecomeSystemScope(t *testing.T) {
	db, tenant, _, _ := agricultureMembershipFixture(t)
	if err := db.Model(&model.Tenant{}).Where("id IN ?", []string{tenant.ID, model.PersonalTenantID("u_alice")}).Update("status", "disabled").Error; err != nil {
		t.Fatal(err)
	}
	ids, err := UserTenantIDs(db, "u_alice")
	if err != nil || len(ids) != 0 {
		t.Fatalf("inactive personal tenant resurrected by fallback: %v %v", ids, err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	entered := false
	router.GET("/probe", TenantMiddleware(db, func(*gin.Context) (*model.User, error) {
		return &model.User{ID: "u_alice"}, nil
	}), func(c *gin.Context) { entered = true; c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if entered || w.Code != http.StatusForbidden {
		t.Fatalf("empty tenant scope entered ordinary handler: entered=%v status=%d", entered, w.Code)
	}
}

func TestAgricultureMembershipLegacyCannotResolveProductWorkspaceOrConsumeInvite(t *testing.T) {
	db, _, workspace, owner := agricultureMembershipFixture(t)
	invite, token, err := CreateInvite(db, owner, "u_alice", "", "viewer", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&workspace).Update("product", "agriculture").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&invite).Update("product", "agriculture").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveScope(db, "u_alice", workspace.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("legacy role resolver accepted agricultural workspace: %v", err)
	}
	if _, err := AcceptInvite(db, "u_bob", token); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("legacy acceptance consumed product invitation: %v", err)
	}
	var persisted model.FlowInvite
	if err := db.First(&persisted, "id = ?", invite.ID).Error; err != nil || persisted.AcceptedAt != nil {
		t.Fatalf("product invite was changed: %+v %v", persisted, err)
	}
}

func TestAgricultureMembershipScopedRoutesUseRequestTransaction(t *testing.T) {
	db, _, workspace, _ := agricultureMembershipFixture(t)
	base, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	baseSQL, err := base.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = baseSQL.Close() })
	// The unrelated base database has no tables. Scope and business reads must
	// both use the real request transaction, not the registration-time handle.
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { _ = tx.Rollback().Error })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	api := router.Group("/api", func(c *gin.Context) { c.Set(reqDBKey, tx); c.Next() })
	actor := func(*gin.Context) (*model.User, error) { return &model.User{ID: "u_alice"}, nil }
	RegisterRoutes(api, base, Config{}, actor)
	RegisterP5Routes(api, base, Config{}, actor, nil)
	RegisterP7Routes(api, base, actor, &MCPGateway{DB: base})
	for _, path := range []string{"/api/intelligence/skills", "/api/intelligence/billing", "/api/intelligence/members"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Workspace-Id", workspace.ID)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("%s did not use request-scoped DB: %d %s", path, w.Code, w.Body.String())
		}
	}
}
