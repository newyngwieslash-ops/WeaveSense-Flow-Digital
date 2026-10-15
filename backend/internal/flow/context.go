package flow

import (
	"encoding/json"
	"fmt"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

// TurnContext is assembled server-side for every agent turn.
type TurnContext struct {
	Subject        AgentSubject             `json:"subject"`
	TenantID       string                   `json:"tenantId"`
	WorkspaceID    string                   `json:"workspaceId"`
	Role           string                   `json:"role"`
	Summary        string                   `json:"summary,omitempty"`
	Recent         []model.FlowAgentMessage `json:"recent"`
	CurrentTopic   *model.FlowTopic         `json:"currentTopic,omitempty"`
	EvidenceScope  map[string]any           `json:"evidenceScope"`
	ConfirmedPlans []any                    `json:"confirmedPlans"`
	ActionReceipts []ToolResult             `json:"actionReceipts"`
	BackgroundJobs []map[string]any         `json:"backgroundJobs"`
	// Canvas is the canvas adapter's context (enhanced canvas snapshot); empty for intelligence subjects.
	Canvas map[string]any `json:"canvas,omitempty"`
}

const recentWindow = 12

func BuildTurnContext(db *gorm.DB, conv model.FlowAgentConversation, ss SubjectScope) (TurnContext, error) {
	tc := TurnContext{Subject: ss.Subject, TenantID: ss.Scope.TenantID, WorkspaceID: ss.Scope.WorkspaceID, Role: ss.Scope.Role,
		Summary: conv.Summary, EvidenceScope: map[string]any{}, ConfirmedPlans: []any{}, ActionReceipts: []ToolResult{}, BackgroundJobs: []map[string]any{}}
	// Recent messages after the summarized prefix, oldest first.
	var msgs []model.FlowAgentMessage
	if err := db.Where("conversation_id = ? AND seq > ? AND tombstoned_at IS NULL", conv.ID, conv.SummaryThroughSeq).Order("seq DESC").Limit(recentWindow).Find(&msgs).Error; err != nil {
		return tc, err
	}
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	tc.Recent = msgs
	if conv.CurrentTopicID != "" {
		var t model.FlowTopic
		if db.First(&t, "id = ? AND tenant_id = ?", conv.CurrentTopicID, conv.TenantID).Error == nil {
			tc.CurrentTopic = &t
		}
	}
	_ = json.Unmarshal([]byte(conv.EvidenceScope), &tc.EvidenceScope)
	_ = json.Unmarshal([]byte(conv.ConfirmedPlans), &tc.ConfirmedPlans)
	// Receipts of write actions (and pending approvals), newest last.
	var calls []model.FlowAgentToolCall
	if err := db.Where("conversation_id = ? AND tenant_id = ? AND class = 'write'", conv.ID, conv.TenantID).Order("created_at DESC").Limit(10).Find(&calls).Error; err != nil {
		return tc, err
	}
	opIDs := []string{}
	for i := len(calls) - 1; i >= 0; i-- {
		r := storedResult(calls[i])
		r.Output = capJSON(r.Output, 1500, &r.Truncated)
		tc.ActionReceipts = append(tc.ActionReceipts, r)
		var o struct {
			OperationID string `json:"operation_id"`
		}
		if json.Unmarshal(r.Output, &o) == nil && o.OperationID != "" {
			opIDs = append(opIDs, o.OperationID)
		}
	}
	var proj []model.FlowOperationProjection
	q := db.Where("tenant_id = ? AND workspace_id = ?", conv.TenantID, conv.WorkspaceID)
	if len(opIDs) > 0 {
		q = q.Where("status IN ? OR operation_id IN ?", []string{"running", "blocked"}, opIDs)
	} else {
		q = q.Where("status IN ?", []string{"running", "blocked"})
	}
	if err := q.Order("as_of DESC").Limit(10).Find(&proj).Error; err != nil {
		return tc, err
	}
	known := map[string]bool{}
	for _, p := range proj {
		known[p.OperationID] = true
		tc.BackgroundJobs = append(tc.BackgroundJobs, map[string]any{"operationId": p.OperationID, "status": p.Status, "asOf": p.AsOf, "source": "projection",
			"sourceVersion": p.SourceVersion, "syncStatus": ProjectionSyncStatus(p, time.Now().UTC())})
	}
	for _, id := range opIDs {
		if !known[id] {
			tc.BackgroundJobs = append(tc.BackgroundJobs, map[string]any{"operationId": id, "status": "unknown", "source": "receipt"})
		}
	}
	return tc, nil
}

// SystemMessage renders the non-message parts of the context for the model.
func (tc TurnContext) SystemMessage() map[string]any {
	ctx := map[string]any{"subject": tc.Subject, "workspaceId": tc.WorkspaceID, "summary": tc.Summary, "currentTopic": tc.CurrentTopic,
		"evidenceScope": tc.EvidenceScope, "confirmedPlans": tc.ConfirmedPlans, "actionReceipts": tc.ActionReceipts, "backgroundJobs": tc.BackgroundJobs}
	b, _ := json.Marshal(ctx)
	return map[string]any{"role": "system", "content": fmt.Sprintf("Intelligence workspace context (authoritative, server-assembled). Resolve references like \"that article\" using evidenceScope.recent_document_ids and actionReceipts; never claim an action happened without a succeeded receipt.\n%s", b)}
}

// Messages returns system context + recent turns in canonical chat form.
func (tc TurnContext) Messages() []map[string]any {
	out := []map[string]any{tc.SystemMessage()}
	for _, m := range tc.Recent {
		msg := map[string]any{"role": m.Role, "content": m.Content}
		if m.Role == "tool" { // stored receipts are rendered without provider tool-call pairing
			msg = map[string]any{"role": "assistant", "content": fmt.Sprintf("[tool %s result] %s", m.ToolName, m.Content)}
		}
		out = append(out, msg)
	}
	return out
}
