package flow

import (
	"encoding/json"
	"errors"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Event is the Engine outbox envelope (service-contracts.md §4).
type Event struct {
	EventID        string          `json:"event_id"`
	OperationID    string          `json:"operation_id"`
	JobID          string          `json:"job_id"`
	TenantID       string          `json:"tenant_id"`
	WorkspaceID    string          `json:"workspace_id"`
	Sequence       int64           `json:"sequence"`
	Type           string          `json:"type"`
	OccurredAt     time.Time       `json:"occurred_at"`
	PayloadVersion int             `json:"payload_version"`
	TraceID        string          `json:"trace_id"`
	Payload        json.RawMessage `json:"payload"`
}

var statusByType = map[string]string{
	"collection.accepted":  "running",
	"collection.completed": "succeeded",
	"operation.failed":     "failed",
	"schedule.blocked":     "blocked",
}

// ConsumeEvent applies an event exactly once per (consumerID, event_id) and
// updates the operation projection monotonically by sequence. Returns
// applied=false for duplicates.
func ConsumeEvent(db *gorm.DB, consumerID string, e Event) (bool, error) {
	if e.EventID == "" || e.TenantID == "" || e.OperationID == "" {
		return false, errors.New("flow: event missing ids")
	}
	applied := false
	err := db.Transaction(func(tx *gorm.DB) error {
		row := model.FlowEventInbox{ConsumerID: consumerID, EventID: e.EventID, TenantID: e.TenantID,
			OperationID: e.OperationID, Type: e.Type, Payload: string(e.Payload), ReceivedAt: time.Now().UTC()}
		res := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		applied = true
		status, ok := statusByType[e.Type]
		if !ok {
			return nil
		}
		var p model.FlowOperationProjection
		err := tx.First(&p, "operation_id = ?", e.OperationID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&model.FlowOperationProjection{OperationID: e.OperationID, TenantID: e.TenantID,
				WorkspaceID: e.WorkspaceID, JobID: e.JobID, Status: status, Sequence: e.Sequence, AsOf: e.OccurredAt}).Error
		}
		if err != nil {
			return err
		}
		if p.TenantID != e.TenantID {
			return errors.New("flow: event tenant mismatch")
		}
		if e.Sequence <= p.Sequence {
			return nil
		}
		return tx.Model(&p).Updates(map[string]any{"status": status, "sequence": e.Sequence, "as_of": e.OccurredAt, "job_id": e.JobID}).Error
	})
	return applied, err
}
