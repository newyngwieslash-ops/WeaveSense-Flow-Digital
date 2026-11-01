package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrCommandConflict indicates an idempotency key was reused with different command parameters (PRD §11.1 / §11.3 → 409).
var ErrCommandConflict = errors.New("flow: command idempotency key reused with different parameters")

// CommandInput describes a manual platform → Engine command to persist in the outbox and dispatch.
type CommandInput struct {
	Scope              Scope
	Actor              Actor
	Command            string // collection.request | research.request
	Method             string
	Path               string
	IdempotencyKey     string
	Payload            []byte
	ReserveMicro       int64
	CapabilitySnapshot string
	MaxAttempts        int
}

// CommandDispatcher manages the platform-side command outbox (PRD §11.3):
// manual commands are committed in the same platform DB transaction as the initial
// operation projection and optional budget reservation, then dispatched idempotently
// to Loom Engine with bounded retries and reconciliation protection.
type CommandDispatcher struct {
	Scanner  *BackgroundScanner
	tenantID string // set only by fixed background tenant discovery
	DB       *gorm.DB
	Engine   EngineCaller
	Adapters map[string]CommandAdapter
	Now      func() time.Time
	Lease    time.Duration
	Backoff  time.Duration
}

func (d CommandDispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

func (d CommandDispatcher) lease() time.Duration {
	if d.Lease > 0 {
		return d.Lease
	}
	return 30 * time.Second
}

func (d CommandDispatcher) backoff() time.Duration {
	if d.Backoff > 0 {
		return d.Backoff
	}
	return 2 * time.Second
}

func commandRequestHash(command, workspaceID, method, path string, payload []byte) string {
	var canonical []byte
	var obj any
	if len(payload) > 0 && json.Unmarshal(payload, &obj) == nil {
		canonical, _ = json.Marshal(obj)
	} else {
		canonical = payload
	}
	h := sha256.Sum256([]byte(command + "\x00" + workspaceID + "\x00" + method + "\x00" + path + "\x00" + string(canonical)))
	return hex.EncodeToString(h[:])
}

// Enqueue persists a command in flow_command_outbox in the same transaction as its
// initial FlowOperationProjection (and optional FlowBudgetReservation).
// If (tenant_id, idempotency_key) already exists with the same request hash, it returns the
// existing row and replayed=true; if the hash differs, it returns ErrCommandConflict.
func (d CommandDispatcher) Enqueue(ctx context.Context, in CommandInput) (model.FlowCommandOutbox, bool, error) {
	var out model.FlowCommandOutbox
	if d.DB == nil {
		return out, false, errors.New("flow: command outbox db not configured")
	}
	if adapterRequired(in.Command) && d.adapter(in.Command) == nil {
		return out, false, ErrCommandAdapterUnavailable
	}
	if d.adapter(in.Command) != nil && (in.ReserveMicro != 0 || in.Actor.Type != "user" || in.Actor.ID == "" || in.Scope.TenantID == "" || in.Scope.WorkspaceID == "" || strings.TrimSpace(in.IdempotencyKey) == "") {
		return out, false, errors.New("flow: invalid adapter command input")
	}
	if d.adapter(in.Command) != nil {
		return d.enqueueAdapter(ctx, in)
	}
	now := d.now()
	hash := commandRequestHash(in.Command, in.Scope.WorkspaceID, in.Method, in.Path, in.Payload)
	rawHeader := strings.TrimSpace(in.IdempotencyKey)
	cmdID := NewID("cmd_")
	opID := NewID("op_")
	storeKey := rawHeader
	if storeKey == "" {
		storeKey = "auto:" + cmdID
	}
	maxAttempts := in.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	capSnap := in.CapabilitySnapshot
	if capSnap == "" {
		capSnap = capabilityFromCtx(ctx)
	}
	replayed := false
	err := d.DB.Transaction(func(tx *gorm.DB) error {
		if rawHeader != "" {
			var prior model.FlowCommandOutbox
			if err := tx.First(&prior, "tenant_id = ? AND idempotency_key = ?", in.Scope.TenantID, rawHeader).Error; err == nil {
				if prior.RequestHash != hash {
					return ErrCommandConflict
				}
				out = prior
				replayed = true
				return nil
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		var resID string
		if in.ReserveMicro > 0 {
			rsv, err := Billing{DB: tx, Now: d.Now}.Reserve(in.Scope.TenantID, "cmd:"+storeKey, in.Command, opID, in.ReserveMicro)
			if err != nil {
				return err
			}
			resID = rsv.ID
		}
		proj := model.FlowOperationProjection{
			OperationID:   opID,
			TenantID:      in.Scope.TenantID,
			WorkspaceID:   in.Scope.WorkspaceID,
			Status:        "queued",
			Sequence:      0,
			AsOf:          now,
			SourceVersion: 0,
			SyncStatus:    "synced",
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&proj).Error; err != nil {
			return err
		}
		actorRole := in.Scope.Role
		if actorRole == "" && d.adapter(in.Command) == nil {
			actorRole = "editor"
		}
		row := model.FlowCommandOutbox{
			ID:                 cmdID,
			OperationID:        opID,
			TenantID:           in.Scope.TenantID,
			WorkspaceID:        in.Scope.WorkspaceID,
			ActorType:          in.Actor.Type,
			ActorID:            in.Actor.ID,
			ActorRole:          actorRole,
			Command:            in.Command,
			Method:             in.Method,
			Path:               in.Path,
			IdempotencyKey:     storeKey,
			ForwardIdemHeader:  rawHeader,
			RequestHash:        hash,
			Payload:            string(in.Payload),
			ReservationID:      resID,
			CapabilitySnapshot: capSnap,
			Status:             "pending",
			Attempts:           0,
			MaxAttempts:        maxAttempts,
			NextAttemptAt:      now,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		out = row
		return nil
	})
	return out, replayed, err
}

func (d CommandDispatcher) databaseWork(fn func(CommandDispatcher) error) error {
	return shortTenantWork(d.DB, d.tenantID, func(tx *gorm.DB) error { scoped := d; scoped.DB = tx; scoped.tenantID = ""; return fn(scoped) })
}

// DispatchOne claims and dispatches one outbox row by ID. If the row is already
// dispatched or permanently failed, it returns the stored state without re-calling the Engine.
func (d CommandDispatcher) DispatchOne(ctx context.Context, id string) (model.FlowCommandOutbox, error) {
	var row model.FlowCommandOutbox
	if err := d.databaseWork(func(d CommandDispatcher) error { return d.DB.First(&row, "id = ?", id).Error }); err != nil {
		return row, err
	}
	if row.Status == "dispatched" || row.Status == "failed" || row.Status == StatusReconciliation {
		return row, nil
	}
	adapter := d.adapter(row.Command)
	if adapter == nil && adapterRequired(row.Command) {
		return row, ErrCommandAdapterUnavailable
	}
	if d.Engine == nil && adapter == nil {
		return row, errors.New("flow: engine caller not configured")
	}
	now := d.now()
	until := now.Add(d.lease())
	var claimed bool
	err := d.databaseWork(func(d CommandDispatcher) error {
		query := d.DB.Model(&model.FlowCommandOutbox{}).Where("id = ? AND (status IN ? OR (status = 'dispatching' AND lease_until < ?))", id, []string{"pending", StatusRetryWait}, now)
		if adapter != nil {
			query = query.Where("next_attempt_at <= ? AND attempts < max_attempts", now)
		}
		claim := query.Updates(map[string]any{"status": "dispatching", "lease_until": until, "attempts": gorm.Expr("attempts + 1"), "updated_at": now})
		if claim.Error != nil {
			return claim.Error
		}
		claimed = claim.RowsAffected == 1
		return d.DB.First(&row, "id = ?", id).Error
	})
	if err != nil {
		return row, err
	}
	// A losing claimant must not send an already-running request a second time.
	if !claimed {
		return row, nil
	}
	if adapter != nil {
		return d.dispatchAdapter(ctx, row, adapter)
	}
	callCtx := ctx
	if row.CapabilitySnapshot != "" {
		callCtx = WithCapabilitySnapshot(callCtx, row.CapabilitySnapshot)
	}
	scope := Scope{TenantID: row.TenantID, WorkspaceID: row.WorkspaceID, Role: row.ActorRole}
	if scope.Role == "" {
		scope.Role = "editor"
	}
	actor := Actor{Type: row.ActorType, ID: row.ActorID}
	status, body, callErr := d.Engine.Do(callCtx, scope, actor, row.Method, row.Path, row.ForwardIdemHeader, []byte(row.Payload))
	var finished model.FlowCommandOutbox
	err = d.databaseWork(func(d CommandDispatcher) error {
		var err error
		finished, err = d.finishDispatch(row, status, body, callErr)
		return err
	})
	if err != nil {
		return finished, err
	}
	// A transport failure is reported only after its durable retry/unknown
	// receipt commits, never used as the database transaction rollback cause.
	if callErr != nil {
		return finished, callErr
	}
	if status <= 0 || status >= 500 {
		return finished, errors.New(extractEngineErrorMsg(body, status))
	}
	return finished, nil
}

// finishDispatch performs only database receipt/ledger updates after external I/O.
func (d CommandDispatcher) finishDispatch(row model.FlowCommandOutbox, status int, body []byte, callErr error) (model.FlowCommandOutbox, error) {
	finNow := d.now()
	switch {
	case callErr == nil && status > 0 && status < 400:
		engineOpID, jobID, projStatus := parseEngineCommandReceipt(row.OperationID, body)
		err := d.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.FlowCommandOutbox{}).Where("id = ?", row.ID).Updates(map[string]any{
				"status":              "dispatched",
				"response_status":     status,
				"response_body":       string(body),
				"engine_operation_id": engineOpID,
				"lease_until":         nil,
				"last_error":          "",
				"updated_at":          finNow,
			}).Error; err != nil {
				return err
			}
			upd := map[string]any{"status": projStatus, "as_of": finNow, "sync_status": "synced"}
			if jobID != "" {
				upd["job_id"] = jobID
			}
			if engineOpID != "" && engineOpID != row.OperationID {
				if err := tx.Where("operation_id = ? AND sequence = 0", row.OperationID).Delete(&model.FlowOperationProjection{}).Error; err != nil {
					return err
				}
				p := model.FlowOperationProjection{
					OperationID:   engineOpID,
					TenantID:      row.TenantID,
					WorkspaceID:   row.WorkspaceID,
					JobID:         jobID,
					Status:        projStatus,
					Sequence:      0,
					AsOf:          finNow,
					SourceVersion: 0,
					SyncStatus:    "synced",
				}
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&p).Error; err != nil {
					return err
				}
			} else {
				if err := tx.Model(&model.FlowOperationProjection{}).Where("operation_id = ?", row.OperationID).Updates(upd).Error; err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return row, err
		}
		if err := d.DB.First(&row, "id = ?", row.ID).Error; err != nil {
			return row, err
		}
		return row, nil

	case callErr == nil && status >= 400 && status < 500:
		// Definite 4xx rejection from Engine (including 400, 404, 409, 422, 429 child limit).
		errMsg := extractEngineErrorMsg(body, status)
		err := d.DB.Transaction(func(tx *gorm.DB) error {
			if row.ReservationID != "" {
				if _, err := (Billing{DB: tx, Now: d.Now}).Release(row.ReservationID); err != nil {
					return err
				}
			}
			if err := tx.Model(&model.FlowCommandOutbox{}).Where("id = ?", row.ID).Updates(map[string]any{
				"status":          "failed",
				"response_status": status,
				"response_body":   string(body),
				"lease_until":     nil,
				"last_error":      truncRunes(errMsg, 480),
				"updated_at":      finNow,
			}).Error; err != nil {
				return err
			}
			return tx.Model(&model.FlowOperationProjection{}).Where("operation_id = ? AND sequence = 0", row.OperationID).
				Updates(map[string]any{"status": "failed", "as_of": finNow}).Error
		})
		if err != nil {
			return row, err
		}
		if err := d.DB.First(&row, "id = ?", row.ID).Error; err != nil {
			return row, err
		}
		return row, nil

	default:
		// Network failure or 5xx: retry with exponential backoff up to MaxAttempts, then park for reconciliation.
		errMsg := "engine unavailable"
		if callErr != nil {
			errMsg = callErr.Error()
		} else if status >= 500 {
			errMsg = extractEngineErrorMsg(body, status)
		}
		nextStatus := StatusRetryWait
		shift := row.Attempts - 1
		if shift < 0 {
			shift = 0
		}
		if shift > 6 {
			shift = 6
		}
		nextAt := finNow.Add(d.backoff() * time.Duration(1<<shift))
		if row.Attempts >= row.MaxAttempts {
			nextStatus = StatusReconciliation
			if row.ReservationID != "" {
				if err := (Billing{DB: d.DB, Now: d.Now}).MarkUnknown(row.ReservationID, row.OperationID, errMsg); err != nil {
					return row, err
				}
			}
		}
		if err := d.DB.Model(&model.FlowCommandOutbox{}).Where("id = ?", row.ID).Updates(map[string]any{
			"status":          nextStatus,
			"response_status": status,
			"response_body":   string(body),
			"next_attempt_at": nextAt,
			"lease_until":     nil,
			"last_error":      truncRunes(errMsg, 480),
			"updated_at":      finNow,
		}).Error; err != nil {
			return row, err
		}
		if nextStatus == StatusReconciliation {
			if err := d.DB.Model(&model.FlowOperationProjection{}).Where("operation_id = ? AND sequence = 0", row.OperationID).
				Updates(map[string]any{"status": StatusReconciliation, "as_of": finNow}).Error; err != nil {
				return row, err
			}
		}
		if err := d.DB.First(&row, "id = ?", row.ID).Error; err != nil {
			return row, err
		}
		return row, nil
	}
}

// EnqueueAndDispatch atomically enqueues a command in the platform outbox and
// immediately attempts the first dispatch step. Replayed commands that were already
// dispatched return their stored response without calling the Engine again.
func (d CommandDispatcher) EnqueueAndDispatch(ctx context.Context, in CommandInput) (model.FlowCommandOutbox, bool, error) {
	row, replayed, err := d.Enqueue(ctx, in)
	if err != nil {
		return row, false, err
	}
	if replayed && (row.Status == "dispatched" || row.Status == "failed") {
		return row, true, nil
	}
	dispatched, derr := d.DispatchOne(ctx, row.ID)
	return dispatched, replayed, derr
}

// DispatchDue claims and dispatches due pending / retry_wait outbox rows.
// Expired dispatching leases are moved to reconciliation_required (PRD §11.3 no blind resend after lost ACK).
func (d CommandDispatcher) DispatchDue(ctx context.Context, limit int) (int, error) {
	if d.DB == nil || (d.Engine == nil && len(d.Adapters) == 0) {
		return 0, nil
	}
	if d.DB.Dialector.Name() == "postgres" && d.tenantID == "" {
		tenants, err := d.Scanner.Next(ctx, ScanCommands)
		if err != nil {
			return 0, err
		}
		n := 0
		for _, tenant := range tenants {
			if err := ctx.Err(); err != nil {
				return n, err
			}
			child := d
			child.tenantID = tenant
			count, err := child.DispatchDue(ctx, limit)
			n += count
			if err != nil {
				return n, err
			}
		}
		return n, nil
	}
	if limit <= 0 {
		limit = 25
	}
	now := d.now()
	var due []model.FlowCommandOutbox
	err := d.databaseWork(func(d CommandDispatcher) error {
		// Domain adapters guarantee business-idempotent recovery using the same
		// persisted operation. Expired attempts are fenced by Attempts/status;
		// the legacy Loom lost-ACK policy below remains unchanged.
		for command, adapter := range d.Adapters {
			if adapter == nil {
				continue
			}
			if err := d.DB.Model(&model.FlowCommandOutbox{}).Where("command=? AND status='dispatching' AND lease_until IS NOT NULL AND lease_until < ? AND attempts < max_attempts", command, now).Updates(map[string]any{"status": StatusRetryWait, "lease_until": nil, "next_attempt_at": now, "last_error": "adapter lease expired; resume original operation", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if err := d.DB.Model(&model.FlowCommandOutbox{}).Where("status = 'dispatching' AND lease_until IS NOT NULL AND lease_until < ?", now).Updates(map[string]any{"status": StatusReconciliation, "lease_until": nil, "last_error": "dispatch lease expired; outcome unknown", "updated_at": now}).Error; err != nil {
			return err
		}
		return d.DB.Where("status IN ? AND next_attempt_at <= ?", []string{"pending", StatusRetryWait}, now).Order("next_attempt_at ASC, created_at ASC").Limit(limit).Find(&due).Error
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range due {
		if ctx.Err() != nil {
			break
		}
		updated, err := d.DispatchOne(ctx, r.ID)
		if err == nil && updated.Status == "dispatched" {
			n++
		}
	}
	return n, nil
}

// RunCommandDispatcher periodically dispatches due platform command outbox rows.
func RunCommandDispatcher(ctx context.Context, d CommandDispatcher, every time.Duration) {
	if every <= 0 {
		every = 2 * time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = d.DispatchDue(ctx, 25)
		}
	}
}

func parseEngineCommandReceipt(defaultOpID string, body []byte) (engineOpID, jobID, projStatus string) {
	projStatus = "running"
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	raw := body
	if json.Unmarshal(body, &env) == nil && len(env.Data) > 0 {
		raw = env.Data
	}
	var d struct {
		OperationID string   `json:"operation_id"`
		ID          string   `json:"id"`
		JobID       string   `json:"job_id"`
		JobIDs      []string `json:"job_ids"`
		Status      string   `json:"status"`
	}
	if json.Unmarshal(raw, &d) == nil {
		if d.OperationID != "" {
			engineOpID = d.OperationID
		} else if d.ID != "" {
			engineOpID = d.ID
		}
		if d.JobID != "" {
			jobID = d.JobID
		} else if len(d.JobIDs) > 0 {
			jobID = d.JobIDs[0]
		}
		if d.Status == "succeeded" || d.Status == "failed" || d.Status == "cancelled" {
			projStatus = d.Status
		}
	}
	if engineOpID == "" {
		engineOpID = defaultOpID
	}
	return engineOpID, jobID, projStatus
}

func extractEngineErrorMsg(body []byte, status int) string {
	var env struct {
		Msg string `json:"msg"`
	}
	if json.Unmarshal(body, &env) == nil && env.Msg != "" {
		return env.Msg
	}
	return http.StatusText(status)
}
