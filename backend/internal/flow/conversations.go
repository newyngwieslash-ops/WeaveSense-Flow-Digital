package flow

import (
	"errors"
	"log/slog"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrNotFound = errors.New("flow: not found")

// Conversations is the server-side directory. The web client's localStorage
// directory becomes a cache of ListConversations.
type Conversations struct {
	DB *gorm.DB
	// noWriteBack: set by the canvas mirror itself (no echo back to the sidebar).
	noWriteBack bool
}

func (c Conversations) Create(userID string, ss SubjectScope, title string) (model.FlowAgentConversation, error) {
	conv := model.FlowAgentConversation{ID: NewID("conv_"), TenantID: ss.Scope.TenantID, WorkspaceID: ss.Scope.WorkspaceID,
		UserID: userID, SubjectKind: ss.Subject.Kind, SubjectID: ss.Subject.ID, Title: title, CurrentTopicID: ss.TopicID,
		EvidenceScope: "{}", ConfirmedPlans: "[]"}
	return conv, c.DB.Create(&conv).Error
}

// Get enforces ownership: conversation must belong to the user AND the
// user's currently resolved tenant (re-resolved per request).
func (c Conversations) Get(userID, id string) (model.FlowAgentConversation, SubjectScope, error) {
	var conv model.FlowAgentConversation
	if err := c.DB.First(&conv, "id = ? AND user_id = ?", id, userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return conv, SubjectScope{}, ErrNotFound
		}
		return conv, SubjectScope{}, err
	}
	ss, err := ResolveSubject(c.DB, userID, AgentSubject{Kind: conv.SubjectKind, ID: conv.SubjectID})
	if err != nil || ss.Scope.TenantID != conv.TenantID {
		return conv, SubjectScope{}, ErrNotFound
	}
	return conv, ss, nil
}

func (c Conversations) List(userID string, limit int, before time.Time) ([]model.FlowAgentConversation, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	q := c.DB.Where("user_id = ?", userID).Order("updated_at DESC").Limit(limit)
	if !before.IsZero() {
		q = q.Where("updated_at < ?", before)
	}
	var out []model.FlowAgentConversation
	return out, q.Find(&out).Error
}

// Append adds a message with the next sequence number atomically.
func (c Conversations) Append(conv model.FlowAgentConversation, m model.FlowAgentMessage) (model.FlowAgentMessage, error) {
	var kind string
	err := c.DB.Transaction(func(tx *gorm.DB) error {
		var cur model.FlowAgentConversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&cur, "id = ?", conv.ID).Error; err != nil {
			return err
		}
		kind = cur.SubjectKind
		m.ID, m.ConversationID, m.TenantID, m.Seq = NewID("msg_"), cur.ID, cur.TenantID, cur.LastSeq+1
		if err := tx.Create(&m).Error; err != nil {
			return err
		}
		return tx.Model(&cur).Updates(map[string]any{"last_seq": m.Seq, "updated_at": time.Now().UTC()}).Error
	})
	if err == nil && !c.noWriteBack && kind == SubjectCanvas && m.ExternalID == "" && (m.Role == "user" || m.Role == "assistant") {
		// Two-way canvas sync: write intelligence-side turns back to the sidebar document.
		if serr := SyncConversationToCanvas(c.DB, conv.ID); serr != nil {
			slog.Warn("flow: canvas write-back", "conversation", conv.ID, "error", serr)
		}
	}
	return m, err
}

// Messages pages newest-first by seq; beforeSeq=0 means latest.
func (c Conversations) Messages(convID string, beforeSeq int64, limit int) ([]model.FlowAgentMessage, int64, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := c.DB.Where("conversation_id = ?", convID).Order("seq DESC").Limit(limit + 1)
	if beforeSeq > 0 {
		q = q.Where("seq < ?", beforeSeq)
	}
	var rows []model.FlowAgentMessage
	if err := q.Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	var next int64
	if len(rows) > limit {
		rows = rows[:limit]
		next = rows[len(rows)-1].Seq
	}
	return rows, next, nil
}

// Runs lists CloudAgent executions (Pi runs/events) linked to the conversation
// through the run ids recorded on its messages.
func (c Conversations) Runs(conv model.FlowAgentConversation) ([]map[string]any, error) {
	var ids []string
	if err := c.DB.Model(&model.FlowAgentMessage{}).Where("conversation_id = ? AND run_id <> ''", conv.ID).Distinct().Pluck("run_id", &ids).Error; err != nil || len(ids) == 0 {
		return []map[string]any{}, err
	}
	var out []map[string]any
	err := c.DB.Model(&model.CloudAgentExecution{}).Select("id, status, created_at").
		Where("id IN ? AND user_id = ?", ids, conv.UserID).Order("created_at").Find(&out).Error
	return out, err
}
