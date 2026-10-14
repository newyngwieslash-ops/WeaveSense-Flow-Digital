package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/model"
)

type modelIdemCtx struct{}

// ---------------------------------------------------------------------------
// Model Gateway status lookup (reconciliation of unknown reservations)

// ModelStatus queries FLOW_MODEL_GATEWAY_STATUS_URL?idempotency_key=<key> for
// the outcome of a call whose answer was lost. Expected JSON:
// {"status":"succeeded"|"not_found"|"pending","total_tokens":N}.
type ModelStatus struct {
	URL, Key      string
	MicroPer1KTok int64
	HTTP          *http.Client
}

func ModelStatusFromEnv() ModelStatus {
	price, _ := strconv.ParseInt(os.Getenv("FLOW_MODEL_MICRO_PER_1K_TOKENS"), 10, 64)
	if price <= 0 {
		price = 2000
	}
	return ModelStatus{URL: strings.TrimSpace(os.Getenv("FLOW_MODEL_GATEWAY_STATUS_URL")), Key: os.Getenv("FLOW_MODEL_GATEWAY_KEY"), MicroPer1KTok: price}
}

type ModelStatusResult struct {
	Status      string `json:"status"`
	TotalTokens int64  `json:"total_tokens"`
	Content     string `json:"content"`
}

func (m ModelStatus) Query(ctx context.Context, r model.FlowBudgetReservation) (Outcome, error) {
	key := strings.TrimPrefix(r.ExternalRef, "model:")
	if m.URL == "" || key == "" || !strings.HasPrefix(r.ExternalRef, "model:") {
		return Outcome{}, nil
	}
	st, err := m.QueryKey(ctx, key)
	if err != nil {
		return Outcome{}, err
	}
	switch st.Status {
	case "succeeded":
		return Outcome{Known: true, Succeeded: true, Cost: st.TotalTokens * m.MicroPer1KTok / 1000}, nil
	case "not_found", "failed":
		return Outcome{Known: true}, nil
	}
	return Outcome{}, nil
}

// QueryKey asks the gateway what happened to the request with this idempotency key.
func (m ModelStatus) QueryKey(ctx context.Context, key string) (ModelStatusResult, error) {
	var st ModelStatusResult
	if m.URL == "" {
		return st, errors.New("no status url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.URL+"?idempotency_key="+url.QueryEscape(key), nil)
	if err != nil {
		return st, err
	}
	if m.Key != "" {
		req.Header.Set("Authorization", "Bearer "+m.Key)
	}
	hc := m.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return st, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return st, fmt.Errorf("status lookup %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st); err != nil {
		return st, err
	}
	return st, nil
}

// ResolveReservation is the operator path for an unknown reservation:
// succeeded settles (cost capped at the reservation), not_executed releases.
func ResolveReservation(db *gorm.DB, scope Scope, id, outcome string, cost int64) (model.FlowBudgetReservation, error) {
	if scope.Role != "admin" {
		return model.FlowBudgetReservation{}, ErrForbidden
	}
	var r model.FlowBudgetReservation
	if err := db.First(&r, "id = ? AND tenant_id = ?", id, scope.TenantID).Error; err != nil {
		return r, ErrNotFound
	}
	if r.Status != StatusReconciliation {
		return r, ErrBudgetState
	}
	b := Billing{DB: db}
	switch outcome {
	case "succeeded":
		if cost <= 0 {
			cost = r.ReservedMicro
		}
		return b.Settle(id, cost)
	case "not_executed":
		return b.Release(id)
	}
	return r, ErrInvalid
}

// ---------------------------------------------------------------------------
// MCP unknown-call operator resolution

func ListMCPCalls(db *gorm.DB, scope Scope, status string) ([]model.FlowMCPCall, error) {
	q := db.Where("tenant_id = ?", scope.TenantID).Order("updated_at DESC").Limit(100)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var out []model.FlowMCPCall
	return out, q.Find(&out).Error
}

// ResolveMCPCall lets an admin settle an unknown MCP call after checking the
// remote system: succeeded (charged once, a replay returns output) or
// not_executed (marked failed, so the same key may be retried).
func (g MCPGateway) ResolveMCPCall(scope Scope, id, outcome, output string) (model.FlowMCPCall, error) {
	var row model.FlowMCPCall
	if scope.Role != "admin" {
		return row, ErrForbidden
	}
	if err := g.DB.First(&row, "id = ? AND tenant_id = ?", id, scope.TenantID).Error; err != nil {
		return row, ErrNotFound
	}
	if row.Status != StatusReconciliation && row.Status != "pending" {
		return row, ErrBudgetState
	}
	switch outcome {
	case "succeeded":
		upd := g.DB.Model(&model.FlowMCPCall{}).Where("id = ? AND status IN ('pending','reconciliation_required')", row.ID).
			Updates(map[string]any{"status": "succeeded", "output": truncRunes(output, 64000), "billed": true, "updated_at": g.now()})
		if upd.RowsAffected == 1 {
			if c, err := g.get(scope, row.ConnectionID); err == nil {
				g.usage(c, row.Tool, "operator", "succeeded", g.now(), len(output), g.View(c).CostRule.PerCallCredits)
			}
		}
	case "not_executed":
		g.DB.Model(&model.FlowMCPCall{}).Where("id = ? AND status IN ('pending','reconciliation_required')", row.ID).
			Updates(map[string]any{"status": "failed", "updated_at": g.now()})
	default:
		return row, ErrInvalid
	}
	row = model.FlowMCPCall{}
	return row, g.DB.First(&row, "id = ?", id).Error
}

// ---------------------------------------------------------------------------
// Org members

type MemberView struct {
	UserID   string `json:"userId"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	Status   string `json:"status"`
	JoinedAt string `json:"joinedAt"`
}

func ListMembers(db *gorm.DB, scope Scope) ([]MemberView, error) {
	var ms []model.Membership
	if err := db.Where("tenant_id = ? AND status = ?", scope.TenantID, "active").Order("created_at").Find(&ms).Error; err != nil {
		return nil, err
	}
	out := make([]MemberView, 0, len(ms))
	for _, m := range ms {
		var u model.User
		db.Select("id", "email", "display_name").First(&u, "id = ?", m.UserID)
		out = append(out, MemberView{UserID: m.UserID, Email: u.Email, Name: u.DisplayName, Role: m.Role, Status: m.Status, JoinedAt: m.CreatedAt.UTC().Format(time.RFC3339)})
	}
	return out, nil
}

// SetMemberRole changes a member's tenant role (admin|member) and the matching
// workspace role (admin→admin, member→editor). Owners cannot be changed and
// the last admin cannot demote themselves away.
func SetMemberRole(db *gorm.DB, scope Scope, userID, role string) error {
	if scope.Role != "admin" {
		return ErrForbidden
	}
	if role != "admin" && role != "member" && role != "viewer" {
		return ErrInvalid
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var m model.Membership
		if err := tx.First(&m, "tenant_id = ? AND user_id = ? AND status = ?", scope.TenantID, userID, "active").Error; err != nil {
			return ErrNotFound
		}
		if m.Role == "owner" {
			return ErrForbidden
		}
		tenantRole, wsRole := role, map[string]string{"admin": "admin", "member": "editor", "viewer": "viewer"}[role]
		if role == "viewer" {
			tenantRole = "member"
		}
		if m.Role == "admin" && tenantRole != "admin" {
			var n int64
			tx.Model(&model.Membership{}).Where("tenant_id = ? AND status = ? AND role IN ?", scope.TenantID, "active", []string{"admin", "owner"}).Count(&n)
			if n <= 1 {
				return ErrForbidden
			}
		}
		if err := tx.Model(&model.Membership{}).Where("id = ?", m.ID).Updates(map[string]any{"role": tenantRole, "updated_at": time.Now().UTC()}).Error; err != nil {
			return err
		}
		return tx.Model(&model.WorkspaceMembership{}).Where("user_id = ? AND workspace_id IN (?)", userID,
			tx.Model(&model.Workspace{}).Select("id").Where("tenant_id = ?", scope.TenantID)).Update("role", wsRole).Error
	})
}

// ---------------------------------------------------------------------------
// Per-user notification read state

type NotificationView struct {
	model.FlowNotification
	ReadAt *time.Time `json:"readAt"`
}

func ListNotificationsForUser(db *gorm.DB, scope Scope, userID string) ([]NotificationView, error) {
	var rows []model.FlowNotification
	if err := db.Where("tenant_id = ? AND workspace_id = ?", scope.TenantID, scope.WorkspaceID).Order("created_at DESC").Limit(100).Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	var reads []model.FlowNotificationRead
	if len(ids) > 0 {
		db.Where("user_id = ? AND notification_id IN ?", userID, ids).Find(&reads)
	}
	at := map[string]time.Time{}
	for _, r := range reads {
		at[r.NotificationID] = r.ReadAt
	}
	out := make([]NotificationView, 0, len(rows))
	for _, r := range rows {
		v := NotificationView{FlowNotification: r}
		if t, ok := at[r.ID]; ok {
			t := t
			v.ReadAt = &t
		}
		out = append(out, v)
	}
	return out, nil
}

// MarkNotificationsRead records read state for this user only ("all" = every
// notification in the workspace).
func MarkNotificationsRead(db *gorm.DB, scope Scope, userID, id string) (int64, error) {
	q := db.Model(&model.FlowNotification{}).Where("tenant_id = ? AND workspace_id = ?", scope.TenantID, scope.WorkspaceID)
	if id != "all" {
		q = q.Where("id = ?", id)
	}
	var ids []string
	if err := q.Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	var n int64
	for _, nid := range ids {
		r := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FlowNotificationRead{UserID: userID, NotificationID: nid, ReadAt: now})
		if r.Error != nil {
			return n, r.Error
		}
		n += r.RowsAffected
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Topic-specific tool sets

// topicTools: a topic conversation analyses existing evidence; it cannot
// change monitors/schedules, trigger collection or call external MCP tools.
var topicTools = map[string]bool{
	"intel_search_documents": true, "intel_read_document": true, "intel_extract_document": true,
	"intel_transcribe_document": true, "intel_get_operation": true, "intel_cancel_operation": true, "intel_load_skill": true,
	"intel_start_research": true, "intel_save_report": true,
}

// ToolAllowedForSubject reports whether a tool is in the subject's tool set.
func ToolAllowedForSubject(kind, tool string) bool {
	if kind != SubjectTopic {
		return true
	}
	return topicTools[tool]
}

var ErrToolNotInSubjectSet = errors.New("flow: tool not available for this subject")

// ---------------------------------------------------------------------------
// Canvas sidebar conversations → unified conversation directory

type CanvasConvMessage struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Text string `json:"text"`
}

type CanvasConv struct {
	ID       string              `json:"id"`
	Title    string              `json:"title"`
	Messages []CanvasConvMessage `json:"messages"`
	// DeletedIDs are tombstones of sidebar messages deleted on either side;
	// a deletion is never resurrected by a stale replica.
	DeletedIDs []string `json:"deletedIds,omitempty"`
}

// CanvasConversationIndexID is the stable directory id for a canvas sidebar conversation.
func CanvasConversationIndexID(userID, canvasID, convID string) string {
	return "cvc_" + shortHash(userID+"\x00"+canvasID+"\x00"+convID)
}

const dirMsgPrefix = "dir_"

func syncableCanvasMessages(cv CanvasConv) []CanvasConvMessage {
	out := make([]CanvasConvMessage, 0, len(cv.Messages))
	for i, m := range cv.Messages {
		if (m.Role == "user" || m.Role == "assistant") && strings.TrimSpace(m.Text) != "" {
			if m.ID == "" {
				m.ID = fmt.Sprintf("idx_%d", i)
			}
			out = append(out, m)
		}
	}
	return out
}

// MirrorCanvasConversations is the canvas → directory half of the two-way
// sync: each sidebar conversation maps to a directory conversation (subject
// canvas, ExternalID = sidebar id) and every user/assistant message is
// appended once, keyed by its sidebar message id. Messages that originated in
// the directory (written back as "dir_<id>") are recognised, not duplicated.
func MirrorCanvasConversations(db *gorm.DB, userID, canvasID string, convs []CanvasConv) error {
	sc, err := ResolveScope(db, userID, "")
	if err != nil {
		return err
	}
	cs := Conversations{DB: db, noWriteBack: true}
	for _, cv := range convs {
		if cv.ID == "" {
			continue
		}
		id := CanvasConversationIndexID(userID, canvasID, cv.ID)
		msgs := syncableCanvasMessages(cv)
		var row model.FlowAgentConversation
		err := db.First(&row, "id = ?", id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			row = model.FlowAgentConversation{ID: id, TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, UserID: userID, ExternalID: cv.ID,
				SubjectKind: SubjectCanvas, SubjectID: canvasID, Title: truncRunes(cv.Title, 200), EvidenceScope: "{}", ConfirmedPlans: "[]"}
			if err := db.Create(&row).Error; err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else if row.UserID != userID {
			continue
		} else {
			if row.ExternalID == "" {
				// Rows mirrored before v59 matched by position: backfill ids.
				var legacy []model.FlowAgentMessage
				db.Where("conversation_id = ? AND (external_id = '' OR external_id IS NULL)", id).Order("seq").Find(&legacy)
				for i, m := range legacy {
					if i < len(msgs) {
						db.Model(&model.FlowAgentMessage{}).Where("id = ?", m.ID).Update("external_id", msgs[i].ID)
					}
				}
				db.Model(&model.FlowAgentConversation{}).Where("id = ?", id).Update("external_id", cv.ID)
				row.ExternalID = cv.ID
			}
			if cv.Title != "" && cv.Title != row.Title {
				db.Model(&model.FlowAgentConversation{}).Where("id = ?", id).Update("title", truncRunes(cv.Title, 200))
			}
		}
		var known []model.FlowAgentMessage
		db.Where("conversation_id = ? AND external_id <> ''", id).Find(&known)
		seen := map[string]model.FlowAgentMessage{}
		for _, k := range known {
			seen[k.ExternalID] = k
		}
		deleted := map[string]bool{}
		for _, d := range cv.DeletedIDs {
			deleted[d] = true
		}
		now := time.Now().UTC()
		for ext, k := range seen {
			if k.TombstonedAt != nil || k.Revision != k.SyncedRevision {
				continue // already deleted, or a directory-side change is pending write-back (directory wins)
			}
			if deleted[ext] {
				// Deleted in the sidebar (explicit tombstone, never inferred from a
				// possibly stale replica's absence): tombstone, idempotent, revision CAS.
				db.Model(&model.FlowAgentMessage{}).Where("id = ? AND revision = ?", k.ID, k.Revision).
					Updates(map[string]any{"tombstoned_at": now, "revision": k.Revision + 1, "synced_revision": k.Revision + 1})
			}
		}
		for _, m := range msgs {
			if k, ok := seen[m.ID]; ok {
				text := truncRunes(m.Text, 20000)
				if k.TombstonedAt == nil && k.Revision == k.SyncedRevision && k.Content != text && !deleted[m.ID] {
					// Edited in the sidebar: update the directory copy.
					db.Model(&model.FlowAgentMessage{}).Where("id = ? AND revision = ?", k.ID, k.Revision).
						Updates(map[string]any{"content": text, "edited_at": now, "revision": k.Revision + 1, "synced_revision": k.Revision + 1})
				}
				continue
			}
			if deleted[m.ID] {
				continue
			}
			if dirID, ok := strings.CutPrefix(m.ID, dirMsgPrefix); ok {
				r := db.Model(&model.FlowAgentMessage{}).Where("id = ? AND conversation_id = ?", dirID, id).Update("external_id", m.ID)
				if r.RowsAffected == 1 {
					continue
				}
			}
			if _, err := cs.Append(row, model.FlowAgentMessage{Role: m.Role, Content: truncRunes(m.Text, 20000), ExternalID: m.ID}); err != nil {
				return err
			}
		}
	}
	return nil
}

// SyncConversationToCanvas is the directory → canvas half: user/assistant
// messages added from the intelligence page are appended to the sidebar
// document with optimistic concurrency (version CAS, retried on conflict),
// then marked as synced. Other document fields are preserved verbatim.
func SyncConversationToCanvas(db *gorm.DB, convID string) error {
	var conv model.FlowAgentConversation
	if err := db.First(&conv, "id = ?", convID).Error; err != nil {
		return err
	}
	if conv.SubjectKind != SubjectCanvas || conv.ExternalID == "" {
		return nil
	}
	var pending []model.FlowAgentMessage
	db.Where("conversation_id = ? AND role IN ? AND (((external_id = '' OR external_id IS NULL) AND tombstoned_at IS NULL) OR revision <> synced_revision)",
		convID, []string{"user", "assistant"}).Order("seq").Find(&pending)
	if len(pending) == 0 {
		return nil
	}
	for attempt := 0; attempt < 5; attempt++ {
		var doc model.FlowCanvasConversationDoc
		if err := db.First(&doc, "user_id = ? AND canvas_id = ?", conv.UserID, conv.SubjectID).Error; err != nil {
			return err
		}
		var root map[string]any
		if err := json.Unmarshal([]byte(nonEmpty(doc.Document)), &root); err != nil {
			return err
		}
		list, _ := root["conversations"].([]any)
		now := time.Now().UTC().Format(time.RFC3339Nano)
		found := false
		for _, item := range list {
			c, _ := item.(map[string]any)
			if c == nil || c["id"] != conv.ExternalID {
				continue
			}
			found = true
			msgs, _ := c["messages"].([]any)
			have := map[string]map[string]any{}
			for _, mm := range msgs {
				if m, _ := mm.(map[string]any); m != nil {
					if id, _ := m["id"].(string); id != "" {
						have[id] = m
					}
				}
			}
			tomb := map[string]bool{}
			if old, _ := c["deletedIds"].([]any); old != nil {
				for _, d := range old {
					if s, _ := d.(string); s != "" {
						tomb[s] = true
					}
				}
			}
			for _, p := range pending {
				ext := p.ExternalID
				if ext == "" {
					ext = dirMsgPrefix + p.ID
				}
				switch {
				case p.TombstonedAt != nil:
					tomb[ext] = true
				case have[ext] != nil:
					have[ext]["text"] = p.Content
					have[ext]["editedAt"] = now
				case !tomb[ext]:
					m := map[string]any{"id": ext, "role": p.Role, "text": p.Content, "meta": "intelligence"}
					have[ext] = m
					msgs = append(msgs, m)
				}
			}
			kept := make([]any, 0, len(msgs))
			for _, mm := range msgs {
				if m, _ := mm.(map[string]any); m != nil {
					if id, _ := m["id"].(string); tomb[id] {
						continue
					}
				}
				kept = append(kept, mm)
			}
			if len(tomb) > 0 {
				ids := make([]string, 0, len(tomb))
				for id := range tomb {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				c["deletedIds"] = ids
			}
			c["messages"] = kept
			c["updatedAt"] = now
		}
		if !found {
			return nil
		}
		raw, _ := json.Marshal(root)
		upd := db.Model(&model.FlowCanvasConversationDoc{}).Where("user_id = ? AND canvas_id = ? AND version = ?", conv.UserID, conv.SubjectID, doc.Version).
			Updates(map[string]any{"document": string(raw), "version": doc.Version + 1, "updated_at": time.Now().UTC()})
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected == 1 {
			for _, p := range pending {
				ext := p.ExternalID
				if ext == "" {
					ext = dirMsgPrefix + p.ID
				}
				// Revision-guarded: a concurrent edit stays pending for the next sync.
				db.Model(&model.FlowAgentMessage{}).Where("id = ? AND revision = ?", p.ID, p.Revision).
					Updates(map[string]any{"external_id": ext, "synced_revision": p.Revision})
			}
			return nil
		}
	}
	return errVersionConflict
}

// ---------------------------------------------------------------------------
// Platform RLS: scoped transactions

// TenantTx runs fn in a transaction with app.tenant_ids set, so Postgres RLS
// policies on flow tables restrict every statement to that tenant.
func TenantTx(db *gorm.DB, tenantID string, fn func(tx *gorm.DB) error) error {
	if tenantID == "" {
		return ErrForbidden
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == "postgres" {
			if err := tx.Exec("SELECT set_config('app.tenant_ids', ?, true)", tenantID).Error; err != nil {
				return err
			}
		}
		return fn(tx)
	})
}

var ErrInvalid = errors.New("flow: invalid request")

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:16])
}
