package flow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

type scriptedGW struct {
	mu    sync.Mutex
	errs  []error
	calls int32
	keys  []string
}

func (g *scriptedGW) Complete(ctx context.Context, _, _ string, _ int) (string, int64, error) {
	atomic.AddInt32(&g.calls, 1)
	g.mu.Lock()
	defer g.mu.Unlock()
	k, _ := ctx.Value(modelIdemCtx{}).(string)
	g.keys = append(g.keys, k)
	if len(g.errs) > 0 {
		e := g.errs[0]
		g.errs = g.errs[1:]
		if e != nil {
			return "", 0, e
		}
	}
	return `{"body":"ok","claims":[]}`, 700, nil
}

func taskSetup(t *testing.T) (ModelTasks, *scriptedGW, string) {
	db := testDB(t)
	fund(t, Billing{DB: db}, "u_alice", 5_000_000)
	gw := &scriptedGW{}
	return ModelTasks{DB: db, GW: gw, Backoff: time.Millisecond}, gw, model.PersonalTenantID("u_alice")
}

func TestModelTaskRetriesWithBackoffThenSettlesOnce(t *testing.T) {
	m, gw, tenant := taskSetup(t)
	gw.errs = []error{ErrModelRetryable, ErrModelRetryable, nil}
	in := ModelTaskInput{TenantID: tenant, Key: "k1", Purpose: "brief", User: "u", BudgetMicro: 100_000}
	t1, err := m.Enqueue(in)
	if err != nil {
		t.Fatal(err)
	}
	if t2, _ := m.Enqueue(in); t2.ID != t1.ID {
		t.Fatal("enqueue not idempotent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done, err := m.Wait(ctx, t1.ID)
	if err != nil || done.Status != "succeeded" || done.Attempts != 3 || done.CostMicro != 700 {
		t.Fatalf("%+v %v", done, err)
	}
	for _, k := range gw.keys {
		if k != "k1" {
			t.Fatalf("idempotency key %q", k)
		}
	}
	var r model.FlowBudgetReservation
	m.DB.First(&r, "id = ?", done.ReservationID)
	if r.Status != "settled" || r.ConsumedMicro != 700 {
		t.Fatalf("reservation %+v", r)
	}
	var consumes int64
	m.DB.Model(&model.CreditLedgerEntry{}).Where("type = ?", model.CreditLedgerConsume).Count(&consumes)
	if consumes != 1 {
		t.Fatalf("charged %d times", consumes)
	}
}

func TestModelTaskConcurrentClaimExecutesOnce(t *testing.T) {
	m, gw, tenant := taskSetup(t)
	task, _ := m.Enqueue(ModelTaskInput{TenantID: tenant, Key: "k2", Purpose: "brief", BudgetMicro: 100_000})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = m.RunTask(context.Background(), task.ID) }()
	}
	wg.Wait()
	if gw.calls != 1 {
		t.Fatalf("executed %d times", gw.calls)
	}
}

func TestModelTaskUnknownReconciledRequeuedAndLeaseExpiry(t *testing.T) {
	m, gw, tenant := taskSetup(t)
	gw.errs = []error{ErrModelUnknownOutcome}
	task, _ := m.Enqueue(ModelTaskInput{TenantID: tenant, Key: "k3", Purpose: "brief", BudgetMicro: 100_000})
	if _, err := m.Wait(context.Background(), task.ID); !errors.Is(err, ErrModelUnknownOutcome) {
		t.Fatal(err)
	}
	var r model.FlowBudgetReservation
	m.DB.First(&r, "id = ?", task.ReservationID)
	if r.Status != "unknown" {
		t.Fatalf("reservation %s", r.Status)
	}
	answers := map[string]string{"k3": "not_found", "k4": "succeeded"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": answers[r.URL.Query().Get("idempotency_key")], "total_tokens": 300, "content": `{"body":"late"}`})
	}))
	defer srv.Close()
	m.Status = ModelStatus{URL: srv.URL, MicroPer1KTok: 1000}
	if n, _ := m.Reconcile(context.Background(), 0); n != 1 {
		t.Fatal("not reconciled")
	}
	if _, err := m.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	done, _ := m.Get(task.ID)
	r = model.FlowBudgetReservation{}
	m.DB.First(&r, "id = ?", task.ReservationID)
	if done.Status != "succeeded" || gw.calls != 2 || r.Status != "settled" {
		t.Fatalf("after requeue %+v calls=%d", done, gw.calls)
	}
	// Worker crash: lease expires while running → unknown → status says succeeded → settled with content.
	t4, _ := m.Enqueue(ModelTaskInput{TenantID: tenant, Key: "k4", Purpose: "brief", BudgetMicro: 100_000})
	past := time.Now().Add(-time.Minute)
	m.DB.Model(&model.FlowModelTask{}).Where("id = ?", t4.ID).Updates(map[string]any{"status": "running", "lease_until": past, "attempts": 1})
	_, _ = m.RunDue(context.Background())
	if g, _ := m.Get(t4.ID); g.Status != "unknown" {
		t.Fatalf("expired lease %s", g.Status)
	}
	_, _ = m.Reconcile(context.Background(), 0)
	g, _ := m.Get(t4.ID)
	r = model.FlowBudgetReservation{}
	m.DB.First(&r, "id = ?", t4.ReservationID)
	if g.Status != "succeeded" || g.Result != `{"body":"late"}` || r.Status != "settled" || r.ConsumedMicro != 300 || gw.calls != 2 {
		t.Fatalf("lease reconcile %+v %+v calls=%d", g, r, gw.calls)
	}
}
