package flow

import (
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/model"
)

// Waits implements waiting_external auto-continue: an agent run parks on an
// engine operation with a checkpoint; the (deduplicated) completion event marks
// it ready; ResumeReady re-authorizes the grant and resumes it exactly once.
type Waits struct{ DB *gorm.DB }

// Park is idempotent per operation.
func (w Waits) Park(row model.FlowExternalWait) (model.FlowExternalWait, error) {
	if row.OperationID == "" || row.RunID == "" {
		return row, errors.New("flow: wait needs operation and run")
	}
	row.ID, row.Status, row.CreatedAt = NewID("wx_"), "waiting", time.Now()
	if err := w.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
		return row, err
	}
	var out model.FlowExternalWait
	err := w.DB.First(&out, "operation_id = ?", row.OperationID).Error
	return out, err
}

// markReady is called inside ConsumeEvent's transaction for terminal events.
func markWaitsReady(tx *gorm.DB, tenantID, operationID, outcome string) error {
	return tx.Model(&model.FlowExternalWait{}).Where("tenant_id = ? AND operation_id = ? AND status = 'waiting'", tenantID, operationID).
		Updates(map[string]any{"status": "ready", "outcome": outcome}).Error
}

// ResumeReady resumes ready waits. Waits whose operation already finished
// before parking (event consumed earlier, or a restart in between) are
// promoted from the projection. Each wait is claimed with a conditional
// update, so concurrent workers and repeated calls resume it once.
func (w Waits) ResumeReady(resume func(model.FlowExternalWait) (string, error)) (int, error) {
	w.DB.Exec(`UPDATE flow_external_waits SET status = 'ready', outcome = (SELECT p.status FROM flow_operation_projections p WHERE p.operation_id = flow_external_waits.operation_id)
		WHERE status = 'waiting' AND EXISTS (SELECT 1 FROM flow_operation_projections p WHERE p.operation_id = flow_external_waits.operation_id AND p.tenant_id = flow_external_waits.tenant_id AND p.status IN ('succeeded','failed','cancelled'))`)
	var rows []model.FlowExternalWait
	if err := w.DB.Where("status = 'ready'").Order("created_at").Limit(50).Find(&rows).Error; err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		claim := w.DB.Model(&model.FlowExternalWait{}).Where("id = ? AND status = 'ready'", r.ID).Update("status", "resuming")
		if claim.Error != nil || claim.RowsAffected != 1 {
			continue
		}
		// Grant re-check runs RLS-scoped to the wait's tenant; the continuation
		// run itself records/executes through UserTx (tenant-scoped as well).
		if err := TenantTx(w.DB, r.TenantID, func(tx *gorm.DB) error {
			return AuthorizeGrant(tx, "user:"+r.UserID, r.TenantID, r.WorkspaceID, "intel.collect")
		}); err != nil {
			w.DB.Model(&model.FlowExternalWait{}).Where("id = ?", r.ID).Update("status", "denied")
			continue
		}
		runID, err := resume(r)
		if err != nil {
			w.DB.Model(&model.FlowExternalWait{}).Where("id = ? AND status = 'resuming'", r.ID).Update("status", "ready")
			continue
		}
		now := time.Now()
		w.DB.Model(&model.FlowExternalWait{}).Where("id = ?", r.ID).Updates(map[string]any{"status": "resumed", "resume_run_id": runID, "resumed_at": now})
		n++
	}
	return n, nil
}
