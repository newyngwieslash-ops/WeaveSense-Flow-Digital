package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/model"
)

// ErrModelRetryable: the gateway refused before processing (429 / overload);
// the task is re-queued with backoff under the same idempotency key.
var ErrModelRetryable = errors.New("flow: model gateway asked to retry later")

// ModelTasks is the async Model Gateway pipeline: tasks are queued with a
// budget reservation, claimed by a worker (conditional update + lease),
// executed with Idempotency-Key = task key, retried with exponential backoff on
// retryable errors, parked as unknown when the outcome is lost, and reconciled
// through the gateway status endpoint. Settlement happens exactly once, when a
// task reaches succeeded (or released on definite failure).
type ModelTasks struct {
	DB      *gorm.DB
	GW      ModelGateway
	Status  ModelStatus
	Now     func() time.Time
	Lease   time.Duration
	Backoff time.Duration
	// Notify is called once per finished task (succeeded/failed) by the worker:
	// the push half of completion (engine brief wake-up). Parked agent runs are
	// released by NotifyDone itself.
	Notify func(model.FlowModelTask) error
}

type ModelTaskInput struct {
	TenantID, ParentResID, Key, Purpose, RefID string
	System, User                               string
	MaxTokens                                  int
	BudgetMicro                                int64
	MaxAttempts                                int
	Context                                    string
}

func (m ModelTasks) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now().UTC()
}

// ManagedProviderAccountID is the provider account model-gateway calls are held against
// (FLOW_MODEL_PROVIDER_ACCOUNT_ID, default "model-gateway"); see FlowProviderAccount.
func ManagedProviderAccountID() string {
	if v := strings.TrimSpace(os.Getenv("FLOW_MODEL_PROVIDER_ACCOUNT_ID")); v != "" {
		return v
	}
	return "model-gateway"
}

func (m ModelTasks) billing() Billing {
	return Billing{DB: m.DB, Now: m.Now, ProviderAccountID: ManagedProviderAccountID()}
}

// Enqueue is idempotent per (tenant, key): the same key returns the same task.
func (m ModelTasks) Enqueue(in ModelTaskInput) (model.FlowModelTask, error) {
	var t model.FlowModelTask
	if in.Key == "" {
		return t, ErrInvalid
	}
	b := m.billing()
	var rsv model.FlowBudgetReservation
	var err error
	if in.ParentResID != "" {
		rsv, err = b.Allocate(in.ParentResID, in.Key, in.Purpose, in.RefID, in.BudgetMicro)
	} else {
		rsv, err = b.Reserve(in.TenantID, in.Key, in.Purpose, in.RefID, in.BudgetMicro)
	}
	if err != nil {
		return t, err
	}
	if in.TenantID == "" {
		in.TenantID = rsv.TenantID
	}
	if in.MaxAttempts <= 0 {
		in.MaxAttempts = 4
	}
	req, _ := json.Marshal(map[string]any{"system": in.System, "user": in.User, "max_tokens": in.MaxTokens})
	now := m.now()
	t = model.FlowModelTask{ID: NewID("mt_"), TenantID: in.TenantID, IdemKey: in.Key, ReservationID: rsv.ID, Purpose: in.Purpose,
		Request: string(req), Context: in.Context, Status: "queued", MaxAttempts: in.MaxAttempts, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	if err := m.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&t).Error; err != nil {
		return t, err
	}
	t = model.FlowModelTask{}
	return t, m.DB.First(&t, "tenant_id = ? AND idem_key = ?", in.TenantID, in.Key).Error
}

func (m ModelTasks) Get(id string) (model.FlowModelTask, error) {
	var t model.FlowModelTask
	return t, m.DB.First(&t, "id = ?", id).Error
}

func (m ModelTasks) set(id string, fields map[string]any) {
	fields["updated_at"] = m.now()
	m.DB.Model(&model.FlowModelTask{}).Where("id = ?", id).Updates(fields)
}

// RunTask claims a due queued task and executes one attempt. It is safe to call
// concurrently from several workers/requests: only one claim wins.
func (m ModelTasks) RunTask(ctx context.Context, id string) (bool, error) {
	if m.GW == nil {
		return false, errors.New("flow: model gateway not configured")
	}
	lease := m.Lease
	if lease <= 0 {
		lease = 3 * time.Minute
	}
	now := m.now()
	until := now.Add(lease)
	claim := m.DB.Model(&model.FlowModelTask{}).Where("id = ? AND status IN ? AND next_attempt_at <= ?", id, []string{"queued", StatusRetryWait}, now).
		Updates(map[string]any{"status": "running", "lease_until": until, "attempts": gorm.Expr("attempts + 1"), "updated_at": now})
	if claim.Error != nil || claim.RowsAffected == 0 {
		return false, claim.Error
	}
	t, err := m.Get(id)
	if err != nil {
		return true, err
	}
	var rq struct {
		System    string `json:"system"`
		User      string `json:"user"`
		MaxTokens int    `json:"max_tokens"`
	}
	_ = json.Unmarshal([]byte(t.Request), &rq)
	var rsv model.FlowBudgetReservation
	m.DB.Select("price_version").First(&rsv, "id = ?", t.ReservationID)
	usage := &ModelUsage{}
	cctx, cancel := context.WithTimeout(WithModelUsage(WithModelCallMeta(context.WithValue(ctx, modelIdemCtx{}, t.IdemKey),
		ModelCallMeta{ReservationID: t.ReservationID, PriceVersion: rsv.PriceVersion, CallID: t.IdemKey}), usage), lease)
	text, cost, cerr := m.GW.Complete(cctx, rq.System, rq.User, rq.MaxTokens)
	cancel()
	b := m.billing()
	switch {
	case cerr == nil:
		if _, err := b.Settle(t.ReservationID, cost); err != nil {
			return true, err
		}
		if err := recordModelUsage(m.DB, t, *usage, cost, m.now()); err != nil {
			return true, err
		}
		m.set(id, map[string]any{"status": "succeeded", "result": text, "cost_micro": cost, "lease_until": nil, "last_error": ""})
	case errors.Is(cerr, ErrModelUnknownOutcome):
		_ = b.MarkUnknown(t.ReservationID, "model:"+t.IdemKey, truncRunes(cerr.Error(), 280))
		m.set(id, map[string]any{"status": StatusReconciliation, "lease_until": nil, "last_error": truncRunes(cerr.Error(), 480)})
	case errors.Is(cerr, ErrModelRetryable) && t.Attempts < t.MaxAttempts:
		backoff := m.Backoff
		if backoff <= 0 {
			backoff = 2 * time.Second
		}
		next := m.now().Add(backoff * time.Duration(1<<(t.Attempts-1)))
		m.set(id, map[string]any{"status": StatusRetryWait, "next_attempt_at": next, "lease_until": nil, "last_error": truncRunes(cerr.Error(), 480)})
	default:
		_, _ = b.Release(t.ReservationID)
		m.set(id, map[string]any{"status": "failed", "lease_until": nil, "last_error": truncRunes(cerr.Error(), 480)})
	}
	return true, nil
}

// RunDue executes due queued tasks and turns expired leases (worker crashed
// mid-call: the request may have been sent) into unknown.
func (m ModelTasks) RunDue(ctx context.Context) (int, error) {
	now := m.now()
	var stale []model.FlowModelTask
	m.DB.Where("status = ? AND lease_until < ?", "running", now).Limit(50).Find(&stale)
	for _, t := range stale {
		upd := m.DB.Model(&model.FlowModelTask{}).Where("id = ? AND status = ?", t.ID, "running").
			Updates(map[string]any{"status": StatusReconciliation, "lease_until": nil, "last_error": "worker lease expired", "updated_at": now})
		if upd.RowsAffected == 1 {
			_ = m.billing().MarkUnknown(t.ReservationID, "model:"+t.IdemKey, "worker lease expired")
		}
	}
	var ids []string
	if err := m.DB.Model(&model.FlowModelTask{}).Where("status IN ? AND next_attempt_at <= ?", []string{"queued", StatusRetryWait}, now).Order("next_attempt_at").Limit(20).Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		if ok, _ := m.RunTask(ctx, id); ok {
			n++
		}
	}
	return n, nil
}

// Reconcile resolves unknown tasks via the gateway status endpoint:
// succeeded → settle (with content if the gateway returns it); not_found →
// re-queue under the same key (the gateway never processed it); pending → wait.
func (m ModelTasks) Reconcile(ctx context.Context, minAge time.Duration) (int, error) {
	if m.Status.URL == "" {
		return 0, nil
	}
	var rows []model.FlowModelTask
	if err := m.DB.Where("status = ? AND updated_at <= ?", StatusReconciliation, m.now().Add(-minAge)).Limit(50).Find(&rows).Error; err != nil {
		return 0, err
	}
	b := m.billing()
	n := 0
	for _, t := range rows {
		st, err := m.Status.QueryKey(ctx, t.IdemKey)
		if err != nil {
			continue
		}
		switch st.Status {
		case "succeeded":
			cost := st.TotalTokens * m.Status.MicroPer1KTok / 1000
			if _, err := b.Settle(t.ReservationID, cost); err != nil {
				return n, err
			}
			m.set(t.ID, map[string]any{"status": "succeeded", "result": st.Content, "cost_micro": cost, "last_error": "reconciled"})
			n++
		case "not_found", "failed":
			if t.Attempts < t.MaxAttempts && b.Reopen(t.ReservationID) == nil {
				m.set(t.ID, map[string]any{"status": StatusRetryWait, "next_attempt_at": m.now(), "last_error": "reconciled: not processed, retrying"})
			} else {
				_, _ = b.Release(t.ReservationID)
				m.set(t.ID, map[string]any{"status": "failed", "last_error": "reconciled: not processed"})
			}
			n++
		}
	}
	return n, nil
}

// Wait drives one task to a terminal state within ctx: it executes due attempts
// itself (so callers work without a separate worker) and otherwise polls.
func (m ModelTasks) Wait(ctx context.Context, id string) (model.FlowModelTask, error) {
	for {
		t, err := m.Get(id)
		if err != nil {
			return t, err
		}
		switch t.Status {
		case "succeeded":
			return t, nil
		case "failed":
			return t, fmt.Errorf("model task failed: %s", t.LastError)
		case StatusReconciliation:
			return t, fmt.Errorf("%w: %s", ErrModelUnknownOutcome, t.LastError)
		case "queued", StatusRetryWait:
			if !t.NextAttemptAt.After(m.now()) {
				if _, err := m.RunTask(ctx, id); err != nil {
					return t, err
				}
				continue
			}
		}
		select {
		case <-ctx.Done():
			return t, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// RunModelTaskWorker processes the queue and reconciles unknown outcomes until ctx ends.
func RunModelTaskWorker(ctx context.Context, m ModelTasks, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := m.RunDue(ctx); err != nil {
			slog.Warn("flow: model tasks", "error", err)
		}
		_, _ = m.Reconcile(ctx, time.Minute)
		m.NotifyDone(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// RegisterModelTaskRoutes exposes task status to the owning tenant.
func RegisterModelTaskRoutes(api gin.IRouter, db *gorm.DB, user UserFunc) {
	api.GET("/intelligence/model-tasks/:id", func(c *gin.Context) {
		db := reqDB(c, db)
		u, err := user(c)
		if err != nil || u == nil {
			reply(c, http.StatusUnauthorized, nil, "unauthorized")
			return
		}
		s, err := ResolveScope(db, u.ID, c.GetHeader("X-Workspace-Id"))
		if err != nil {
			reply(c, http.StatusNotFound, nil, "workspace not found")
			return
		}
		var t model.FlowModelTask
		if err := db.First(&t, "id = ? AND tenant_id = ?", c.Param("id"), s.TenantID).Error; err != nil {
			reply(c, http.StatusNotFound, nil, "not found")
			return
		}
		reply(c, http.StatusOK, t, "ok")
	})
}

var _ = strings.TrimSpace

// NotifyDone pushes completion of finished tasks exactly once (notified_at
// claim): agent runs parked on the task (waiting_external with operation_id =
// task id) become ready for auto-continue immediately, and the Notify hook
// wakes other consumers (the engine's brief scheduler). A failed hook leaves
// the task un-notified so the next worker pass retries; consumers also keep
// their own polling fallback.
func (m ModelTasks) NotifyDone(ctx context.Context) int {
	var rows []model.FlowModelTask
	m.DB.Where("status IN ? AND notified_at IS NULL", []string{"succeeded", "failed"}).Order("updated_at").Limit(50).Find(&rows)
	n := 0
	for _, t := range rows {
		m.DB.Model(&model.FlowExternalWait{}).Where("operation_id = ? AND tenant_id = ? AND status = ?", t.ID, t.TenantID, "waiting").
			Updates(map[string]any{"status": "ready", "outcome": t.Status})
		if m.Notify != nil {
			if err := m.Notify(t); err != nil {
				slog.Warn("flow: model task notify", "task", t.ID, "error", err)
				continue
			}
		}
		now := m.now()
		if m.DB.Model(&model.FlowModelTask{}).Where("id = ? AND notified_at IS NULL", t.ID).Update("notified_at", now).RowsAffected == 1 {
			n++
		}
	}
	return n
}

// SynthesisContext is stored on synthesis tasks for asynchronous completion.
type SynthesisContext struct {
	WorkspaceID string             `json:"workspace_id,omitempty"`
	Question    string             `json:"question"`
	Evidence    []ResolvedEvidence `json:"evidence"`
	Scope       MaterialScope      `json:"scope"`
}

// ModelTaskOperation renders a model task as an operation for agent tools
// (intel_get_operation on an "mt_" id): status, and the citation-validated
// synthesis once it succeeded.
func ModelTaskOperation(db *gorm.DB, tenantID, id string) (map[string]any, error) {
	var t model.FlowModelTask
	if err := db.First(&t, "id = ? AND tenant_id = ?", id, tenantID).Error; err != nil {
		return nil, ErrNotFound
	}
	op := map[string]any{"id": t.ID, "kind": "model_task", "purpose": t.Purpose, "status": t.Status, "attempts": t.Attempts}
	switch t.Status {
	case "succeeded":
		var sc SynthesisContext
		_ = json.Unmarshal([]byte(t.Context), &sc)
		syn, err := FinishSynthesis(t, SynthesisRequest{Question: sc.Question, Evidence: sc.Evidence, Scope: sc.Scope})
		if err != nil {
			op["error"] = err.Error()
		} else {
			op["synthesis"] = syn
		}
	case "failed":
		op["error"] = t.LastError
	}
	return op, nil
}

// BriefWakeNotifier pushes a finished brief summarization task to the engine
// (POST /v1/briefs/wake with the window key) so the parked brief window is
// completed immediately instead of on the next scheduler tick.
func BriefWakeNotifier(engine EngineCaller) func(model.FlowModelTask) error {
	return func(t model.FlowModelTask) error {
		key, ok := strings.CutPrefix(t.IdemKey, "brief:")
		if t.Purpose != "brief" || !ok {
			return nil
		}
		var sc SynthesisContext
		_ = json.Unmarshal([]byte(t.Context), &sc)
		body, _ := json.Marshal(map[string]any{"key": key, "task_id": t.ID, "status": t.Status})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		status, _, err := engine.Do(ctx, Scope{TenantID: t.TenantID, WorkspaceID: sc.WorkspaceID, Role: "editor"},
			Actor{Type: "service_principal", ID: "flow-platform"}, http.MethodPost, "/v1/briefs/wake", "", body)
		if err != nil {
			return err
		}
		if status >= 500 {
			return fmt.Errorf("engine wake returned %d", status)
		}
		return nil // 4xx (e.g. schedule gone): nothing to wake, do not retry
	}
}
