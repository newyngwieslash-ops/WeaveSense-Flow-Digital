package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"yingce/backend/internal/database"
	"yingce/backend/internal/model"
)

func TestWorkerTrustedTenantNotDeserializable(t *testing.T) {
	var m ModelTasks
	var d CommandDispatcher
	body := []byte(`{"tenantID":"forged","tenant_id":"forged","TenantID":"forged"}`)
	if json.Unmarshal(body, &m) != nil || json.Unmarshal(body, &d) != nil {
		t.Fatal("fixture decode")
	}
	if m.tenantID != "" || d.tenantID != "" {
		t.Fatal("request selected worker tenant")
	}
}

// Local HTTP fixtures are deliberate provider/Engine protocol peers, not live
// external services. Database, RLS, scanner, ledger and worker code are real.
func TestFlowPostgresWorkerShortTransactions(t *testing.T) {
	if os.Getenv("AGRI_TEST_PG_ISOLATED") != "1" || os.Getenv("AGRI_TEST_PG_MIGRATION_DSN") == "" || os.Getenv("AGRI_TEST_PG_RUNTIME_DSN") == "" || os.Getenv("AGRI_TEST_PG_SYSTEM_DSN") == "" {
		t.Skip("requires owned isolated app/migration/system roles")
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
		pool.SetMaxIdleConns(1)
		t.Cleanup(func() { pool.Close() })
		return db
	}
	owner, app := open("AGRI_TEST_PG_MIGRATION_DSN"), open("AGRI_TEST_PG_RUNTIME_DSN")
	if err := database.MigrateSchema(owner); err != nil {
		t.Fatal(err)
	}
	scanner, err := OpenBackgroundScanner(os.Getenv("AGRI_TEST_PG_SYSTEM_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer scanner.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	suffix := fmt.Sprintf("worker%d", time.Now().UnixNano())
	tenants := []string{"a_" + suffix, "b_" + suffix}
	for _, tenant := range tenants {
		user := "u_" + tenant
		for _, row := range []any{&model.User{ID: user, Username: user, Status: "active"}, &model.Tenant{ID: tenant, Kind: "personal", OwnerUserID: user, Status: "active"}, &model.CreditAccount{UserID: user, AvailableMicrocredits: 100000}} {
			if err := owner.Create(row).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	var calls, unscoped atomic.Int32
	probe := func() bool {
		// MaxOpenConns=1: this would time out if the worker retained a transaction
		// during HTTP. The reused connection must also have no tenant-local setting.
		probeCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
		defer stop()
		var safe bool
		err := app.WithContext(probeCtx).Raw("SELECT coalesce(current_setting('app.tenant_ids',true),'')='' AND txid_current_if_assigned() IS NULL").Scan(&safe).Error
		if err == nil && safe {
			unscoped.Add(1)
			return true
		}
		return false
	}
	var failEngine, failModel atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !probe() {
			w.WriteHeader(503)
			return
		}
		if r.URL.Path == "/status" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"succeeded","total_tokens":1,"content":"fixture"}`))
			return
		}
		if r.URL.Path == "/chat/completions" {
			if failModel.Load() {
				w.WriteHeader(503)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"fixture"}}],"usage":{"total_tokens":1}}`))
			return
		}
		if _, err := Verify(key, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "loom-engine", time.Now()); err != nil {
			w.WriteHeader(401)
			return
		}
		if failEngine.Load() {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"status":"queued"}}`))
	}))
	defer server.Close()
	gw := &OpenAICompatGateway{URL: server.URL, MicroPer1KTok: 1000, HTTP: server.Client()}
	engine := EngineClient{Cfg: Config{EngineURL: server.URL, SigningKey: key, Issuer: "flow-platform", Audience: "loom-engine", TTL: time.Minute}, HTTP: server.Client()}
	t.Run("missing_scanner_fails_closed", func(t *testing.T) {
		if _, err := (ModelTasks{DB: app, GW: gw}).RunDue(ctx); !errors.Is(err, ErrBackgroundScanner) {
			t.Fatal("model queue accepted missing scanner")
		}
		if _, err := (CommandDispatcher{DB: app, Engine: engine}).DispatchDue(ctx, 10); !errors.Is(err, ErrBackgroundScanner) {
			t.Fatal("command queue accepted missing scanner")
		}
	})
	t.Run("model_gateway_and_notification_outside_transaction", func(t *testing.T) {
		ids := []string{}
		for _, tenant := range tenants {
			if err := TenantTx(app, tenant, func(tx *gorm.DB) error {
				row, err := (ModelTasks{DB: tx}).Enqueue(ModelTaskInput{TenantID: tenant, Key: "model_" + suffix, Purpose: "fixture", BudgetMicro: 100, User: "fixture"})
				ids = append(ids, row.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		worker := ModelTasks{DB: app, GW: gw, Scanner: scanner, Notify: func(model.FlowModelTask) error {
			if !probe() {
				return errors.New("notify held transaction")
			}
			return nil
		}}
		if n, err := worker.RunDue(ctx); err != nil || n != 2 {
			t.Fatalf("model dispatch n=%d err=%v", n, err)
		}
		if n := worker.NotifyDone(ctx); n != 2 {
			t.Fatalf("notification count %d", n)
		}
		for i, id := range ids {
			if err := TenantTx(app, tenants[i], func(tx *gorm.DB) error {
				row, err := (ModelTasks{DB: tx}).Get(id)
				if err == nil && (row.Status != "succeeded" || row.NotifiedAt == nil) {
					return errors.New("model receipt missing")
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("model_reconciliation_http_outside_transaction", func(t *testing.T) {
		var task model.FlowModelTask
		if err := TenantTx(app, tenants[0], func(tx *gorm.DB) error {
			var err error
			task, err = (ModelTasks{DB: tx}).Enqueue(ModelTaskInput{TenantID: tenants[0], Key: "unknown_" + suffix, Purpose: "fixture", BudgetMicro: 100, User: "fixture"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		worker := ModelTasks{DB: app, GW: gw, Scanner: scanner, Status: ModelStatus{URL: server.URL + "/status", HTTP: server.Client(), MicroPer1KTok: 1000}}
		failModel.Store(true)
		if n, err := worker.RunDue(ctx); err != nil || n != 1 {
			t.Fatalf("unknown attempt n=%d err=%v", n, err)
		}
		failModel.Store(false)
		if err := TenantTx(app, tenants[0], func(tx *gorm.DB) error {
			row, err := (ModelTasks{DB: tx}).Get(task.ID)
			if err == nil && row.Status != StatusReconciliation {
				return errors.New("unknown model result was not durable")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if n, err := worker.Reconcile(ctx, 0); err != nil || n != 1 {
			t.Fatalf("real status reconciliation n=%d err=%v", n, err)
		}
		if err := TenantTx(app, tenants[0], func(tx *gorm.DB) error {
			row, err := (ModelTasks{DB: tx}).Get(task.ID)
			if err == nil && row.Status != "succeeded" {
				return errors.New("reconciliation not settled")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("command_http_outside_transaction_and_retry_receipt_commits", func(t *testing.T) {
		enqueue := func(tenant, key string, max int) model.FlowCommandOutbox {
			var row model.FlowCommandOutbox
			err := TenantTx(app, tenant, func(tx *gorm.DB) error {
				var err error
				row, _, err = (CommandDispatcher{DB: tx}).Enqueue(ctx, CommandInput{Scope: Scope{TenantID: tenant, WorkspaceID: "ws_" + tenant, Role: "editor"}, Actor: Actor{Type: "user", ID: "u_" + tenant}, Command: "collection.request", Method: "POST", Path: "/fixture", IdempotencyKey: key, Payload: []byte(`{}`), MaxAttempts: max, ReserveMicro: 100})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			return row
		}
		for _, tenant := range tenants {
			enqueue(tenant, "command_"+suffix, 3)
		}
		worker := CommandDispatcher{DB: app, Engine: engine, Scanner: scanner}
		if n, err := worker.DispatchDue(ctx, 10); err != nil || n != 2 {
			t.Fatalf("command dispatch n=%d err=%v", n, err)
		}
		failed := enqueue(tenants[0], "failure_"+suffix, 1)
		failEngine.Store(true)
		if _, err := worker.DispatchDue(ctx, 10); err != nil {
			t.Fatal(err)
		}
		if err := TenantTx(app, tenants[0], func(tx *gorm.DB) error {
			var row model.FlowCommandOutbox
			if err := tx.First(&row, "id=?", failed.ID).Error; err != nil {
				return err
			}
			if row.Status != StatusReconciliation {
				return errors.New("network outcome rolled back durable receipt")
			}
			var rsv model.FlowBudgetReservation
			if err := tx.First(&rsv, "id=?", row.ReservationID).Error; err != nil {
				return err
			}
			if rsv.Status != StatusReconciliation {
				return errors.New("reservation unknown state not durable")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	if calls.Load() != 7 || unscoped.Load() != 9 {
		t.Fatalf("HTTP/notification scope probes calls=%d free=%d", calls.Load(), unscoped.Load())
	}
}
