package agriculture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"yingce/backend/internal/database"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func postgresFixture(t *testing.T) (Service, *gorm.DB, string, string) {
	t.Helper()
	ownerDSN, appDSN := os.Getenv("AGRI_TEST_PG_MIGRATION_DSN"), os.Getenv("AGRI_TEST_PG_RUNTIME_DSN")
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || ownerDSN == "" || appDSN == "" {
		t.Skip("requires explicitly isolated PostgreSQL migration/runtime fixtures")
	}
	open := func(dsn string) *gorm.DB {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
		if err != nil {
			t.Fatal("cannot open configured isolated PostgreSQL fixture")
		}
		conn, err := db.DB()
		if err != nil {
			t.Fatal("cannot obtain isolated database pool")
		}
		conn.SetMaxOpenConns(5)
		t.Cleanup(func() { _ = conn.Close() })
		return db
	}
	owner, app := open(ownerDSN), open(appDSN)
	if err := database.MigrateSchema(owner); err != nil {
		t.Fatalf("fixture migration failed: %T", err)
	}
	if err := database.RequireAgricultureAdmission(app); err != nil {
		t.Fatal("runtime admission gate did not pass")
	}
	var unsafe bool
	if err := app.Raw("SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user").Scan(&unsafe).Error; err != nil || unsafe {
		t.Fatal("fixture runtime must be nonprivileged")
	}
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(random[:])
	a, b := "pgowner_"+suffix, "pgmember_"+suffix
	now := time.Now().UTC()
	for _, id := range []string{a, b} {
		if err := owner.Create(&model.User{ID: id, Username: id, Email: id + "@fixture.invalid", EmailVerifiedAt: &now, DisplayName: "Isolated PostgreSQL fixture", Status: model.UserStatusActive, Role: model.UserRoleUser}).Error; err != nil {
			t.Fatalf("fixture user setup failed: %T", err)
		}
	}
	t.Cleanup(func() {
		var tenants []model.Tenant
		if err := owner.Where("owner_user_id=?", a).Find(&tenants).Error; err != nil {
			t.Error("fixture cleanup tenant lookup failed")
			return
		}
		for _, tenant := range tenants {
			err := flow.TenantTx(owner, tenant.ID, func(tx *gorm.DB) error {
				for _, table := range []string{"flow_agriculture_audit_events", "flow_agriculture_role_bindings", "flow_invites", "flow_billing_accounts", "flow_memberships"} {
					if err := tx.Table(table).Where("tenant_id=?", tenant.ID).Delete(map[string]any{}).Error; err != nil {
						return err
					}
				}
				if err := tx.Where("workspace_id IN (?)", tx.Model(&model.Workspace{}).Select("id").Where("tenant_id=?", tenant.ID)).Delete(&model.WorkspaceMembership{}).Error; err != nil {
					return err
				}
				if err := tx.Where("tenant_id=?", tenant.ID).Delete(&model.Workspace{}).Error; err != nil {
					return err
				}
				return tx.Where("id=?", tenant.ID).Delete(&model.Tenant{}).Error
			})
			if err != nil {
				t.Errorf("fixture cleanup failed: %T", err)
			}
		}
		if err := owner.Where("id IN ?", []string{a, b}).Delete(&model.User{}).Error; err != nil {
			t.Errorf("fixture user cleanup failed: %T", err)
		}
	})
	return Service{DB: app}, owner, a, b
}

func TestAgriculturePostgresMembershipLifecycle(t *testing.T) {
	s, owner, actor, recipient := postgresFixture(t)
	ctx := context.Background()
	org, err := s.CreateOrganization(ctx, actor, "PostgreSQL isolated enterprise", "pg-create-fixture-001")
	if err != nil {
		t.Fatalf("nonowner runtime organization creation failed: %T %v", err, err)
	}
	if repeat, err := s.CreateOrganization(ctx, actor, "PostgreSQL isolated enterprise", "pg-create-fixture-001"); err != nil || repeat.Workspace.ID != org.Workspace.ID {
		t.Fatal("PostgreSQL idempotent create failed")
	}
	checkOwnerBusinessRoleLifecycle(t, s, actor, org)
	inv, err := s.CreateInvite(ctx, actor, org.Workspace.ID, recipient+"@fixture.invalid", "analyst")
	if err != nil {
		t.Fatalf("runtime invite creation failed: %T", err)
	}
	if _, err := s.AcceptInvite(ctx, actor, inv.Token); !errors.Is(err, ErrInvitation) {
		t.Fatal("wrong recipient admission was not rejected")
	}
	if err := owner.Model(&model.User{}).Where("id=?", recipient).Update("email_verified_at", nil).Error; err != nil {
		t.Fatal("fixture verification change failed")
	}
	if _, err := s.AcceptInvite(ctx, recipient, inv.Token); !errors.Is(err, ErrInvitation) {
		t.Fatal("unverified recipient admitted")
	}
	if err := owner.Model(&model.User{}).Where("id=?", recipient).Update("email_verified_at", time.Now()).Error; err != nil {
		t.Fatal("fixture verification restore failed")
	}
	accepted, err := s.AcceptInvite(ctx, recipient, inv.Token)
	if err != nil || len(accepted.Roles) != 1 || accepted.Roles[0] != "analyst" {
		t.Fatalf("cross-tenant narrow admission failed: %T", err)
	}
	if _, err := s.AcceptInvite(ctx, recipient, inv.Token); !errors.Is(err, ErrInvitation) {
		t.Fatal("consumed PostgreSQL invitation replayed")
	}
	var visible int64
	if err := s.DB.Model(&model.AgricultureRoleBinding{}).Where("tenant_id=?", org.Tenant.ID).Count(&visible).Error; err != nil || visible != 0 {
		t.Fatal("ordinary unscoped runtime read role bindings")
	}
	views, err := s.ListWorkspaces(ctx, recipient)
	if err != nil || len(views) != 1 || views[0].CanManageMembers {
		t.Fatal("runtime workspace projection exceeds membership")
	}
	roles := []string{"tenant_admin"}
	if err := s.ChangeMember(ctx, recipient, org.Workspace.ID, actor, &roles, nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("analyst changed tenant governance")
	}
	if err := s.ChangeMember(ctx, actor, org.Workspace.ID, recipient, &roles, nil); err != nil {
		t.Fatalf("owner role grant failed: %T", err)
	}
	if _, err := s.Members(ctx, recipient, org.Workspace.ID); err != nil {
		t.Fatalf("authorized governance failed: %T", err)
	}
	if _, err := flow.ResolveScope(s.DB, recipient, org.Workspace.ID); !errors.Is(err, flow.ErrForbidden) {
		t.Fatal("legacy route bypassed agriculture profile")
	}
	status := "suspended"
	if err := s.ChangeMember(ctx, actor, org.Workspace.ID, recipient, nil, &status); err != nil {
		t.Fatal("member suspension failed")
	}
	if _, err := s.Members(ctx, recipient, org.Workspace.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("suspended membership retained authorization")
	}
	if err := s.ChangeOrganizationStatus(ctx, actor, org.Workspace.ID, "disabled"); err != nil {
		t.Fatal("owner could not disable organization")
	}
	if views, err := s.ListWorkspaces(ctx, recipient); err != nil || len(views) != 0 {
		t.Fatal("disabled tenant still visible")
	}
	if err := s.ChangeOrganizationStatus(ctx, actor, org.Workspace.ID, "active"); err != nil {
		t.Fatal("explicit owner restore failed")
	}
	var scope string
	if err := s.DB.Raw("SELECT coalesce(current_setting('app.tenant_ids',true),'')").Scan(&scope).Error; err != nil || scope != "" {
		t.Fatal("transaction tenant scope leaked into pool")
	}
}

func TestAgriculturePostgresConcurrentAcceptance(t *testing.T) {
	s, _, actor, recipient := postgresFixture(t)
	ctx := context.Background()
	org, err := s.CreateOrganization(ctx, actor, "Concurrent fixture", "pg-concurrent-create-001")
	if err != nil {
		t.Fatalf("create fixture: %T", err)
	}
	inv, err := s.CreateInvite(ctx, actor, org.Workspace.ID, recipient+"@fixture.invalid", "viewer")
	if err != nil {
		t.Fatalf("invite fixture: %T", err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := s.AcceptInvite(ctx, recipient, inv.Token); results <- err }()
	}
	close(start)
	wg.Wait()
	close(results)
	passed, rejected := 0, 0
	for err := range results {
		if err == nil {
			passed++
		} else if errors.Is(err, ErrInvitation) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent failure: %T", err)
		}
	}
	if passed != 1 || rejected != 1 {
		t.Fatalf("one-time acceptance: passed=%d rejected=%d", passed, rejected)
	}
	if err := flow.TenantTx(s.DB, org.Tenant.ID, func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&model.AgricultureRoleBinding{}).Where("tenant_id=? AND user_id=?", org.Tenant.ID, recipient).Count(&n).Error; err != nil {
			return err
		}
		if n != 1 {
			t.Fatal("duplicate role binding from repeated acceptance")
		}
		return nil
	}); err != nil {
		t.Fatalf("verification query failed: %T", err)
	}
}
