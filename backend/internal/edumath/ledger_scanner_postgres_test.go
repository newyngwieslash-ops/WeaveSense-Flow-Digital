package edumath

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/database"
	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

func TestEduPostgresDailyLedgerPagination(t *testing.T) {
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_PG_SYSTEM_DSN") == "" {
		t.Skip("requires isolated PostgreSQL scanner fixture")
	}
	open := func(key string) *gorm.DB {
		db, e := database.Open(database.Config{Driver: "postgres", DSN: os.Getenv(key)})
		if e != nil {
			t.Fatal("fixture connection failed")
		}
		db = db.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)})
		p, e := db.DB()
		if e != nil {
			t.Fatal(e)
		}
		p.SetMaxOpenConns(1)
		t.Cleanup(func() { p.Close() })
		return db
	}
	owner, app := open("AGRI_TEST_PG_MIGRATION_DSN"), open("AGRI_TEST_PG_RUNTIME_DSN")
	if e := database.MigrateSchema(owner); e != nil {
		t.Fatal(e)
	}
	scanner, e := flow.OpenBackgroundScanner(os.Getenv("AGRI_TEST_PG_SYSTEM_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	defer scanner.Close()
	// A bounded artificial clock keeps unrelated earlier fixture jobs outside
	// this cycle. These are deterministic fixture jobs, not real school billing.
	now := time.Now().UTC().Truncate(time.Second).Add(48 * time.Hour)
	prefix := fmt.Sprintf("ledger%d", time.Now().UnixNano())
	schools := []string{}
	for i := 0; i < 106; i++ {
		id := fmt.Sprintf("%s_%03d", prefix, i)
		schools = append(schools, id)
		at := now.Add(-time.Minute)
		if i == 105 {
			at = now.Add(-2 * time.Hour)
		}
		for _, row := range []any{&model.Tenant{ID: id, Kind: "organization", OwnerUserID: "fixture", Status: "active"}, &model.EduMathJob{ID: id, TenantID: id, WorkspaceID: "fixture", Kind: "analyze", IdemKey: id, InputHash: "fixture", ProblemRevisionID: "fixture", Status: "succeeded", Settlement: "settled", CreatedAt: at, UpdatedAt: at}} {
			if e := owner.Create(row).Error; e != nil {
				t.Fatal(e)
			}
		}
	}
	svc := &Service{DB: app, Scanner: scanner, Now: func() time.Time { return now }}
	counts := func() map[string]int64 {
		t.Helper()
		var rows []struct {
			TenantID string
			N        int64
		}
		if e := owner.Model(&model.EduAuditEvent{}).Select("tenant_id,count(*) AS n").Where("tenant_id IN ? AND action='ledger.reconcile.scheduled'", schools).Group("tenant_id").Scan(&rows).Error; e != nil {
			t.Fatal(e)
		}
		out := map[string]int64{}
		for _, r := range rows {
			out[r.TenantID] = r.N
		}
		return out
	}
	t.Run("all_105_schools_and_window", func(t *testing.T) {
		n, _, e := svc.ReconcileAll(context.Background(), time.Hour)
		if e != nil || n != 105 {
			t.Fatalf("daily pass visited=%d error=%v", n, e)
		}
		rows := counts()
		for _, id := range schools[:105] {
			if rows[id] != 1 {
				t.Fatal("school missing or repeated in daily pass")
			}
		}
		if rows[schools[105]] != 0 {
			t.Fatal("old-only school was reconciled outside window")
		}
	})
	t.Run("cancelled_pass_restarts_without_missing_schools", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var auditCalls atomic.Int32
		callbackName := "fixture_cancel_ledger_pass"
		if e := app.Callback().Create().After("gorm:create").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "edu_audit_events" && auditCalls.Add(1) == 7 {
				cancel()
			}
		}); e != nil {
			t.Fatal(e)
		}
		n, _, e := svc.ReconcileAll(ctx, time.Hour)
		if removeErr := app.Callback().Create().Remove(callbackName); removeErr != nil {
			t.Fatal(removeErr)
		}
		if !errors.Is(e, context.Canceled) || n >= 105 || auditCalls.Load() < 7 {
			t.Fatalf("cancellation did not stop partial pass count=%d", n)
		}
		before := counts()
		n, _, e = svc.ReconcileAll(context.Background(), time.Hour)
		if e != nil || n != 105 {
			t.Fatalf("retry lost schools count=%d error=%v", n, e)
		}
		after := counts()
		for _, id := range schools[:105] {
			if after[id] != before[id]+1 {
				t.Fatal("retry skipped or repeated school")
			}
		}
		if after[schools[105]] != 0 {
			t.Fatal("retry included old window")
		}
		var empty bool
		if e := app.Raw("SELECT coalesce(current_setting('app.tenant_ids',true),'')=''").Scan(&empty).Error; e != nil || !empty {
			t.Fatal("cancelled ledger leaked pooled scope")
		}
	})
}
