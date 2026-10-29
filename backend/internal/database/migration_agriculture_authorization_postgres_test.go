package database

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"yingce/backend/internal/model"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAgriculturePostgresAuthorizationBindingsAndEpoch(t *testing.T) {
	ownerDSN, appDSN := os.Getenv("AGRI_TEST_PG_MIGRATION_DSN"), os.Getenv("AGRI_TEST_PG_RUNTIME_DSN")
	if ownerDSN == "" || appDSN == "" || os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" {
		t.Skip("not_run: isolated PostgreSQL migration/runtime DSNs required")
	}
	open := func(dsn string) *gorm.DB {
		db, err := Open(Config{Driver: "postgres", DSN: dsn})
		if err != nil {
			t.Fatal("cannot open authorization PostgreSQL fixture")
		}
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		pool, err := db.DB()
		if err != nil {
			t.Fatal("cannot open fixture pool")
		}
		pool.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	owner, app := open(ownerDSN), open(appDSN)
	if err := MigrateSchema(owner); err != nil {
		t.Fatal("authorization migration failed")
	}
	if err := MigrateSchema(owner); err != nil {
		t.Fatal("authorization migration replay failed")
	}
	if err := RequireAgricultureAdmission(app); err != nil {
		t.Fatal("runtime role unsafe")
	}
	var safeGuard bool
	if err := app.Raw(`SELECT EXISTS (
 SELECT 1 FROM pg_catalog.pg_trigger tr JOIN pg_catalog.pg_proc p ON p.oid=tr.tgfoid
 WHERE tr.tgrelid='public.flow_agriculture_authorization_epochs'::regclass
 AND tr.tgname='agriculture_authorization_epoch_guard' AND tr.tgenabled='O'
 AND NOT p.prosecdef AND p.proconfig @> ARRAY['search_path=pg_catalog']::text[]
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.aclexplode(p.proacl) a WHERE a.grantee=0 AND a.privilege_type='EXECUTE')
)`).Scan(&safeGuard).Error; err != nil || !safeGuard {
		t.Fatal("epoch guard must be invoker, fixed search path and no PUBLIC execution")
	}
	binding := authorizationFixture(t, owner, fmt.Sprintf("az%d", time.Now().UnixNano()))
	scope := func(db *gorm.DB, tenant string, fn func(*gorm.DB) error) error {
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("SELECT set_config('app.tenant_ids', ?, true)", tenant).Error; err != nil {
				return err
			}
			return fn(tx)
		})
	}
	t.Cleanup(func() {
		if err := scope(owner, binding.TenantID, func(tx *gorm.DB) error {
			for _, table := range []string{"flow_agriculture_resource_bindings", "flow_agriculture_authorization_epochs", "flow_memberships"} {
				if err := tx.Table(table).Where("tenant_id = ?", binding.TenantID).Delete(map[string]any{}).Error; err != nil {
					return err
				}
			}
			var other model.Workspace
			if err := tx.First(&other, "id = ?", binding.WorkspaceID[:len(binding.WorkspaceID)-1]+"z").Error; err != nil {
				return err
			}
			if err := tx.Where("tenant_id IN ?", []string{binding.TenantID, other.TenantID}).Delete(&model.Workspace{}).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", []string{binding.TenantID, other.TenantID}).Delete(&model.Tenant{}).Error; err != nil {
				return err
			}
			return tx.Where("id = ?", binding.SubjectID).Delete(&model.User{}).Error
		}); err != nil {
			t.Error("authorization fixture cleanup failed")
		}
	})
	for _, table := range []string{"flow_agriculture_resource_bindings", "flow_agriculture_authorization_epochs"} {
		var forced bool
		if err := owner.Raw("SELECT relrowsecurity AND relforcerowsecurity FROM pg_class WHERE oid = ?::regclass", table).Scan(&forced).Error; err != nil || !forced {
			t.Fatal("authorization table lacks forced isolation")
		}
	}
	epoch := model.AgricultureAuthorizationEpoch{TenantID: binding.TenantID, Version: 1, UpdatedAt: time.Now().UTC()}
	if app.Create(&epoch).Error == nil {
		t.Fatal("missing scope created epoch")
	}
	if app.Create(&binding).Error == nil {
		t.Fatal("missing scope created resource binding")
	}
	for _, row := range []any{&epoch, &binding} {
		err := scope(app, binding.TenantID+"wrong", func(tx *gorm.DB) error { return tx.Create(row).Error })
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "42501" {
			t.Fatal("cross-scope write must be rejected by RLS")
		}
	}
	if err := scope(app, binding.TenantID, func(tx *gorm.DB) error {
		if err := tx.Create(&epoch).Error; err != nil {
			return err
		}
		return tx.Create(&binding).Error
	}); err != nil {
		t.Fatal("valid scoped resource binding failed")
	}
	for _, tenant := range []string{"", binding.TenantID + "wrong"} {
		if err := scope(app, tenant, func(tx *gorm.DB) error {
			for _, table := range []string{"flow_agriculture_resource_bindings", "flow_agriculture_authorization_epochs"} {
				var count int64
				if err := tx.Table(table).Where("tenant_id = ?", binding.TenantID).Count(&count).Error; err != nil {
					return err
				}
				if count != 0 {
					return errors.New("out-of-scope authorization visible")
				}
			}
			return nil
		}); err != nil {
			t.Fatal("empty/cross scope read permitted")
		}
	}
	for _, kind := range []string{"duplicate", "workspace", "subject"} {
		if err := scope(app, binding.TenantID, func(tx *gorm.DB) error {
			copy := binding
			copy.ID = binding.ID + "n"
			switch kind {
			case "workspace":
				copy.WorkspaceID = binding.WorkspaceID[:len(binding.WorkspaceID)-1] + "z"
			case "subject":
				copy.SubjectID = "not-member"
			}
			return tx.Create(&copy).Error
		}); err == nil {
			t.Fatalf("%s resource constraint not enforced", kind)
		}
	}
	if err := scope(app, binding.TenantID, func(tx *gorm.DB) error {
		return tx.Where("tenant_id = ?", binding.TenantID).Delete(&model.AgricultureAuthorizationEpoch{}).Error
	}); err == nil {
		t.Fatal("ordinary runtime deleted the revocation clock")
	} else {
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "42501" {
			t.Fatal("epoch deletion must fail with insufficient privilege")
		}
	}
	for _, invalidVersion := range []int64{0, 1, 3, 9007199254740992} {
		err := scope(app, binding.TenantID, func(tx *gorm.DB) error {
			return tx.Model(&epoch).Where("tenant_id = ?", binding.TenantID).Update("version", invalidVersion).Error
		})
		var pgError *pgconn.PgError
		if !errors.As(err, &pgError) || pgError.Code != "23514" {
			t.Fatal("invalid epoch must fail database CHECK")
		}
	}
	rollback := errors.New("fixture atomic authorization rollback")
	if err := scope(app, binding.TenantID, func(tx *gorm.DB) error {
		if err := tx.Model(&epoch).Where("tenant_id = ?", binding.TenantID).Update("version", gorm.Expr("version + 1")).Error; err != nil {
			return err
		}
		if err := tx.Model(&binding).Where("id = ?", binding.ID).Update("state", "revoked").Error; err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal("authorization rollback fixture failed")
	}
	if err := scope(app, binding.TenantID, func(tx *gorm.DB) error {
		if err := tx.First(&epoch, "tenant_id = ?", binding.TenantID).Error; err != nil {
			return err
		}
		if err := tx.First(&binding, "id = ?", binding.ID).Error; err != nil {
			return err
		}
		if epoch.Version != 1 || binding.State != "active" {
			return errors.New("authorization clock and binding changed after rollback")
		}
		return nil
	}); err != nil {
		t.Fatal("authorization rollback was not atomic")
	}
	var count int64
	if err := app.Model(&binding).Where("tenant_id = ?", binding.TenantID).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("pooled connection leaked tenant scope")
	}
}
