package database

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Run only against a disposable database initialized by the agriculture Compose
// role bootstrap. This test does not create/drop databases or use a superuser.
func TestAgriculturePostgresAdmissionAndStrictRLS(t *testing.T) {
	ownerDSN, appDSN := os.Getenv("AGRI_TEST_PG_MIGRATION_DSN"), os.Getenv("AGRI_TEST_PG_RUNTIME_DSN")
	if ownerDSN == "" || appDSN == "" || os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" {
		t.Skip("not_run: disposable PostgreSQL agriculture migration/runtime DSNs and AGRI_TEST_PG_ISOLATED=1 required")
	}
	open := func(dsn string) *gorm.DB {
		db, err := Open(Config{Driver: "postgres", DSN: dsn})
		if err != nil {
			t.Fatal("cannot open PostgreSQL test connection")
		}
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		conn, err := db.DB()
		if err != nil {
			t.Fatal("cannot get PostgreSQL pool")
		}
		conn.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = conn.Close() })
		return db
	}
	owner, app := open(ownerDSN), open(appDSN)
	if err := MigrateSchema(owner); err != nil {
		t.Fatal("migration failed (details deliberately not logged with test credentials)")
	}
	if err := RequireAgricultureAdmission(app); err != nil {
		t.Fatal(err)
	}
	var isOwner bool
	if err := app.Raw("SELECT pg_has_role(current_user,'flow_platform_admission','MEMBER')").Scan(&isOwner).Error; err != nil || isOwner {
		t.Fatal("runtime inherited admission owner")
	}
	if err := app.Transaction(func(tx *gorm.DB) error { return tx.Exec("SET LOCAL ROLE flow_platform_admission").Error }); err == nil {
		t.Fatal("runtime can SET ROLE admission")
	}
	prefix := fmt.Sprintf("agritest_%d", time.Now().UnixNano())
	tenant, workspace, actor, other := prefix+"t", prefix+"w", prefix+"u", prefix+"x"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(prefix)))
	verified := time.Now().UTC()
	fixtures := []any{
		&model.User{ID: actor, Username: actor, Email: prefix + "@example.invalid", EmailVerifiedAt: &verified, Status: model.UserStatusActive},
		&model.User{ID: other, Username: other, Email: prefix + "other@example.invalid", EmailVerifiedAt: &verified, Status: model.UserStatusActive},
		&model.Tenant{ID: tenant, Kind: "organization", Name: "Admission fixture", Status: "active"},
		&model.Workspace{ID: workspace, TenantID: tenant, Slug: "default", Product: "agriculture"},
		&model.FlowInvite{ID: prefix + "i", TenantID: tenant, WorkspaceID: workspace, Product: "agriculture", Role: "viewer", Email: strings.ToUpper(prefix) + "@EXAMPLE.INVALID", TokenHash: hash, ExpiresAt: verified.Add(time.Hour)},
	}
	for _, fixture := range fixtures {
		if err := owner.Create(fixture).Error; err != nil {
			t.Fatal("cannot create isolated admission fixture")
		}
	}
	t.Cleanup(func() {
		_ = owner.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('app.tenant_ids', ?, true)", tenant).Error; err != nil {
				return err
			}
			for _, table := range []string{"flow_agriculture_role_bindings", "flow_agriculture_audit_events", "flow_invites", "flow_workspaces"} {
				if err := tx.Table(table).Where("tenant_id = ?", tenant).Delete(map[string]any{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("id = ?", tenant).Delete(&model.Tenant{}).Error; err != nil {
				return err
			}
			return tx.Where("id IN ?", []string{actor, other}).Delete(&model.User{}).Error
		})
	})
	resolve := func(token, user string, want int) {
		t.Helper()
		var rows []struct{ InviteID, TenantID, WorkspaceID, RecipientUserID string }
		if err := app.Raw("SELECT * FROM agri_admission.resolve_invite(?, ?)", token, user).Scan(&rows).Error; err != nil {
			t.Fatal("admission query failed")
		}
		if len(rows) != want {
			t.Fatalf("admission returned %d rows, want %d", len(rows), want)
		}
		if want == 1 && (rows[0].TenantID != tenant || rows[0].WorkspaceID != workspace || rows[0].RecipientUserID != actor) {
			t.Fatal("wrong admission scope")
		}
	}
	resolve(hash, actor, 1)
	if err := app.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.tenant_ids', ?, true)", "unrelated-existing-tenant").Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Raw("SELECT count(*) FROM agri_admission.resolve_invite(?, ?)", hash, actor).Scan(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			t.Fatal("narrow admission cannot resolve invited tenant outside existing membership")
		}
		return nil
	}); err != nil {
		t.Fatal("cross-membership admission resolution failed")
	}
	resolve(hash, other, 0)
	resolve("", actor, 0)
	resolve(strings.Repeat("f", 64), actor, 0)
	resolve("' OR TRUE --", actor, 0)
	for _, change := range []struct {
		table, column  string
		invalid, valid any
	}{
		{"flow_invites", "product", "", "agriculture"},
		{"flow_workspaces", "product", "", "agriculture"},
		{"flow_invites", "accepted_at", verified, nil},
		{"flow_invites", "revoked_at", verified, nil},
		{"flow_invites", "expires_at", verified.Add(-time.Hour), verified.Add(time.Hour)},
		{"flow_tenants", "status", "disabled", "active"},
		{"users", "status", "disabled", "active"},
		{"users", "email_verified_at", nil, verified},
	} {
		key := "tenant_id"
		value := tenant
		if change.table == "flow_tenants" {
			key = "id"
		}
		if change.table == "users" {
			key, value = "id", actor
		}
		if err := owner.Table(change.table).Where(key+" = ?", value).Update(change.column, change.invalid).Error; err != nil {
			t.Fatal("cannot alter negative fixture")
		}
		resolve(hash, actor, 0)
		if err := owner.Table(change.table).Where(key+" = ?", value).Update(change.column, change.valid).Error; err != nil {
			t.Fatal("cannot restore negative fixture")
		}
	}
	withScope := func(scope string, fn func(*gorm.DB) error) error {
		return app.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('app.tenant_ids', ?, true)", scope).Error; err != nil {
				return err
			}
			return fn(tx)
		})
	}
	for _, table := range []string{"flow_agriculture_role_bindings", "flow_agriculture_audit_events"} {
		var force bool
		if err := owner.Raw("SELECT relrowsecurity AND relforcerowsecurity FROM pg_class WHERE oid = ?::regclass", "public."+table).Scan(&force).Error; err != nil || !force {
			t.Fatal("agriculture table lacks FORCE RLS")
		}
	}
	role := model.AgricultureRoleBinding{ID: prefix + "r", TenantID: tenant, WorkspaceID: workspace, UserID: actor, Role: "viewer"}
	if err := app.Create(&role).Error; err == nil {
		t.Fatal("unscoped role insert permitted")
	}
	if err := withScope(tenant, func(tx *gorm.DB) error { return tx.Create(&role).Error }); err != nil {
		t.Fatal("authorized role insert failed")
	}
	for _, scope := range []string{"", tenant + "other"} {
		if err := withScope(scope, func(tx *gorm.DB) error {
			var count int64
			if err := tx.Model(&model.AgricultureRoleBinding{}).Where("id = ?", role.ID).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				t.Fatal("cross-scope read leaked row")
			}
			return nil
		}); err != nil {
			t.Fatal("scope test failed")
		}
	}
	var count int64
	if err := app.Model(&model.AgricultureRoleBinding{}).Where("id = ?", role.ID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("connection reuse retained transaction scope")
	}
	role.ID += "b"
	role.TenantID += "other"
	if err := withScope(tenant, func(tx *gorm.DB) error { return tx.Create(&role).Error }); err == nil {
		t.Fatal("cross-tenant write permitted")
	}
	audit := model.AgricultureAuditEvent{ID: prefix + "audit", TenantID: tenant, WorkspaceID: workspace, ActorID: actor, SubjectID: actor, Action: "test.fixture", Detail: "{}"}
	if err := app.Create(&audit).Error; err == nil {
		t.Fatal("unscoped audit write permitted")
	}
	if err := withScope(tenant, func(tx *gorm.DB) error { return tx.Create(&audit).Error }); err != nil {
		t.Fatal("authorized audit insert failed")
	}
	for _, table := range []string{"flow_agriculture_role_bindings", "flow_agriculture_audit_events"} {
		for _, sample := range []struct {
			scope string
			want  int64
		}{{tenant, 1}, {tenant + "other", 0}, {"", 0}} {
			if err := withScope(sample.scope, func(tx *gorm.DB) error {
				var count int64
				if err := tx.Table(table).Where("tenant_id = ?", tenant).Count(&count).Error; err != nil {
					return err
				}
				if count != sample.want {
					t.Fatalf("%s scope read returned %d want %d", table, count, sample.want)
				}
				return nil
			}); err != nil {
				t.Fatal("tenant read validation failed")
			}
		}
	}
	audit.ID += "other"
	audit.TenantID += "other"
	if err := withScope(tenant, func(tx *gorm.DB) error { return tx.Create(&audit).Error }); err == nil {
		t.Fatal("cross-tenant audit write permitted")
	}
}
