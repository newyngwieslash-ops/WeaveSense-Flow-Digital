package flow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/model"
)

// HandoffCitation records a verifiable evidence citation attached to a handed-off
// intelligence artifact so the Canvas Pi Agent can trace claims back to source documents.
type HandoffCitation struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url,omitempty"`
	Snippet    string `json:"snippet,omitempty"`
	CharStart  int    `json:"char_start,omitempty"`
	CharEnd    int    `json:"char_end,omitempty"`
}

// HandoffToCanvasInput describes an intelligence artifact (report, brief, topic,
// document, or synthesis) to land on a new or existing Canvas as a source text node.
type HandoffToCanvasInput struct {
	CanvasID    string            `json:"canvas_id"`
	CanvasTitle string            `json:"canvas_title"`
	Kind        string            `json:"kind"`
	SourceID    string            `json:"source_id"`
	Title       string            `json:"title"`
	Content     string            `json:"content"`
	Citations   []HandoffCitation `json:"citations,omitempty"`
	AgentPrompt string            `json:"agent_prompt,omitempty"`
	OpenAgent   *bool             `json:"open_agent,omitempty"`
}

func (in *HandoffToCanvasInput) UnmarshalJSON(data []byte) error {
	type rawInput struct {
		CanvasID     string            `json:"canvas_id"`
		CanvasID2    string            `json:"canvasId"`
		CanvasTitle  string            `json:"canvas_title"`
		CanvasTitle2 string            `json:"canvasTitle"`
		Kind         string            `json:"kind"`
		SourceID     string            `json:"source_id"`
		SourceID2    string            `json:"sourceId"`
		Title        string            `json:"title"`
		Content      string            `json:"content"`
		Citations    []HandoffCitation `json:"citations"`
		AgentPrompt  string            `json:"agent_prompt"`
		AgentPrompt2 string            `json:"agentPrompt"`
		OpenAgent    *bool             `json:"open_agent"`
		OpenAgent2   *bool             `json:"openAgent"`
	}
	var r rawInput
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	in.CanvasID = firstNonEmpty(r.CanvasID, r.CanvasID2)
	in.CanvasTitle = firstNonEmpty(r.CanvasTitle, r.CanvasTitle2)
	in.Kind = r.Kind
	in.SourceID = firstNonEmpty(r.SourceID, r.SourceID2)
	in.Title = r.Title
	in.Content = r.Content
	in.Citations = r.Citations
	in.AgentPrompt = firstNonEmpty(r.AgentPrompt, r.AgentPrompt2)
	if r.OpenAgent != nil {
		in.OpenAgent = r.OpenAgent
	} else {
		in.OpenAgent = r.OpenAgent2
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// HandoffToCanvasResult is returned by both the REST endpoint and intel_handoff_to_canvas.
type HandoffToCanvasResult struct {
	CanvasID       string `json:"canvas_id"`
	CanvasTitle    string `json:"canvas_title"`
	NodeID         string `json:"node_id"`
	NodeTitle      string `json:"node_title"`
	ConversationID string `json:"conversation_id"`
	CanvasURL      string `json:"canvas_url"`
	CreatedCanvas  bool   `json:"created_canvas"`
	Revision       int64  `json:"revision"`
}

func normalizeHandoffKind(kind string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" {
		return "report", nil
	}
	switch k {
	case "report", "brief", "topic", "document", "analysis":
		return k, nil
	default:
		return "", fmt.Errorf("%w: unsupported kind %q", ErrInvalid, kind)
	}
}

func handoffKindLabel(kind string) string {
	switch kind {
	case "report":
		return "研究报告"
	case "brief":
		return "情报简报"
	case "topic":
		return "研究专题"
	case "document":
		return "原始素材"
	case "analysis":
		return "情报分析"
	default:
		return "情报条目"
	}
}

// FormatIntelHandoffContent appends a structured provenance & citations footer
// to the node's markdown content so canvas_read_text preserves verifiable evidence.
func FormatIntelHandoffContent(s Scope, kind, sourceID, content string, citations []HandoffCitation) string {
	body := strings.TrimSpace(content)
	if strings.Contains(body, "## 情报来源与证据引用") {
		return body
	}
	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n\n---\n## 情报来源与证据引用\n")
	b.WriteString(fmt.Sprintf("- **情报类型**: %s (`%s`)\n", handoffKindLabel(kind), kind))
	if s.WorkspaceID != "" {
		b.WriteString(fmt.Sprintf("- **来源工作区**: `%s`\n", s.WorkspaceID))
	}
	if strings.TrimSpace(sourceID) != "" {
		b.WriteString(fmt.Sprintf("- **来源标识**: `%s`\n", strings.TrimSpace(sourceID)))
	}
	if len(citations) > 0 {
		b.WriteString("- **证据引用**:\n")
		for i, c := range citations {
			docID := strings.TrimSpace(c.DocumentID)
			if docID == "" {
				docID = fmt.Sprintf("ref_%d", i+1)
			}
			label := strings.TrimSpace(c.Title)
			if label == "" {
				label = docID
			} else if docID != label {
				label = fmt.Sprintf("%s (`%s`)", label, docID)
			}
			line := fmt.Sprintf("  %d. %s", i+1, label)
			if snip := strings.TrimSpace(c.Snippet); snip != "" {
				line += fmt.Sprintf(" — “%s”", truncRunes(snip, 180))
			}
			if u := strings.TrimSpace(c.URL); u != "" {
				line += fmt.Sprintf(" (%s)", u)
			}
			b.WriteString(line + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func computeNextNodePosition(nodes []any) map[string]any {
	if len(nodes) == 0 {
		return map[string]any{"x": 120, "y": 120}
	}
	maxRight := 120.0
	baseY := 120.0
	found := false
	for _, raw := range nodes {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		pos, _ := m["position"].(map[string]any)
		x, xOk := asFloat(pos["x"])
		y, yOk := asFloat(pos["y"])
		w, wOk := asFloat(m["width"])
		if !wOk || w <= 0 {
			w = 420
		}
		if xOk && yOk {
			if !found || x+w > maxRight {
				maxRight = x + w
				baseY = y
				found = true
			}
		}
	}
	if !found {
		return map[string]any{"x": 120 + len(nodes)*48, "y": 120 + len(nodes)*36}
	}
	return map[string]any{"x": int(maxRight) + 60, "y": int(baseY)}
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// HandoffToCanvas lands an intelligence artifact onto a new or existing Canvas
// owned by userID as a structured text node, and mirrors a handoff conversation
// into the shared Canvas/Intelligence conversation directory.
func HandoffToCanvas(db *gorm.DB, s Scope, userID string, in HandoffToCanvasInput, now time.Time) (HandoffToCanvasResult, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" || s.TenantID == "" || s.WorkspaceID == "" {
		return HandoffToCanvasResult{}, ErrForbidden
	}
	kind, err := normalizeHandoffKind(in.Kind)
	if err != nil {
		return HandoffToCanvasResult{}, err
	}
	// If handing off a workspace topic by ID without explicit title/content, load it.
	if kind == "topic" && strings.TrimSpace(in.SourceID) != "" && (strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Content) == "") {
		if top, terr := GetTopic(db, s, strings.TrimSpace(in.SourceID)); terr == nil {
			if strings.TrimSpace(in.Title) == "" {
				in.Title = top.Title
			}
			if strings.TrimSpace(in.Content) == "" {
				var parts []string
				if strings.TrimSpace(top.Question) != "" {
					parts = append(parts, "### 核心研究问题\n"+strings.TrimSpace(top.Question))
				}
				if strings.TrimSpace(top.Notes) != "" {
					parts = append(parts, "### 专题笔记与进展\n"+strings.TrimSpace(top.Notes))
				}
				in.Content = strings.Join(parts, "\n\n")
				if in.Content == "" {
					in.Content = top.Title
				}
			}
		}
	}
	nodeTitle := truncRunes(strings.TrimSpace(in.Title), 120)
	rawContent := strings.TrimSpace(in.Content)
	if nodeTitle == "" || rawContent == "" {
		return HandoffToCanvasResult{}, fmt.Errorf("%w: title and content are required", ErrInvalid)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if !db.Migrator().HasTable(&model.CanvasProject{}) {
		if err := db.AutoMigrate(&model.CanvasProject{}); err != nil {
			return HandoffToCanvasResult{}, err
		}
	}
	if !db.Migrator().HasTable(&model.FlowCanvasConversationDoc{}) {
		if err := db.AutoMigrate(&model.FlowCanvasConversationDoc{}); err != nil {
			return HandoffToCanvasResult{}, err
		}
	}

	fullContent := FormatIntelHandoffContent(s, kind, in.SourceID, rawContent, in.Citations)
	citations := in.Citations
	if citations == nil {
		citations = []HandoffCitation{}
	}
	intelSourceMeta := map[string]any{
		"workspaceId": s.WorkspaceID,
		"tenantId":    s.TenantID,
		"kind":        kind,
		"sourceId":    strings.TrimSpace(in.SourceID),
		"title":       nodeTitle,
		"citations":   citations,
		"handedOffAt": now.Format(time.RFC3339),
	}

	nodeID := NewID("intel_node_")
	canvasID := strings.TrimSpace(in.CanvasID)
	canvasTitle := truncRunes(strings.TrimSpace(in.CanvasTitle), 120)
	if canvasTitle == "" {
		canvasTitle = nodeTitle
	}
	createdCanvas := false
	var revision int64

	err = db.Transaction(func(tx *gorm.DB) error {
		if canvasID == "" {
			createdCanvas = true
			canvasID = NewID("canvas_")
			node := map[string]any{
				"id":       nodeID,
				"type":     "text",
				"title":    nodeTitle,
				"position": map[string]any{"x": 120, "y": 120},
				"width":    420,
				"height":   320,
				"status":   "idle",
				"metadata": map[string]any{
					"content":     fullContent,
					"intelSource": intelSourceMeta,
				},
			}
			payload := map[string]any{
				"id":          canvasID,
				"title":       canvasTitle,
				"nodes":       []any{node},
				"connections": []any{},
				"viewport":    map[string]any{"x": 0, "y": 0, "k": 1},
				"createdAt":   now.Format(time.RFC3339Nano),
				"updatedAt":   now.Format(time.RFC3339Nano),
			}
			rawPayload, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			proj := model.CanvasProject{
				ID:          canvasID,
				UserID:      userID,
				Title:       canvasTitle,
				PayloadJSON: string(rawPayload),
				Revision:    1,
				CreatedAt:   now,
				UpdatedAt:   now,
			}
			if err := tx.Create(&proj).Error; err != nil {
				return err
			}
			revision = 1
			return nil
		}

		var existing model.CanvasProject
		if err := tx.First(&existing, "id = ? AND user_id = ?", canvasID, userID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if strings.TrimSpace(existing.Title) != "" {
			canvasTitle = existing.Title
		}
		var doc map[string]any
		if strings.TrimSpace(existing.PayloadJSON) != "" {
			if err := json.Unmarshal([]byte(existing.PayloadJSON), &doc); err != nil {
				doc = map[string]any{}
			}
		} else {
			doc = map[string]any{}
		}
		nodes, _ := doc["nodes"].([]any)
		pos := computeNextNodePosition(nodes)
		node := map[string]any{
			"id":       nodeID,
			"type":     "text",
			"title":    nodeTitle,
			"position": pos,
			"width":    420,
			"height":   320,
			"status":   "idle",
			"metadata": map[string]any{
				"content":     fullContent,
				"intelSource": intelSourceMeta,
			},
		}
		nodes = append(nodes, node)
		doc["id"] = canvasID
		doc["title"] = canvasTitle
		doc["nodes"] = nodes
		if doc["connections"] == nil {
			doc["connections"] = []any{}
		}
		if doc["viewport"] == nil {
			doc["viewport"] = map[string]any{"x": 0, "y": 0, "k": 1}
		}
		doc["updatedAt"] = now.Format(time.RFC3339Nano)
		rawPayload, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		nextRev := existing.Revision + 1
		if nextRev <= 0 {
			nextRev = 1
		}
		upd := tx.Model(&model.CanvasProject{}).
			Where("id = ? AND user_id = ? AND revision = ?", existing.ID, userID, existing.Revision).
			Updates(map[string]any{
				"title":        canvasTitle,
				"payload_json": string(rawPayload),
				"updated_at":   now,
				"revision":     nextRev,
			})
		if upd.Error != nil {
			return upd.Error
		}
		if upd.RowsAffected != 1 {
			return ErrRevisionConflict
		}
		revision = nextRev
		return nil
	})
	if err != nil {
		return HandoffToCanvasResult{}, err
	}

	convID := ensureHandoffCanvasConversation(db, userID, canvasID, nodeID, nodeTitle, kind, in.AgentPrompt, now)

	openAgent := true
	if in.OpenAgent != nil {
		openAgent = *in.OpenAgent
	}
	q := url.Values{}
	if openAgent {
		q.Set("agent", "1")
	}
	q.Set("intelNodeId", nodeID)
	if prompt := strings.TrimSpace(in.AgentPrompt); prompt != "" {
		q.Set("intelPrompt", prompt)
	}
	canvasURL := "/canvas/" + url.PathEscape(canvasID)
	if enc := q.Encode(); enc != "" {
		canvasURL += "?" + enc
	}

	return HandoffToCanvasResult{
		CanvasID:       canvasID,
		CanvasTitle:    canvasTitle,
		NodeID:         nodeID,
		NodeTitle:      nodeTitle,
		ConversationID: convID,
		CanvasURL:      canvasURL,
		CreatedCanvas:  createdCanvas,
		Revision:       revision,
	}, nil
}

func ensureHandoffCanvasConversation(db *gorm.DB, userID, canvasID, nodeID, nodeTitle, kind, agentPrompt string, now time.Time) string {
	sidebarConvID := "handoff_" + nodeID
	nowISO := now.Format(time.RFC3339Nano)
	convTitle := truncRunes("情报投送："+nodeTitle, 48)
	summaryMsg := fmt.Sprintf("已从情报工作台将%s《%s》投送至当前画布文本节点（ID: `%s`）。", handoffKindLabel(kind), nodeTitle, nodeID)
	if p := strings.TrimSpace(agentPrompt); p != "" {
		summaryMsg += "\n\n建议创作指令：" + p
	}
	msgID := "msg_" + nodeID
	newConvEntry := map[string]any{
		"id":             sidebarConvID,
		"title":          convTitle,
		"permissionMode": "request_approval",
		"run":            nil,
		"createdAt":      nowISO,
		"updatedAt":      nowISO,
		"messages": []any{
			map[string]any{
				"id":   msgID,
				"role": "assistant",
				"text": summaryMsg,
				"meta": "intelligence_handoff",
			},
		},
	}

	var docRow model.FlowCanvasConversationDoc
	err := db.First(&docRow, "user_id = ? AND canvas_id = ?", userID, canvasID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		root := map[string]any{
			"version":       1,
			"activeId":      sidebarConvID,
			"conversations": []any{newConvEntry},
		}
		raw, _ := json.Marshal(root)
		_ = db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.FlowCanvasConversationDoc{
			UserID:    userID,
			CanvasID:  canvasID,
			Document:  string(raw),
			Version:   1,
			UpdatedAt: now,
		}).Error
	} else if err == nil {
		var root map[string]any
		if json.Unmarshal([]byte(nonEmpty(docRow.Document)), &root) != nil || root == nil {
			root = map[string]any{"version": 1}
		}
		list, _ := root["conversations"].([]any)
		list = append([]any{newConvEntry}, list...)
		root["version"] = 1
		root["activeId"] = sidebarConvID
		root["conversations"] = list
		raw, _ := json.Marshal(root)
		_ = db.Model(&model.FlowCanvasConversationDoc{}).
			Where("user_id = ? AND canvas_id = ? AND version = ?", userID, canvasID, docRow.Version).
			Updates(map[string]any{"document": string(raw), "version": docRow.Version + 1, "updated_at": now}).Error
	}

	_ = MirrorCanvasConversations(db, userID, canvasID, []CanvasConv{
		{
			ID:    sidebarConvID,
			Title: convTitle,
			Messages: []CanvasConvMessage{
				{ID: msgID, Role: "assistant", Text: summaryMsg},
			},
		},
	})
	return CanvasConversationIndexID(userID, canvasID, sidebarConvID)
}

func parseHandoffCitationsArg(v any) []HandoffCitation {
	arr, ok := v.([]any)
	if !ok {
		if typed, ok := v.([]HandoffCitation); ok {
			return typed
		}
		return nil
	}
	out := make([]HandoffCitation, 0, len(arr))
	for _, item := range arr {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		c := HandoffCitation{
			DocumentID: firstNonEmpty(argStr(m, "document_id"), argStr(m, "documentId")),
			Title:      argStr(m, "title"),
			URL:        argStr(m, "url"),
			Snippet:    argStr(m, "snippet"),
		}
		if start, ok := asFloat(m["char_start"]); ok {
			c.CharStart = int(start)
		}
		if end, ok := asFloat(m["char_end"]); ok {
			c.CharEnd = int(end)
		}
		if c.DocumentID != "" || c.Title != "" || c.Snippet != "" || c.URL != "" {
			out = append(out, c)
		}
	}
	return out
}

func registerCanvasHandoffRoutes(g *gin.RouterGroup, db *gorm.DB, scoped func(*gin.Context) (Scope, *model.User, bool)) {
	g.POST("/canvas-handoff", func(c *gin.Context) {
		db := reqDB(c, db)
		s, u, ok := scoped(c)
		if !ok {
			return
		}
		if !hasScope(s.Role, "intel.documents.read") {
			reply(c, http.StatusForbidden, nil, "forbidden")
			return
		}
		var in HandoffToCanvasInput
		if err := c.ShouldBindJSON(&in); err != nil {
			reply(c, http.StatusBadRequest, nil, "bad request")
			return
		}
		out, err := HandoffToCanvas(db, s, u.ID, in, time.Now().UTC())
		switch {
		case errors.Is(err, ErrInvalid):
			reply(c, http.StatusBadRequest, nil, err.Error())
		case errors.Is(err, ErrNotFound):
			reply(c, http.StatusNotFound, nil, "canvas not found")
		case errors.Is(err, ErrForbidden):
			reply(c, http.StatusForbidden, nil, "forbidden")
		case errors.Is(err, ErrRevisionConflict):
			reply(c, http.StatusConflict, nil, "canvas revision conflict")
		case err != nil:
			reply(c, http.StatusInternalServerError, nil, "handoff failed")
		default:
			status := http.StatusOK
			if out.CreatedCanvas {
				status = http.StatusCreated
			}
			reply(c, status, out, "ok")
		}
	})
}

func init() {
	Catalog = append(Catalog, ToolSpec{
		Name:          "intel_handoff_to_canvas",
		Description:   "Hand off an intelligence report, brief, topic, or document to a new or existing Canvas as a structured text source node with evidence citations, returning canvas_id, node_id, and canvas_url for visual/storyboard creation.",
		Class:         "write",
		Scope:         "intel.documents.read",
		NeedsApproval: false,
		Timeout:       5 * time.Second,
		OutputCap:     4000,
		CostBasis:     "free: platform canvas write (no model or provider cost)",
		Parameters: obj([]string{"title", "content"}, map[string]any{
			"title":        tStr,
			"content":      tStr,
			"kind":         map[string]any{"type": "string", "enum": []string{"report", "brief", "topic", "document", "analysis"}},
			"source_id":    tStr,
			"canvas_id":    tStr,
			"canvas_title": tStr,
			"agent_prompt": tStr,
			"citations":    map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		}),
		local: func(t Tools, _ context.Context, _ model.FlowAgentConversation, ss SubjectScope, userID string, a map[string]any) (any, error) {
			in := HandoffToCanvasInput{
				CanvasID:    argStr(a, "canvas_id"),
				CanvasTitle: argStr(a, "canvas_title"),
				Kind:        argStr(a, "kind"),
				SourceID:    argStr(a, "source_id"),
				Title:       argStr(a, "title"),
				Content:     argStr(a, "content"),
				AgentPrompt: argStr(a, "agent_prompt"),
				Citations:   parseHandoffCitationsArg(a["citations"]),
			}
			if in.SourceID == "" && ss.TopicID != "" {
				in.SourceID = ss.TopicID
				if in.Kind == "" {
					in.Kind = "topic"
				}
			}
			return HandoffToCanvas(t.DB, ss.Scope, userID, in, t.now())
		},
	})
	topicTools["intel_handoff_to_canvas"] = true
}
