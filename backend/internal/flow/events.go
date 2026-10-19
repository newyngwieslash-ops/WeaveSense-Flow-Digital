package flow

import (
	"encoding/json"
	"errors"
	"strings"
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
	// §11.1 cancel/retry transitions so a cancelled task no longer shows "running".
	"operation.cancelled":        "cancelled",
	"operation.cancel_requested": "cancel_requested",
	"operation.retrying":         "retry_wait",
	"schedule.blocked":           "blocked",
	"research.completed":         "succeeded", // payload.status=failed overrides
}

// ProjectionStaleAfter: a non-terminal projection not updated for this long is reported sync_status=stale.
const ProjectionStaleAfter = 15 * time.Minute

// ProjectionSyncStatus reports whether a projection row can be trusted as current (PRD §12.1).
func ProjectionSyncStatus(p model.FlowOperationProjection, now time.Time) string {
	if (p.Status == "running" || p.Status == "queued" || p.Status == "blocked" || p.Status == "retry_wait" || p.Status == "cancel_requested") && now.Sub(p.AsOf) > ProjectionStaleAfter {
		return "stale"
	}
	if p.SyncStatus == "" {
		return "synced"
	}
	return p.SyncStatus
}

// PRDOperationStatus maps internal operation/job statuses onto the PRD §5.2
// 7-state user-facing operation status set:
// queued / running / retrying / completed / partial / failed / cancelled.
func PRDOperationStatus(status string) string {
	switch status {
	case "succeeded", "completed":
		return "completed"
	case "retry_wait", "retrying":
		return "retrying"
	case "leased", "running", "cancel_requested", "reconciliation_required":
		return "running"
	case "queued", "pending":
		return "queued"
	case "partial":
		return "partial"
	case "cancelled":
		return "cancelled"
	default:
		return "failed"
	}
}

// notifyTitles: engine events that also become in-app notifications (besides report.completed).
var notifyTitles = map[string]string{
	"research.completed": "研究任务已完成",
	"schedule.blocked":   "采集计划已受阻",
	"document.revised":   "已引用的文档出现新修订",
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
			CountDuplicateDelivery()
			return nil
		}
		applied = true
		if e.Type == "report.completed" {
			var p struct {
				ReportID string `json:"report_id"`
				Title    string `json:"title"`
				Kind     string `json:"kind"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FlowNotification{ID: NewID("ntf_"), TenantID: e.TenantID,
				WorkspaceID: e.WorkspaceID, EventID: e.EventID, Type: e.Type, Title: p.Title, RefID: p.ReportID, CreatedAt: time.Now().UTC()}).Error; err != nil {
				return err
			}
		}
		if t, ok := notifyTitles[e.Type]; ok {
			var p struct {
				Title            string `json:"title"`
				Reason           string `json:"reason"`
				DocumentID       string `json:"document_id"`
				ResearchRunID    string `json:"research_run_id"`
				ScheduleID       string `json:"schedule_id"`
				PreviousRevision int64  `json:"previous_revision"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			ref := p.ResearchRunID + p.ScheduleID + p.DocumentID
			title := t
			if p.Reason != "" {
				title += "：" + p.Reason
			}
			notify := true
			if e.Type == "document.revised" {
				// Only documents the platform has cited are worth a notice (citations keep their revision).
				notify = citedDocument(tx, e.TenantID, p.DocumentID)
			}
			if notify {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FlowNotification{ID: NewID("ntf_"), TenantID: e.TenantID,
					WorkspaceID: e.WorkspaceID, EventID: e.EventID, Type: e.Type, Title: truncRunes(title, 280), RefID: ref, CreatedAt: time.Now().UTC()}).Error; err != nil {
					return err
				}
			}
		}
		if e.Type == "report.completed" || e.Type == "operation.failed" || e.Type == "schedule.blocked" || e.Type == "research.completed" {
			// External deliveries are created in the same inbox transaction: exactly once per event.
			var p struct {
				ReportID string `json:"report_id"`
				Title    string `json:"title"`
				Error    string `json:"error"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			title := p.Title
			if title == "" {
				title = strings.TrimSpace(e.Type + " " + p.Error)
			}
			if err := fanOutDeliveries(tx, e, title, p.ReportID, time.Now().UTC()); err != nil {
				return err
			}
		}
		if e.Type == "usage.recorded" {
			// Engine-metered usage (ocr_pages, asr_seconds, paid_source_calls, stored_bytes) is priced
			// with the current price book and charged once per event.
			var p struct {
				UsageType  string `json:"usage_type"`
				Quantity   int64  `json:"quantity"`
				DocumentID string `json:"document_id"`
				ObjectKey  string `json:"object_key"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			if validUsageType(p.UsageType) && p.Quantity >= 0 {
				if _, err := DefaultUsageRecorder.Record(tx, UsageInput{TenantID: e.TenantID, WorkspaceID: e.WorkspaceID, UsageType: p.UsageType, Quantity: p.Quantity,
					CostMicro: -1, SubjectRef: strings.Trim(p.DocumentID+":"+p.ObjectKey, ":"), IdemKey: "event:" + e.EventID, Charge: true}, time.Now().UTC()); err != nil {
					return err
				}
			}
		}
		status, ok := statusByType[e.Type]
		if !ok {
			return nil
		}
		if e.Type == "research.completed" {
			var p struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(e.Payload, &p)
			if p.Status == "failed" {
				status = "failed"
			}
		}
		if status == "succeeded" || status == "failed" || status == "cancelled" {
			// waiting_external: a duplicate delivery never reaches here (inbox dedupe).
			if err := markWaitsReady(tx, e.TenantID, e.OperationID, status); err != nil {
				return err
			}
		}
		var p model.FlowOperationProjection
		err := tx.First(&p, "operation_id = ?", e.OperationID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&model.FlowOperationProjection{OperationID: e.OperationID, TenantID: e.TenantID,
				WorkspaceID: e.WorkspaceID, JobID: e.JobID, Status: status, Sequence: e.Sequence, AsOf: e.OccurredAt, SourceVersion: e.Sequence, SyncStatus: "synced"}).Error
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
		return tx.Model(&p).Updates(map[string]any{"status": status, "sequence": e.Sequence, "as_of": e.OccurredAt, "job_id": e.JobID, "source_version": e.Sequence, "sync_status": "synced"}).Error
	})
	return applied, err
}

// citedDocument: a conversation of the tenant has the document in its evidence scope.
func citedDocument(tx *gorm.DB, tenantID, documentID string) bool {
	if documentID == "" {
		return false
	}
	var n int64
	tx.Model(&model.FlowAgentConversation{}).Where("tenant_id = ? AND evidence_scope LIKE ?", tenantID, "%\""+documentID+"\"%").Count(&n)
	return n > 0
}
