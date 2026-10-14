package flow

import (
	"errors"
	"fmt"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Budget reservations in the platform credit ledger.
//
//   Reserve (root): available → reserved on the payer's CreditAccount, ledger "reserve".
//   Allocate (child): carved atomically from the parent's unallocated remainder; no ledger move.
//   Settle: child → consumed rolls up to the parent; root → ledger "consume" for the
//           actual cost and "refund" for the remainder.
//   MarkUnknown: an external call's outcome is unknown (e.g. lost ACK) → reconciliation_required. Credits stay held
//           until Reconcile resolves it; nothing is retried blindly.

var (
	ErrBudgetInsufficient = errors.New("flow: insufficient credits for reservation")
	ErrBudgetQuota        = errors.New("flow: tenant quota exceeded")
	ErrBudgetParent       = errors.New("flow: parent reservation cannot cover child budget")
	ErrBudgetState        = errors.New("flow: reservation is not open")
	ErrBudgetNotFound     = errors.New("flow: reservation not found")
)

type Billing struct {
	DB  *gorm.DB
	Now func() time.Time
}

func (b Billing) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now().UTC()
}

// Account returns (creating if needed) the tenant's billing account.
func (b Billing) Account(tx *gorm.DB, tenantID string) (model.FlowBillingAccount, error) {
	var a model.FlowBillingAccount
	if err := tx.First(&a, "tenant_id = ?", tenantID).Error; err == nil {
		return a, nil
	}
	var t model.Tenant
	if err := tx.First(&t, "id = ?", tenantID).Error; err != nil {
		return a, fmt.Errorf("flow: tenant %s: %w", tenantID, err)
	}
	kind := "personal"
	if t.Kind == "organization" {
		kind = "shared"
	}
	a = model.FlowBillingAccount{ID: "ba_" + tenantID, TenantID: tenantID, PayerUserID: t.OwnerUserID, Kind: kind, CreatedAt: b.now(), UpdatedAt: b.now()}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&a).Error; err != nil {
		return a, err
	}
	return a, tx.First(&a, "tenant_id = ?", tenantID).Error
}

func (b Billing) ledger(tx *gorm.DB, userID string, typ model.CreditLedgerType, amount, availDelta, resDelta int64, ref, note string) error {
	var acc model.CreditAccount
	if err := tx.First(&acc, "user_id = ?", userID).Error; err != nil {
		return err
	}
	return tx.Create(&model.CreditLedgerEntry{ID: NewID("cl_"), UserID: userID, Type: typ, AmountMicrocredits: amount,
		AvailableDeltaMicrocredits: availDelta, ReservedDeltaMicrocredits: resDelta, AvailableAfterMicrocredits: acc.AvailableMicrocredits,
		ReservedAfterMicrocredits: acc.ReservedMicrocredits, Scene: "flow", Note: note, ReferenceKey: &ref, CreatedAt: b.now()}).Error
}

// Reserve opens a root reservation for the tenant (idempotent by key).
func (b Billing) Reserve(tenantID, key, purpose, refID string, amount int64) (model.FlowBudgetReservation, error) {
	var out model.FlowBudgetReservation
	if amount <= 0 || key == "" {
		return out, fmt.Errorf("flow: invalid reservation")
	}
	err := b.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&out, "tenant_id = ? AND idempotency_key = ?", tenantID, key).Error; err == nil {
			return nil
		}
		acc, err := b.Account(tx, tenantID)
		if err != nil {
			return err
		}
		if acc.MonthlyQuotaMicro > 0 {
			var used int64
			month := time.Date(b.now().Year(), b.now().Month(), 1, 0, 0, 0, 0, time.UTC)
			tx.Model(&model.FlowBudgetReservation{}).Where("account_id = ? AND parent_id = '' AND created_at >= ? AND status <> ?", acc.ID, month, "released").
				Select("COALESCE(SUM(CASE WHEN status = 'settled' THEN consumed_micro ELSE reserved_micro END), 0)").Scan(&used)
			if used+amount > acc.MonthlyQuotaMicro {
				return ErrBudgetQuota
			}
		}
		if acc.MaxOpenReserves > 0 {
			var open int64
			tx.Model(&model.FlowBudgetReservation{}).Where("account_id = ? AND parent_id = '' AND status IN ?", acc.ID, []string{"reserved", StatusReconciliation}).Count(&open)
			if int(open) >= acc.MaxOpenReserves {
				return ErrBudgetQuota
			}
		}
		// Conditional update = atomic check-and-hold on the payer's balance.
		res := tx.Model(&model.CreditAccount{}).Where("user_id = ? AND available_microcredits >= ?", acc.PayerUserID, amount).
			Updates(map[string]any{"available_microcredits": gorm.Expr("available_microcredits - ?", amount),
				"reserved_microcredits": gorm.Expr("reserved_microcredits + ?", amount), "version": gorm.Expr("version + 1"), "updated_at": b.now()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrBudgetInsufficient
		}
		out = model.FlowBudgetReservation{ID: NewID("rsv_"), TenantID: tenantID, AccountID: acc.ID, Purpose: purpose, RefID: refID, IdempotencyKey: key, PriceVersion: CurrentPriceBook().Version,
			ReservedMicro: amount, Status: "reserved", CreatedAt: b.now(), UpdatedAt: b.now()}
		if err := tx.Create(&out).Error; err != nil {
			return err
		}
		return b.ledger(tx, acc.PayerUserID, model.CreditLedgerReserve, amount, -amount, amount, "flow:reserve:"+out.ID, purpose+" "+refID)
	})
	return out, err
}

// Allocate carves a child reservation out of the parent's remaining budget
// atomically (conditional UPDATE), idempotent by key.
func (b Billing) Allocate(parentID, key, purpose, refID string, amount int64) (model.FlowBudgetReservation, error) {
	var out model.FlowBudgetReservation
	err := b.DB.Transaction(func(tx *gorm.DB) error {
		var p model.FlowBudgetReservation
		if err := tx.First(&p, "id = ?", parentID).Error; err != nil {
			return ErrBudgetNotFound
		}
		if err := tx.First(&out, "tenant_id = ? AND idempotency_key = ?", p.TenantID, key).Error; err == nil {
			return nil
		}
		res := tx.Model(&model.FlowBudgetReservation{}).
			Where("id = ? AND status = ? AND reserved_micro - allocated_micro - consumed_micro >= ?", parentID, "reserved", amount).
			Updates(map[string]any{"allocated_micro": gorm.Expr("allocated_micro + ?", amount), "updated_at": b.now()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 || amount <= 0 {
			return ErrBudgetParent
		}
		out = model.FlowBudgetReservation{ID: NewID("rsv_"), TenantID: p.TenantID, AccountID: p.AccountID, ParentID: p.ID, PriceVersion: p.PriceVersion, Purpose: purpose, RefID: refID,
			IdempotencyKey: key, ReservedMicro: amount, Status: "reserved", CreatedAt: b.now(), UpdatedAt: b.now()}
		return tx.Create(&out).Error
	})
	return out, err
}

// Settle closes an open (or unknown) reservation with the actual cost (capped at the reservation).
func (b Billing) Settle(id string, actual int64) (model.FlowBudgetReservation, error) {
	return b.close(id, actual, "settled")
}

// Release closes a reservation with zero cost (failure known, nothing billed).
func (b Billing) Release(id string) (model.FlowBudgetReservation, error) {
	return b.close(id, 0, "released")
}

func (b Billing) close(id string, actual int64, status string) (model.FlowBudgetReservation, error) {
	var r model.FlowBudgetReservation
	err := b.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.First(&r, "id = ?", id).Error; err != nil {
			return ErrBudgetNotFound
		}
		if r.Status == "settled" || r.Status == "released" {
			return nil // idempotent
		}
		if r.AllocatedMicro > 0 {
			return fmt.Errorf("%w: open children", ErrBudgetState)
		}
		if actual < 0 {
			actual = 0
		}
		total := r.ConsumedMicro + actual
		if total > r.ReservedMicro {
			total = r.ReservedMicro
		}
		now := b.now()
		upd := tx.Model(&model.FlowBudgetReservation{}).Where("id = ? AND status IN ?", r.ID, []string{"reserved", StatusReconciliation}).
			Updates(map[string]any{"status": status, "consumed_micro": total, "settled_at": now, "updated_at": now})
		if upd.Error != nil || upd.RowsAffected == 0 {
			return upd.Error
		}
		if r.ParentID != "" {
			return tx.Model(&model.FlowBudgetReservation{}).Where("id = ?", r.ParentID).Updates(map[string]any{
				"allocated_micro": gorm.Expr("allocated_micro - ?", r.ReservedMicro), "consumed_micro": gorm.Expr("consumed_micro + ?", total), "updated_at": now}).Error
		}
		var acc model.FlowBillingAccount
		if err := tx.First(&acc, "id = ?", r.AccountID).Error; err != nil {
			return err
		}
		refund := r.ReservedMicro - total
		if err := tx.Model(&model.CreditAccount{}).Where("user_id = ?", acc.PayerUserID).Updates(map[string]any{
			"reserved_microcredits": gorm.Expr("reserved_microcredits - ?", r.ReservedMicro), "available_microcredits": gorm.Expr("available_microcredits + ?", refund),
			"version": gorm.Expr("version + 1"), "updated_at": now}).Error; err != nil {
			return err
		}
		if total > 0 {
			if err := b.ledger(tx, acc.PayerUserID, model.CreditLedgerConsume, total, 0, -total, "flow:consume:"+r.ID, r.Purpose+" "+r.RefID); err != nil {
				return err
			}
		}
		if refund > 0 {
			return b.ledger(tx, acc.PayerUserID, model.CreditLedgerRefund, refund, refund, -refund, "flow:refund:"+r.ID, r.Purpose+" "+r.RefID)
		}
		return nil
	})
	if err == nil {
		err = b.DB.First(&r, "id = ?", id).Error
	}
	return r, err
}

// MarkUnknown records that an external effect may or may not have happened.
// Credits stay held; the reservation must be resolved by Reconcile.
func (b Billing) MarkUnknown(id, externalRef, note string) error {
	res := b.DB.Model(&model.FlowBudgetReservation{}).Where("id = ? AND status = ?", id, "reserved").
		Updates(map[string]any{"status": StatusReconciliation, "external_ref": externalRef, "note": note, "updated_at": b.now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrBudgetState
	}
	return nil
}

// Outcome is what a reconciler learned by querying the external system.
type Outcome struct {
	Known     bool
	Succeeded bool
	Cost      int64
}

// Reconcile resolves unknown reservations older than minAge by asking query()
// (an idempotent status lookup, never a retry). Unresolved ones stay unknown.
func (b Billing) Reconcile(minAge time.Duration, query func(model.FlowBudgetReservation) (Outcome, error)) (resolved int, err error) {
	var rows []model.FlowBudgetReservation
	if err := b.DB.Where("status = ? AND updated_at <= ?", StatusReconciliation, b.now().Add(-minAge)).Order("updated_at").Limit(200).Find(&rows).Error; err != nil {
		return 0, err
	}
	for _, r := range rows {
		o, qerr := query(r)
		if qerr != nil || !o.Known {
			continue
		}
		if o.Succeeded {
			_, err = b.Settle(r.ID, o.Cost)
		} else {
			_, err = b.Release(r.ID)
		}
		if err != nil {
			return resolved, err
		}
		resolved++
	}
	return resolved, nil
}

// SweepStale settles root agent-run reservations left open (failed/cancelled
// runs) after maxAge; unknown ones are left for Reconcile.
func (b Billing) SweepStale(maxAge time.Duration) (int, error) {
	var rows []model.FlowBudgetReservation
	if err := b.DB.Where("status = ? AND parent_id = '' AND purpose = ? AND allocated_micro = 0 AND updated_at <= ?", "reserved", "agent_run", b.now().Add(-maxAge)).Limit(200).Find(&rows).Error; err != nil {
		return 0, err
	}
	for _, r := range rows {
		if _, err := b.Settle(r.ID, 0); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// Reopen returns an unknown reservation to reserved when reconciliation proved
// the external call never happened and it will be retried under the same key.
func (b Billing) Reopen(id string) error {
	res := b.DB.Model(&model.FlowBudgetReservation{}).Where("id = ? AND status = ?", id, StatusReconciliation).
		Updates(map[string]any{"status": "reserved", "updated_at": b.now()})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrBudgetState
	}
	return nil
}
