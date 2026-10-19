package flow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
)

func TestCommandOutboxTransactionalEnqueueAndDispatch(t *testing.T) {
	db := testDB(t)
	fe, client := newFakeEngine(t)
	scope, err := ResolveScope(db, "u_alice", "")
	if err != nil {
		t.Fatal(err)
	}
	fund(t, Billing{DB: db}, "u_alice", 50_000)

	now := time.Unix(1_800_000_000, 0).UTC()
	d := CommandDispatcher{
		DB:     db,
		Engine: client,
		Now:    func() time.Time { return now },
	}
	payload := []byte(`{"source_ids":["src_1"]}`)
	row, replayed, err := d.Enqueue(context.Background(), CommandInput{
		Scope:          scope,
		Actor:          Actor{Type: "user", ID: "u_alice"},
		Command:        "collection.request",
		Method:         http.MethodPost,
		Path:           "/v1/collection-runs",
		IdempotencyKey: "idem-collect-1",
		Payload:        payload,
		ReserveMicro:   1_000,
	})
	if err != nil || replayed {
		t.Fatalf("enqueue: err=%v replayed=%v", err, replayed)
	}
	if row.Status != "pending" || row.OperationID == "" || row.ReservationID == "" {
		t.Fatalf("unexpected outbox row: %+v", row)
	}

	// Verify initial operation projection and budget reservation were created in the same transaction.
	var proj model.FlowOperationProjection
	if err := db.First(&proj, "operation_id = ?", row.OperationID).Error; err != nil || proj.Status != "queued" {
		t.Fatalf("initial projection missing or wrong status: %+v err=%v", proj, err)
	}
	var rsv model.FlowBudgetReservation
	if err := db.First(&rsv, "id = ?", row.ReservationID).Error; err != nil || rsv.Status != "reserved" || rsv.ReservedMicro != 1_000 {
		t.Fatalf("reservation missing or wrong state: %+v err=%v", rsv, err)
	}

	// Dispatch the command to the Engine.
	dispatched, err := d.DispatchOne(context.Background(), row.ID)
	if err != nil {
		t.Fatalf("dispatch one: %v", err)
	}
	if dispatched.Status != "dispatched" || dispatched.ResponseStatus != 200 || dispatched.EngineOperationID != "op_idem-collect-1" {
		t.Fatalf("unexpected dispatched row: %+v", dispatched)
	}
	if fe.calls["POST /v1/collection-runs"] != 1 {
		t.Fatalf("want 1 engine call, got %d", fe.calls["POST /v1/collection-runs"])
	}

	// Idempotent replay with identical parameters returns the stored dispatched row without calling Engine again.
	replayRow, replayed2, err := d.EnqueueAndDispatch(context.Background(), CommandInput{
		Scope:          scope,
		Actor:          Actor{Type: "user", ID: "u_alice"},
		Command:        "collection.request",
		Method:         http.MethodPost,
		Path:           "/v1/collection-runs",
		IdempotencyKey: "idem-collect-1",
		Payload:        payload,
	})
	if err != nil || !replayed2 || replayRow.ID != row.ID || fe.calls["POST /v1/collection-runs"] != 1 {
		t.Fatalf("replay failed: err=%v replayed=%v calls=%d", err, replayed2, fe.calls["POST /v1/collection-runs"])
	}

	// Reusing the same idempotency key with different parameters returns ErrCommandConflict.
	_, _, err = d.Enqueue(context.Background(), CommandInput{
		Scope:          scope,
		Actor:          Actor{Type: "user", ID: "u_alice"},
		Command:        "collection.request",
		Method:         http.MethodPost,
		Path:           "/v1/collection-runs",
		IdempotencyKey: "idem-collect-1",
		Payload:        []byte(`{"source_ids":["src_2"]}`),
	})
	if !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("want ErrCommandConflict, got %v", err)
	}
}

func TestCommandOutboxTransientFailureRetryAndReconciliation(t *testing.T) {
	db := testDB(t)
	scope, err := ResolveScope(db, "u_alice", "")
	if err != nil {
		t.Fatal(err)
	}
	fund(t, Billing{DB: db}, "u_alice", 50_000)

	var shouldFail atomic.Bool
	shouldFail.Store(true)
	var engineCalls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		engineCalls.Add(1)
		if shouldFail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"code":502,"msg":"upstream unavailable"}`))
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"code":0,"data":{"operation_id":"op_recovered","job_ids":["job_1"]},"msg":"accepted"}`))
	}))
	t.Cleanup(srv.Close)

	now := time.Unix(1_800_000_000, 0).UTC()
	client := EngineClient{Cfg: Config{EngineURL: srv.URL, SigningKey: key, Issuer: "flow-platform", Audience: "loom-engine", TTL: MaxTokenTTL}}
	d := CommandDispatcher{
		DB:      db,
		Engine:  client,
		Now:     func() time.Time { return now },
		Backoff: 5 * time.Second,
	}

	// 1. First attempt fails with 502 -> row enters retry_wait with backoff.
	row, _, err := d.EnqueueAndDispatch(context.Background(), CommandInput{
		Scope:          scope,
		Actor:          Actor{Type: "user", ID: "u_alice"},
		Command:        "collection.request",
		Method:         http.MethodPost,
		Path:           "/v1/collection-runs",
		IdempotencyKey: "idem-retry-1",
		Payload:        []byte(`{"source_ids":["src_1"]}`),
		MaxAttempts:    3,
	})
	if err == nil || row.Status != StatusRetryWait || row.Attempts != 1 {
		t.Fatalf("want retry_wait after 1st failure, got status=%s attempts=%d err=%v", row.Status, row.Attempts, err)
	}

	// Before backoff expires, DispatchDue does nothing.
	if n, err := d.DispatchDue(context.Background(), 10); err != nil || n != 0 {
		t.Fatalf("expected 0 dispatched before backoff, got %d err=%v", n, err)
	}

	// Advance clock past backoff and recover Engine -> DispatchDue delivers the command.
	now = now.Add(6 * time.Second)
	shouldFail.Store(false)
	if n, err := d.DispatchDue(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("expected 1 dispatched after backoff, got %d err=%v", n, err)
	}
	var updated model.FlowCommandOutbox
	db.First(&updated, "id = ?", row.ID)
	if updated.Status != "dispatched" || updated.EngineOperationID != "op_recovered" {
		t.Fatalf("expected dispatched after recovery, got %+v", updated)
	}

	// 2. Exhausting MaxAttempts transitions row and reservation to reconciliation_required (no blind resend).
	shouldFail.Store(true)
	row2, _, _ := d.Enqueue(context.Background(), CommandInput{
		Scope:          scope,
		Actor:          Actor{Type: "user", ID: "u_alice"},
		Command:        "collection.request",
		Method:         http.MethodPost,
		Path:           "/v1/collection-runs",
		IdempotencyKey: "idem-exhaust-1",
		Payload:        []byte(`{"source_ids":["src_1"]}`),
		ReserveMicro:   2_000,
		MaxAttempts:    2,
	})
	_, _ = d.DispatchOne(context.Background(), row2.ID)
	now = now.Add(60 * time.Second)
	exhausted, _ := d.DispatchOne(context.Background(), row2.ID)
	if exhausted.Status != StatusReconciliation || exhausted.Attempts != 2 {
		t.Fatalf("expected reconciliation_required after MaxAttempts, got %+v", exhausted)
	}
	var rsv2 model.FlowBudgetReservation
	db.First(&rsv2, "id = ?", row2.ReservationID)
	if rsv2.Status != StatusReconciliation {
		t.Fatalf("expected reservation in reconciliation_required, got %+v", rsv2)
	}
}

func TestCommandOutboxRESTAsyncAndConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	fe, client := newFakeEngine(t)
	cfg := client.Cfg
	r := gin.New()
	RegisterRoutes(r.Group("/api"), db, cfg, func(*gin.Context) (*model.User, error) {
		return &model.User{ID: "u_alice"}, nil
	})

	// 1. Async outbox mode enqueues without calling Engine immediately and returns 202 Accepted.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/intelligence/collection-runs", strings.NewReader(`{"source_ids":["src_1"]}`))
	req.Header.Set("X-Idempotency-Key", "rest-async-1")
	req.Header.Set("X-Flow-Outbox-Mode", "async")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("async enqueue status=%d body=%s", w.Code, w.Body.String())
	}
	if fe.calls["POST /v1/collection-runs"] != 0 {
		t.Fatalf("async mode must not call engine synchronously, calls=%d", fe.calls["POST /v1/collection-runs"])
	}
	var env struct {
		Data struct {
			OperationID string `json:"operation_id"`
			OutboxID    string `json:"outbox_id"`
			Status      string `json:"status"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	if env.Data.OperationID == "" || env.Data.OutboxID == "" || env.Data.Status != "pending" {
		t.Fatalf("unexpected async response: %s", w.Body.String())
	}

	// Background dispatcher delivers the queued command to Engine.
	d := CommandDispatcher{DB: db, Engine: client}
	if n, err := d.DispatchDue(context.Background(), 10); err != nil || n != 1 {
		t.Fatalf("DispatchDue: n=%d err=%v", n, err)
	}
	if fe.calls["POST /v1/collection-runs"] != 1 {
		t.Fatalf("expected engine call after DispatchDue, calls=%d", fe.calls["POST /v1/collection-runs"])
	}

	// 2. Reusing the same idempotency key with different payload returns 409 Conflict before hitting Engine.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/intelligence/collection-runs", strings.NewReader(`{"source_ids":["src_other"]}`))
	req2.Header.Set("X-Idempotency-Key", "rest-async-1")
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusConflict || fe.calls["POST /v1/collection-runs"] != 1 {
		t.Fatalf("conflict check failed: status=%d calls=%d", w2.Code, fe.calls["POST /v1/collection-runs"])
	}
}
