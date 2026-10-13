package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EngineCaller is satisfied by EngineClient (and by fakes in tests).
type EngineCaller interface {
	Do(ctx context.Context, scope Scope, actor Actor, method, path, idemKey string, body []byte) (int, []byte, error)
}

// ToolSpec describes an intel_* tool for the Pi/Go tool bridge.
type ToolSpec struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	Class         string         `json:"class"` // read | write
	Scope         string         `json:"scope"` // required Engine scope
	NeedsApproval bool           `json:"needsApproval"`
	Timeout       time.Duration  `json:"-"`
	OutputCap     int            `json:"outputCap"`
	Parameters    map[string]any `json:"parameters"`
	route         func(args map[string]any) (method, path string, body any)
}

func obj(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

var (
	tStr  = map[string]any{"type": "string"}
	tInt  = map[string]any{"type": "integer"}
	tStrs = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
)

func opPath(args map[string]any, suffix string) string {
	return "/v1/operations/" + url.PathEscape(fmt.Sprint(args["operation_id"])) + suffix
}

// Catalog is the fixed P2 tool set.
var Catalog = []ToolSpec{
	{Name: "intel_search_documents", Description: "Full-text search collected materials in the current workspace (filters: sources, time window). Paged.",
		Class: "read", Scope: "intel.documents.read", Timeout: 10 * time.Second, OutputCap: 16000,
		Parameters: obj([]string{"query"}, map[string]any{"query": tStr, "source_ids": tStrs, "from": tStr, "to": tStr, "limit": tInt, "offset": tInt}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/documents/search", a }},
	{Name: "intel_read_document", Description: "Read a document by chunk/offset. Returns coverage (truncated, next_offset).",
		Class: "read", Scope: "intel.documents.read", Timeout: 10 * time.Second, OutputCap: 24000,
		Parameters: obj([]string{"document_id"}, map[string]any{"document_id": tStr, "revision": tInt, "offset": tInt, "max_chars": tInt}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/documents/read", a }},
	{Name: "intel_preview_monitor", Description: "Preview a monitor (keywords, sources, interval, timezone) with run counts. Does not write configuration.",
		Class: "read", Scope: "intel.sources.read", Timeout: 10 * time.Second, OutputCap: 8000,
		Parameters: obj([]string{"keywords", "source_ids", "interval_seconds"}, map[string]any{"name": tStr, "keywords": tStrs, "source_ids": tStrs, "interval_seconds": tInt, "timezone": tStr}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/monitors/preview", a }},
	{Name: "intel_apply_monitor", Description: "Save a previewed monitor. Requires user approval; runs only after approval.",
		Class: "write", Scope: "intel.monitors.write", NeedsApproval: true, Timeout: 15 * time.Second, OutputCap: 8000,
		Parameters: obj([]string{"preview_id"}, map[string]any{"preview_id": tStr}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/monitors", a }},
	{Name: "intel_collect_once", Description: "Request one collection run for sources. Returns operation_id and job ids (async).",
		Class: "write", Scope: "intel.collect", Timeout: 15 * time.Second, OutputCap: 8000,
		Parameters: obj([]string{"source_ids"}, map[string]any{"source_ids": tStrs}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodPost, "/v1/collection-runs", a }},
	{Name: "intel_get_operation", Description: "Get authoritative status of an operation. Read-only, no model calls.",
		Class: "read", Scope: "intel.operations.read", Timeout: 10 * time.Second, OutputCap: 8000,
		Parameters: obj([]string{"operation_id"}, map[string]any{"operation_id": tStr}),
		route:      func(a map[string]any) (string, string, any) { return http.MethodGet, opPath(a, ""), nil }},
	{Name: "intel_cancel_operation", Description: "Cancel a queued/running operation owned by this workspace.",
		Class: "write", Scope: "intel.collect", Timeout: 10 * time.Second, OutputCap: 4000,
		Parameters: obj([]string{"operation_id"}, map[string]any{"operation_id": tStr, "reason": tStr}),
		route: func(a map[string]any) (string, string, any) {
			return http.MethodPost, opPath(a, "/cancel"), map[string]any{}
		}},
}

func FindTool(name string) (ToolSpec, bool) {
	for _, t := range Catalog {
		if t.Name == name {
			return t, true
		}
	}
	return ToolSpec{}, false
}

// PiFunctionTools renders the catalog in the OpenAI/Pi function-tool shape
// used by the canonical agent request.
func PiFunctionTools() []map[string]any {
	out := make([]map[string]any, 0, len(Catalog))
	for _, t := range Catalog {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": t.Parameters}})
	}
	return out
}

var (
	ErrToolUnknown    = errors.New("flow: unknown tool")
	ErrToolPermission = errors.New("flow: permission denied for tool")
	ErrToolArgs       = errors.New("flow: invalid tool arguments")
	ErrToolConflict   = errors.New("flow: tool call id reused with different arguments")
)

func validateArgs(spec ToolSpec, args map[string]any) error {
	props, _ := spec.Parameters["properties"].(map[string]any)
	for _, r := range spec.Parameters["required"].([]string) {
		if _, ok := args[r]; !ok {
			return fmt.Errorf("%w: missing %s", ErrToolArgs, r)
		}
	}
	for k, v := range args {
		p, ok := props[k].(map[string]any)
		if !ok {
			return fmt.Errorf("%w: unknown field %s", ErrToolArgs, k)
		}
		okType := false
		switch p["type"] {
		case "string":
			_, okType = v.(string)
		case "integer":
			f, isNum := v.(float64)
			okType = isNum && f == float64(int64(f))
		case "array":
			arr, isArr := v.([]any)
			okType = isArr
			for _, x := range arr {
				if _, s := x.(string); !s {
					okType = false
				}
			}
		}
		if !okType {
			return fmt.Errorf("%w: %s must be %v", ErrToolArgs, k, p["type"])
		}
	}
	return nil
}

func hasScope(role, scope string) bool {
	for _, s := range ScopesForRole(role) {
		if s == scope {
			return true
		}
	}
	return false
}

// ToolResult is what the bridge feeds back to the model as the tool message.
type ToolResult struct {
	CallID     string          `json:"callId"`
	Tool       string          `json:"tool"`
	Status     string          `json:"status"` // succeeded | failed | waiting_approval | rejected
	ApprovalID string          `json:"approvalId,omitempty"`
	Replayed   bool            `json:"replayed,omitempty"`
	Truncated  bool            `json:"truncated,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
}

// Tools executes intel_* tools for a conversation.
type Tools struct {
	DB     *gorm.DB
	Engine EngineCaller
	Now    func() time.Time
}

func (t Tools) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now().UTC()
}

// Invoke runs (or replays) a tool call. callID is the model's tool_call id,
// unique per conversation; replay with identical args returns the stored result.
func (t Tools) Invoke(ctx context.Context, conv model.FlowAgentConversation, ss SubjectScope, userID, callID, name string, args map[string]any) (ToolResult, error) {
	spec, ok := FindTool(name)
	if !ok {
		return ToolResult{}, ErrToolUnknown
	}
	if conv.TenantID != ss.Scope.TenantID || conv.WorkspaceID != ss.Scope.WorkspaceID {
		return ToolResult{}, ErrForbidden
	}
	if !hasScope(ss.Scope.Role, spec.Scope) {
		return ToolResult{}, ErrToolPermission
	}
	if args == nil {
		args = map[string]any{}
	}
	if err := validateArgs(spec, args); err != nil {
		return ToolResult{}, err
	}
	raw, _ := json.Marshal(args)
	sum := sha256.Sum256(append([]byte(name+"\x00"), raw...))
	hash := hex.EncodeToString(sum[:])
	row := model.FlowAgentToolCall{ID: NewID("tc_"), TenantID: conv.TenantID, ConversationID: conv.ID, CallID: callID,
		ToolName: name, Class: spec.Class, RequestHash: hash, Arguments: string(raw), Status: "pending"}
	res := t.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if res.Error != nil {
		return ToolResult{}, res.Error
	}
	if res.RowsAffected == 0 {
		var prior model.FlowAgentToolCall
		if err := t.DB.First(&prior, "tenant_id = ? AND conversation_id = ? AND call_id = ?", conv.TenantID, conv.ID, callID).Error; err != nil {
			return ToolResult{}, err
		}
		if prior.RequestHash != hash {
			return ToolResult{}, ErrToolConflict
		}
		r := storedResult(prior)
		r.Replayed = true
		return r, nil
	}
	if spec.NeedsApproval {
		ap := model.FlowAgentApproval{ID: NewID("ap_"), TenantID: conv.TenantID, WorkspaceID: conv.WorkspaceID, ConversationID: conv.ID,
			ToolCallID: row.ID, ToolName: name, Summary: string(raw), Status: "pending", ExpiresAt: t.now().Add(24 * time.Hour)}
		err := t.DB.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&ap).Error; err != nil {
				return err
			}
			return tx.Model(&row).Updates(map[string]any{"status": "waiting_approval", "approval_id": ap.ID}).Error
		})
		if err != nil {
			return ToolResult{}, err
		}
		row.Status, row.ApprovalID = "waiting_approval", ap.ID
		return storedResult(row), nil
	}
	return t.execute(ctx, row, spec, ss, userID, args, "tool:"+conv.ID+":"+callID)
}

func (t Tools) execute(ctx context.Context, row model.FlowAgentToolCall, spec ToolSpec, ss SubjectScope, userID string, args map[string]any, idem string) (ToolResult, error) {
	method, path, body := spec.route(args)
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	cctx, cancel := context.WithTimeout(ctx, spec.Timeout)
	defer cancel()
	key := ""
	if spec.Class == "write" {
		key = idem
	}
	status, out, err := t.Engine.Do(cctx, ss.Scope, Actor{Type: "user", ID: userID}, method, path, key, payload)
	result := ToolResult{CallID: row.CallID, Tool: row.ToolName}
	switch {
	case err != nil:
		result.Status, result.Error = "failed", "engine unavailable"
	case status >= 400:
		result.Status, result.Error = "failed", fmt.Sprintf("engine returned %d", status)
		result.Output = capJSON(out, spec.OutputCap, &result.Truncated)
	default:
		result.Status = "succeeded"
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(out, &env) == nil && len(env.Data) > 0 {
			out = env.Data
		}
		result.Output = capJSON(out, spec.OutputCap, &result.Truncated)
	}
	stored, _ := json.Marshal(result)
	if err := t.DB.Model(&model.FlowAgentToolCall{}).Where("id = ?", row.ID).
		Updates(map[string]any{"status": result.Status, "result": string(stored), "updated_at": t.now()}).Error; err != nil {
		return result, err
	}
	if result.Status == "succeeded" {
		t.trackEvidence(row.ConversationID, row.ToolName, args, result.Output)
	}
	return result, nil
}

func capJSON(b []byte, limit int, truncated *bool) json.RawMessage {
	if len(b) <= limit {
		if json.Valid(b) {
			return b
		}
		s, _ := json.Marshal(string(b))
		return s
	}
	*truncated = true
	s, _ := json.Marshal(map[string]any{"truncated_text": string(b[:limit])})
	return s
}

func storedResult(row model.FlowAgentToolCall) ToolResult {
	var r ToolResult
	if row.Result != "" && json.Unmarshal([]byte(row.Result), &r) == nil {
		return r
	}
	return ToolResult{CallID: row.CallID, Tool: row.ToolName, Status: row.Status, ApprovalID: row.ApprovalID}
}

// Decide approves/rejects a pending write. Approval executes exactly once
// (idempotency key derived from the approval id).
func (t Tools) Decide(ctx context.Context, userID, approvalID string, approve bool) (ToolResult, error) {
	var ap model.FlowAgentApproval
	if err := t.DB.First(&ap, "id = ?", approvalID).Error; err != nil {
		return ToolResult{}, ErrNotFound
	}
	conv, ss, err := Conversations{DB: t.DB}.Get(userID, ap.ConversationID)
	if err != nil || ap.TenantID != ss.Scope.TenantID {
		return ToolResult{}, ErrNotFound
	}
	var row model.FlowAgentToolCall
	if err := t.DB.First(&row, "id = ?", ap.ToolCallID).Error; err != nil {
		return ToolResult{}, err
	}
	if ap.Status != "pending" {
		return storedResult(row), nil
	}
	now := t.now()
	status := "rejected"
	if approve {
		status = "approved"
	}
	if now.After(ap.ExpiresAt) {
		status = "expired"
	}
	upd := t.DB.Model(&model.FlowAgentApproval{}).Where("id = ? AND status = 'pending'", ap.ID).
		Updates(map[string]any{"status": status, "decided_by": userID, "decided_at": now})
	if upd.Error != nil {
		return ToolResult{}, upd.Error
	}
	if upd.RowsAffected == 0 { // concurrent decision won
		_ = t.DB.First(&row, "id = ?", row.ID).Error
		return storedResult(row), nil
	}
	if status != "approved" {
		r := ToolResult{CallID: row.CallID, Tool: row.ToolName, Status: "rejected", ApprovalID: ap.ID, Error: status}
		b, _ := json.Marshal(r)
		t.DB.Model(&row).Updates(map[string]any{"status": "rejected", "result": string(b)})
		return r, nil
	}
	spec, _ := FindTool(row.ToolName)
	if !hasScope(ss.Scope.Role, spec.Scope) {
		return ToolResult{}, ErrToolPermission
	}
	var args map[string]any
	_ = json.Unmarshal([]byte(row.Arguments), &args)
	args["approval_id"] = ap.ID
	r, err := t.execute(ctx, row, spec, ss, userID, args, "approval:"+ap.ID)
	r.ApprovalID = ap.ID
	_ = conv
	return r, err
}

// trackEvidence keeps recently searched/read documents in the conversation's
// evidence scope so follow-up turns ("that article") can be resolved.
func (t Tools) trackEvidence(convID, tool string, args map[string]any, out json.RawMessage) {
	var ids []string
	switch tool {
	case "intel_read_document":
		if s, ok := args["document_id"].(string); ok {
			ids = append(ids, s)
		}
	case "intel_search_documents":
		var r struct {
			Items []struct {
				DocumentID string `json:"document_id"`
			} `json:"items"`
		}
		if json.Unmarshal(out, &r) == nil {
			for _, it := range r.Items {
				ids = append(ids, it.DocumentID)
			}
		}
	default:
		return
	}
	_ = t.DB.Transaction(func(tx *gorm.DB) error {
		var c model.FlowAgentConversation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, "id = ?", convID).Error; err != nil {
			return err
		}
		scope := map[string]any{}
		_ = json.Unmarshal([]byte(c.EvidenceScope), &scope)
		recent, _ := scope["recent_document_ids"].([]any)
		seen := map[string]bool{}
		merged := []any{}
		for _, id := range ids {
			if !seen[id] {
				seen[id] = true
				merged = append(merged, id)
			}
		}
		for _, v := range recent {
			if s, _ := v.(string); s != "" && !seen[s] && len(merged) < 20 {
				seen[s] = true
				merged = append(merged, s)
			}
		}
		scope["recent_document_ids"] = merged
		b, _ := json.Marshal(scope)
		return tx.Model(&c).Update("evidence_scope", string(b)).Error
	})
}
