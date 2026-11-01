package flow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type CommandOutcomeState string

const (
	CommandCompleted      CommandOutcomeState = "completed"
	CommandPending        CommandOutcomeState = "retryable_pending"
	CommandRejected       CommandOutcomeState = "rejected"
	CommandUnknown        CommandOutcomeState = "unknown"
	CommandReconciliation CommandOutcomeState = "reconciliation_required"
)

// Receipt is an adapter-private, verified in-memory value, never serialized by
// the dispatcher. ResponseBody must contain only the adapter's safe public DTO.
type CommandOutcome struct {
	State        CommandOutcomeState
	HTTPStatus   int
	ResponseBody []byte
	Receipt      any
}

// Execute performs external I/O outside a transaction. ApplyReceipt performs
// only local database work and rechecks current domain authority. It is invoked
// inside the same fenced transaction as the outbox and operation receipt.
type CommandAdapter interface {
	Execute(context.Context, model.FlowCommandOutbox) (CommandOutcome, error)
	ApplyReceipt(context.Context, *gorm.DB, model.FlowCommandOutbox, CommandOutcome) error
}

var (
	ErrCommandLeaseLost          = errors.New("flow: command dispatch lease lost")
	ErrCommandAdapterUnavailable = errors.New("flow: command adapter unavailable")
)

func (d CommandDispatcher) adapter(command string) CommandAdapter { return d.Adapters[command] }

// ForTenant binds a server-verified tenant for short database phases. Use the
// runtime connection pool, not a live request transaction, before DispatchOne.
// It is not an authorization check; only a trusted composition may call it.
func (d CommandDispatcher) ForTenant(tenantID string) CommandDispatcher {
	d.tenantID = tenantID
	return d
}

// Reserved domain commands must not fall through to the legacy Loom caller.
func adapterRequired(command string) bool { return strings.HasPrefix(command, "agri.") }

// Insert the outbox first with a conflict-safe statement. A concurrent replay
// must not create an orphan projection or abort a PostgreSQL outer transaction.
func (d CommandDispatcher) enqueueAdapter(ctx context.Context, in CommandInput) (model.FlowCommandOutbox, bool, error) {
	now := d.now()
	key := strings.TrimSpace(in.IdempotencyKey)
	maxAttempts := in.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	hash := commandRequestHash(in.Command, in.Scope.WorkspaceID, in.Method, in.Path, in.Payload)
	row := model.FlowCommandOutbox{ID: NewID("cmd_"), OperationID: NewID("op_"), TenantID: in.Scope.TenantID, WorkspaceID: in.Scope.WorkspaceID, ActorType: in.Actor.Type, ActorID: in.Actor.ID, Command: in.Command, Method: in.Method, Path: in.Path, IdempotencyKey: key, ForwardIdemHeader: key, RequestHash: hash, Payload: string(in.Payload), Status: "pending", MaxAttempts: maxAttempts, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}
	replayed := false
	err := d.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "tenant_id"}, {Name: "idempotency_key"}}, DoNothing: true}).Create(&row)
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			row = model.FlowCommandOutbox{}
			if err := tx.Where("tenant_id=? AND idempotency_key=?", in.Scope.TenantID, key).First(&row).Error; err != nil {
				return err
			}
			if row.RequestHash != hash || row.ActorType != in.Actor.Type || row.ActorID != in.Actor.ID || row.WorkspaceID != in.Scope.WorkspaceID || row.Command != in.Command {
				return ErrCommandConflict
			}
			replayed = true
			return nil
		}
		return tx.Create(&model.FlowOperationProjection{OperationID: row.OperationID, TenantID: row.TenantID, WorkspaceID: row.WorkspaceID, Status: "queued", AsOf: now, SyncStatus: "synced"}).Error
	})
	return row, replayed, err
}

func (d CommandDispatcher) dispatchAdapter(ctx context.Context, row model.FlowCommandOutbox, adapter CommandAdapter) (model.FlowCommandOutbox, error) {
	outcome, callErr := adapter.Execute(ctx, row)
	if callErr != nil {
		// Never persist exception text, arbitrary upstream bytes, or partially
		// verified receipts from a failed external operation.
		outcome = CommandOutcome{State: CommandUnknown}
	}
	valid := outcome.HTTPStatus >= 0 && outcome.HTTPStatus <= 599 && len(outcome.ResponseBody) <= 64*1024
	valid = valid && (len(outcome.ResponseBody) == 0 || json.Valid(outcome.ResponseBody))
	switch outcome.State {
	case CommandCompleted:
		valid = valid && (outcome.HTTPStatus == 200 || outcome.HTTPStatus == 201 || outcome.HTTPStatus == 204)
	case CommandRejected:
		valid = valid && outcome.HTTPStatus >= 400 && outcome.HTTPStatus < 500
	case CommandPending, CommandUnknown, CommandReconciliation:
	default:
		valid = false
	}
	if !valid {
		outcome = CommandOutcome{State: CommandUnknown}
		callErr = errors.New("flow: invalid adapter outcome")
	}
	var finished model.FlowCommandOutbox
	err := d.databaseWork(func(scoped CommandDispatcher) error {
		return scoped.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var current model.FlowCommandOutbox
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND workspace_id=?", row.ID, row.TenantID, row.WorkspaceID).First(&current).Error; err != nil {
				return err
			}
			now := d.now()
			// Attempts is the existing monotonic claim generation. A losing or
			// expired worker cannot even enter the domain receipt/grant callback.
			if current.Status != "dispatching" || current.Attempts != row.Attempts || current.LeaseUntil == nil || !now.Before(*current.LeaseUntil) || current.OperationID != row.OperationID || current.RequestHash != row.RequestHash || current.Command != row.Command || current.Payload != row.Payload {
				return ErrCommandLeaseLost
			}
			if (outcome.State == CommandPending || outcome.State == CommandUnknown) && current.Attempts >= current.MaxAttempts {
				outcome.State = CommandReconciliation
			}
			if err := adapter.ApplyReceipt(ctx, tx, current, outcome); err != nil {
				return err
			}
			// A slow local callback may cross lease expiry. Roll all of its writes
			// back instead of acknowledging a stale receipt.
			now = d.now()
			if !now.Before(*current.LeaseUntil) {
				return ErrCommandLeaseLost
			}
			status, projection := StatusRetryWait, "pending"
			switch outcome.State {
			case CommandCompleted:
				status, projection = "dispatched", "completed"
			case CommandRejected:
				status, projection = "failed", "failed"
			case CommandReconciliation:
				status, projection = StatusReconciliation, StatusReconciliation
			default:
				if current.Attempts >= current.MaxAttempts {
					status, projection = StatusReconciliation, StatusReconciliation
				}
			}
			shift := current.Attempts - 1
			if shift < 0 {
				shift = 0
			}
			if shift > 6 {
				shift = 6
			}
			next := now.Add(d.backoff() * time.Duration(1<<shift))
			update := tx.Model(&model.FlowCommandOutbox{}).Where("id=? AND tenant_id=? AND workspace_id=? AND status='dispatching' AND attempts=? AND lease_until>?", row.ID, row.TenantID, row.WorkspaceID, row.Attempts, now).Updates(map[string]any{"status": status, "response_status": outcome.HTTPStatus, "response_body": string(outcome.ResponseBody), "lease_until": nil, "next_attempt_at": next, "updated_at": now, "last_error": ""})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return ErrCommandLeaseLost
			}
			if err := tx.Model(&model.FlowOperationProjection{}).Where("operation_id=? AND tenant_id=? AND workspace_id=? AND sequence=0", row.OperationID, row.TenantID, row.WorkspaceID).Updates(map[string]any{"status": projection, "as_of": now, "sync_status": "synced"}).Error; err != nil {
				return err
			}
			return tx.First(&finished, "id=?", row.ID).Error
		})
	})
	if err != nil {
		return row, err
	}
	return finished, callErr
}
