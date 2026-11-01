package flow

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
)

func TestFlowPostgresWaitAndNotificationScopes(t *testing.T) {
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_PG_SYSTEM_DSN") == "" {
		t.Skip("requires isolated PostgreSQL roles")
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
	scanner, e := OpenBackgroundScanner(os.Getenv("AGRI_TEST_PG_SYSTEM_DSN"))
	if e != nil {
		t.Fatal(e)
	}
	defer scanner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	scopes := []Scope{}
	users := []string{}
	for i := 0; i < 2; i++ {
		user := fmt.Sprintf("cont%d_%d", time.Now().UnixNano(), i)
		if e := owner.Create(&model.User{ID: user, Username: user, Status: "active"}).Error; e != nil {
			t.Fatal(e)
		}
		s, e := ResolveScope(owner, user, "")
		if e != nil {
			t.Fatal(e)
		}
		scopes = append(scopes, s)
		users = append(users, user)
	}
	probe := func() error {
		probeCtx, stop := context.WithTimeout(ctx, time.Second)
		defer stop()
		var safe bool
		e := app.WithContext(probeCtx).Raw("SELECT coalesce(current_setting('app.tenant_ids',true),'')='' AND txid_current_if_assigned() IS NULL").Scan(&safe).Error
		if e != nil {
			return e
		}
		if !safe {
			return fmt.Errorf("continuation retained tenant transaction")
		}
		return nil
	}
	t.Run("wait_continuation_outside_short_transaction", func(t *testing.T) {
		for i, s := range scopes {
			id := fmt.Sprintf("w%d_%d", time.Now().UnixNano(), i)
			if e := owner.Create(&model.FlowExternalWait{ID: id, TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, UserID: users[i], RunID: id, OperationID: id, Status: "ready"}).Error; e != nil {
				t.Fatal(e)
			}
		}
		calls := 0
		w := Waits{DB: app, Scanner: scanner}
		n, e := w.ResumeReadyContext(ctx, func(row model.FlowExternalWait) (string, error) {
			calls++
			if e := probe(); e != nil {
				return "", e
			}
			return "resumed_fixture", nil
		})
		if e != nil || n != 2 || calls != 2 {
			t.Fatalf("continuation count=%d calls=%d error=%v", n, calls, e)
		}
		n, e = w.ResumeReadyContext(ctx, func(model.FlowExternalWait) (string, error) { t.Error("duplicate resume"); return "", nil })
		if e != nil || n != 0 {
			t.Fatal("continuation replay changed state")
		}
		for _, s := range scopes {
			var n int64
			if e := owner.Model(&model.FlowExternalWait{}).Where("tenant_id=? AND status='resumed' AND resume_run_id='resumed_fixture'", s.TenantID).Count(&n).Error; e != nil || n != 1 {
				t.Fatal("tenant continuation receipt missing")
			}
		}
	})
	t.Run("notifications_outside_short_transaction", func(t *testing.T) {
		for i, s := range scopes {
			id := fmt.Sprintf("n%d_%d", time.Now().UnixNano(), i)
			for _, row := range []any{&model.FlowNotificationTarget{ID: id, TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, Channel: "email", Address: fmt.Sprintf("fixture%d@example.invalid", i), Status: "active"}, &model.FlowNotificationDelivery{ID: id, TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, TargetID: id, EventID: id, EventType: "report.completed", Status: "pending", MaxAttempts: 3, NextAttemptAt: time.Now().Add(-time.Minute)}} {
				if e := owner.Create(row).Error; e != nil {
					t.Fatal(e)
				}
			}
		}
		calls := 0
		n := Notifier{DB: app, Scanner: scanner, Mail: func(context.Context, string, string, string, map[string]string) (string, error) {
			calls++
			if e := probe(); e != nil {
				return "", e
			}
			return "fixture_receipt", nil
		}}
		count, e := n.RunDue(ctx)
		if e != nil || count != 2 || calls != 2 {
			t.Fatalf("notification count=%d calls=%d err=%v", count, calls, e)
		}
		for _, s := range scopes {
			var count int64
			if e := owner.Model(&model.FlowNotificationDelivery{}).Where("tenant_id=? AND status='delivered' AND receipt='fixture_receipt'", s.TenantID).Count(&count).Error; e != nil || count != 1 {
				t.Fatal("tenant notification receipt missing")
			}
		}
		if count, e = n.RunDue(ctx); e != nil || count != 0 {
			t.Fatal("notification replay repeated send")
		}
	})
}
