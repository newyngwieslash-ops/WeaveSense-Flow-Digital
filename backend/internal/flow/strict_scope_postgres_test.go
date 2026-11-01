package flow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
)

func TestFlowPostgresStrictScopeAndProviderAtomicity(t *testing.T) {
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
	if err := database.RequireFlowScope(app); err != nil {
		t.Fatal(err)
	}
	if err := database.RequireFlowScope(owner); err == nil {
		t.Fatal("migration owner accepted as ordinary runtime")
	}
	id := fmt.Sprintf("strict%d", time.Now().UnixNano())
	tenant, other, provider, user := "tn_"+id, "other_"+id, "provider_"+id, "user_"+id
	now := time.Now().UTC()
	for _, row := range []any{&model.User{ID: user, Username: user, Status: "active"}, &model.Tenant{ID: tenant, OwnerUserID: user, Kind: "personal", Status: "active"}, &model.Tenant{ID: other, OwnerUserID: user, Kind: "personal", Status: "active"}, &model.CreditAccount{UserID: user, AvailableMicrocredits: 10000}, &model.FlowProviderAccount{ID: provider, Status: "active", MonthlyBudgetMicro: 200, CreatedAt: now, UpdatedAt: now}} {
		if err := owner.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, tn := range []string{tenant, other} {
		if err := owner.Create(&model.FlowTopic{ID: "topic_" + tn, TenantID: tn, WorkspaceID: "ws", Title: "fixture"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, table := range []string{"flow_budget_reservations", "flow_billing_accounts", "flow_topics"} {
			if err := owner.Table(table).Where("tenant_id IN ?", []string{tenant, other}).Delete(map[string]any{}).Error; err != nil {
				t.Error("fixture cleanup failed", table)
			}
		}
		owner.Where("account_id=?", provider).Delete(&model.FlowProviderAccountPeriod{})
		owner.Where("id=?", provider).Delete(&model.FlowProviderAccount{})
		owner.Where("user_id=?", user).Delete(&model.CreditLedgerEntry{})
		owner.Where("user_id=?", user).Delete(&model.CreditAccount{})
		owner.Where("id IN ?", []string{tenant, other}).Delete(&model.Tenant{})
		owner.Where("id=?", user).Delete(&model.User{})
	})
	count := func(db *gorm.DB, tn string) int64 {
		var n int64
		if err := db.Model(&model.FlowTopic{}).Where("tenant_id=?", tn).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		return n
	}
	t.Run("empty_scope_and_pool_reuse", func(t *testing.T) {
		if count(app, tenant) != 0 {
			t.Fatal("empty scope read tenant")
		}
		if err := app.Create(&model.FlowTopic{ID: "illegal_" + id, TenantID: tenant, WorkspaceID: "ws", Title: "reject"}).Error; err == nil {
			t.Fatal("empty scope wrote tenant")
		}
		for _, tn := range []string{tenant, other, tenant} {
			rollback := errors.New("fixture rollback")
			err := TenantTx(app, tn, func(tx *gorm.DB) error {
				if count(tx, tn) != 1 {
					t.Fatal("valid scope missing")
				}
				return rollback
			})
			if !errors.Is(err, rollback) || count(app, tn) != 0 {
				t.Fatal("scope survived rollback/pool reuse")
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = TenantTx(app.WithContext(ctx), tenant, func(tx *gorm.DB) error { return tx.Exec("SELECT 1").Error })
		if count(app, tenant) != 0 {
			t.Fatal("scope survived cancelled transaction")
		}
		for _, role := range []string{"flow_platform_guard", "flow_platform_system", "flow_platform_migrate"} {
			if err := app.Exec("SET ROLE " + role).Error; err == nil {
				t.Fatal("application assumed protected role")
			}
		}
		if rows, err := app.Raw("SELECT * FROM flow_provider_accounts").Rows(); err == nil {
			rows.Close()
			t.Fatal("application read provider table")
		}
	})
	period := func() (int64, int64) {
		var row model.FlowProviderAccountPeriod
		err := owner.First(&row, "account_id=?", provider).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return row.ReservedMicro, row.ConsumedMicro
	}
	balance := func() model.CreditAccount {
		var a model.CreditAccount
		if err := owner.First(&a, "user_id=?", user).Error; err != nil {
			t.Fatal(err)
		}
		return a
	}
	t.Run("provider_and_wallet_rollback_together", func(t *testing.T) {
		rollback := errors.New("rollback after actual reserve")
		before := balance()
		err := TenantTx(app, tenant, func(tx *gorm.DB) error {
			_, e := (Billing{DB: tx, ProviderAccountID: provider}).Reserve(tenant, "rollback", "agent", "fixture", 100)
			if e != nil {
				return e
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		held, used := period()
		after := balance()
		if held != 0 || used != 0 || after.AvailableMicrocredits != before.AvailableMicrocredits || after.ReservedMicrocredits != before.ReservedMicrocredits {
			t.Fatal("cross-ledger rollback broken")
		}
	})
	t.Run("provider_state_idempotency_and_quota", func(t *testing.T) {
		var reservation model.FlowBudgetReservation
		if err := TenantTx(app, tenant, func(tx *gorm.DB) error {
			b := Billing{DB: tx, ProviderAccountID: provider}
			var e error
			reservation, e = b.Reserve(tenant, "reserve", "agent", "fixture", 100)
			if e != nil {
				return e
			}
			again, e := b.Reserve(tenant, "reserve", "agent", "fixture", 100)
			if e != nil {
				return e
			}
			if again.ID != reservation.ID {
				return errors.New("reserve not idempotent")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if held, used := period(); held != 100 || used != 0 {
			t.Fatal("duplicate reserve changed provider")
		}
		before := balance()
		err := TenantTx(app, tenant, func(tx *gorm.DB) error {
			_, e := (Billing{DB: tx, ProviderAccountID: provider}).Reserve(tenant, "overquota", "agent", "fixture", 150)
			return e
		})
		if !errors.Is(err, ErrBudgetProvider) {
			t.Fatal("provider quota not enforced", err)
		}
		if balance().AvailableMicrocredits != before.AvailableMicrocredits {
			t.Fatal("provider denial debited wallet")
		}
		if err := TenantTx(app, tenant, func(tx *gorm.DB) error {
			b := Billing{DB: tx}
			if _, e := b.Settle(reservation.ID, 40); e != nil {
				return e
			}
			_, e := b.Settle(reservation.ID, 90)
			return e
		}); err != nil {
			t.Fatal(err)
		}
		if held, used := period(); held != 0 || used != 40 {
			t.Fatal("duplicate settle changed provider")
		}
		if err := TenantTx(app, tenant, func(tx *gorm.DB) error {
			return tx.Model(&model.FlowBudgetReservation{}).Where("id=?", reservation.ID).Update("status", "reserved").Error
		}); err == nil {
			t.Fatal("terminal reservation reopened")
		}
	})
	t.Run("unknown_provider_and_cross_tenant", func(t *testing.T) {
		if err := TenantTx(app, tenant, func(tx *gorm.DB) error {
			r, e := (Billing{DB: tx, ProviderAccountID: "not_configured"}).Reserve(tenant, "unconfigured", "agent", "fixture", 10)
			if e != nil {
				return e
			}
			if r.ProviderAccountID != "" {
				return errors.New("unknown provider behavior changed")
			}
			_, e = (Billing{DB: tx}).Release(r.ID)
			return e
		}); err != nil {
			t.Fatal(err)
		}
		if err := TenantTx(app, other, func(tx *gorm.DB) error {
			_, e := (Billing{DB: tx, ProviderAccountID: provider}).Reserve(tenant, "cross", "agent", "fixture", 10)
			return e
		}); err == nil {
			t.Fatal("cross tenant reservation accepted")
		}
	})
	t.Run("child_release_and_suspended_provider_preserve_legacy_math", func(t *testing.T) {
		if err := TenantTx(app, tenant, func(tx *gorm.DB) error {
			b := Billing{DB: tx, ProviderAccountID: provider}
			root, e := b.Reserve(tenant, "parent", "agent", "fixture", 100)
			if e != nil {
				return e
			}
			child, e := b.Allocate(root.ID, "child", "tool", "fixture", 40)
			if e != nil {
				return e
			}
			if _, e = b.Settle(child.ID, 20); e != nil {
				return e
			}
			if _, e = b.Release(root.ID); e != nil {
				return e
			}
			_, e = b.Release(root.ID)
			return e
		}); err != nil {
			t.Fatal(err)
		}
		// Existing billing charges each provider-tagged child and its tagged
		// parent total. This scope migration must not silently alter that math.
		if held, used := period(); held != 0 || used != 80 {
			t.Fatal("legacy root/child provider accounting changed")
		}
		if err := owner.Model(&model.FlowProviderAccount{}).Where("id=?", provider).Update("status", "suspended").Error; err != nil {
			t.Fatal(err)
		}
		before := balance()
		err := TenantTx(app, tenant, func(tx *gorm.DB) error {
			_, e := (Billing{DB: tx, ProviderAccountID: provider}).Reserve(tenant, "suspended", "agent", "fixture", 10)
			return e
		})
		if !errors.Is(err, ErrBudgetProvider) || balance().AvailableMicrocredits != before.AvailableMicrocredits {
			t.Fatal("suspended provider did not roll back wallet")
		}
	})
}
