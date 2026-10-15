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
	"slices"
	"strings"
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
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	Class         string        `json:"class"` // read | write
	Scope         string        `json:"scope"` // required Engine scope
	NeedsApproval bool          `json:"needsApproval"`
	Timeout       time.Duration `json:"-"`
	OutputCap     int           `json:"outputCap"`
	// CostBasis (§8.3 成本口径) states how a call is metered and billed.
	CostBasis  string         `json:"costBasis"`
	Parameters map[string]any `json:"parameters"`
	route      func(args map[string]any) (method, path string, body any)
	// prepare (optional) rewrites args before routing, e.g. citation validation
	// or skill pinning. An error fails the call with a model-readable message.
	prepare func(t Tools, ctx context.Context, conv model.FlowAgentConversation, ss SubjectScope, userID string, args map[string]any) (map[string]any, error)
	// local (optional) answers without calling the Engine.
	local func(t Tools, ctx context.Context, conv model.FlowAgentConversation, ss SubjectScope, userID string, args map[string]any) (any, error)
	// after (optional) post-processes a successful engine output (e.g. LLM synthesis).
	after func(t Tools, ctx context.Context, ss SubjectScope, callID string, args map[string]any, out json.RawMessage) (json.RawMessage, error)
	// rawArgs skips catalog argument validation (MCP tools: the remote server validates).
	rawArgs bool
}

func obj(required []string, props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

var (
	tStr  = map[string]any{"type": "string"}
	tInt  = map[string]any{"type": "integer"}
	tStrs = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	tObj  = map[string]any{"type": "object"}
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
		Parameters: obj([]string{"keywords", "source_ids", "interval_seconds"}, map[string]any{"name": tStr, "keywords": tStrs, "source_ids": tStrs, "interval_seconds": tInt, "timezone": tStr, "misfire_policy": map[string]any{"type": "string", "enum": []string{"coalesce", "skip", "catch_up"}}}),
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
	ErrToolUnknown      = errors.New("flow: unknown tool")
	ErrToolPermission   = errors.New("flow: permission denied for tool")
	ErrToolArgs         = errors.New("flow: invalid tool arguments")
	ErrToolConflict     = errors.New("flow: tool call id reused with different arguments")
	ErrApprovalRequired = errors.New("flow: tool requires approval through the agent run")
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
		case "object":
			_, okType = v.(map[string]any)
		case "array":
			arr, isArr := v.([]any)
			okType = isArr
			itemType := "string"
			if it, ok := p["items"].(map[string]any); ok {
				itemType, _ = it["type"].(string)
			}
			for _, x := range arr {
				switch itemType {
				case "string":
					if _, s := x.(string); !s {
						okType = false
					}
				case "object":
					if _, o := x.(map[string]any); !o {
						okType = false
					}
				}
			}
		}
		if !okType {
			return fmt.Errorf("%w: %s must be %v", ErrToolArgs, k, p["type"])
		}
		if enum, ok := p["enum"].([]string); ok && !slices.Contains(enum, fmt.Sprint(v)) {
			return fmt.Errorf("%w: %s must be one of %v", ErrToolArgs, k, enum)
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
	// Value is the in-process result for adapters whose tools return structured
	// values (canvas); it is never serialized.
	Value any `json:"-"`
}

// Tools executes intel_* tools for a conversation.
type Tools struct {
	DB     *gorm.DB
	Engine EngineCaller
	Now    func() time.Time
	// RunID is the agent run driving the tools (research parent, skill pins).
	RunID string
	// MCP is the gateway for mcp_* tools (nil = MCP disabled).
	MCP *MCPGateway
	// Model is the platform Model Gateway for research synthesis (nil = extractive only).
	Model ModelGateway
}

func (t Tools) runKey(conv model.FlowAgentConversation) string {
	if t.RunID != "" {
		return t.RunID
	}
	return "conv:" + conv.ID
}

func (t Tools) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now().UTC()
}

// Invoke runs (or replays) a tool call. callID is the model's tool_call id,
// unique per conversation; replay with identical args returns the stored result.
// approvalID must be the id of an approval already granted through the Pi
// approval chain for tools that need approval; it is empty otherwise.
func (t Tools) Invoke(ctx context.Context, conv model.FlowAgentConversation, ss SubjectScope, userID, callID, name string, args map[string]any, approvalID string) (ToolResult, error) {
	spec, ok := FindTool(name)
	if !ok {
		spec, ok = t.mcpSpec(ss.Scope, name)
	}
	if !ok {
		return ToolResult{}, ErrToolUnknown
	}
	if !ToolAllowedForSubject(ss.Subject.Kind, name) {
		return ToolResult{}, ErrToolNotInSubjectSet
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
	if !spec.rawArgs {
		if err := validateArgs(spec, args); err != nil {
			return ToolResult{}, err
		}
	}
	if spec.NeedsApproval && approvalID == "" {
		return ToolResult{}, ErrApprovalRequired
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
		if prior.Status == "unknown" && spec.local != nil {
			// Unknown outcome: re-enter the executor, which reconciles before any retry.
			out, err := spec.local(t, context.WithValue(ctx, callKeyCtx{}, "tool:"+conv.ID+":"+callID), conv, ss, userID, args)
			return t.finish(prior, localResult(prior, spec, out, err), args)
		}
		r.Replayed = true
		return r, nil
	}
	idem := "tool:" + conv.ID + ":" + callID
	if spec.NeedsApproval {
		row.ApprovalID = approvalID
		t.DB.Model(&row).Update("approval_id", approvalID)
		exec := map[string]any{}
		for k, v := range args {
			exec[k] = v
		}
		exec["approval_id"] = approvalID
		args, idem = exec, "approval:"+approvalID
	}
	if spec.prepare != nil {
		prepared, err := spec.prepare(t, ctx, conv, ss, userID, args)
		if err != nil {
			return t.finish(row, ToolResult{CallID: row.CallID, Tool: row.ToolName, Status: "failed", Error: err.Error()}, args)
		}
		args = prepared
	}
	if spec.local != nil {
		out, err := spec.local(t, context.WithValue(ctx, callKeyCtx{}, idem), conv, ss, userID, args)
		return t.finish(row, localResult(row, spec, out, err), args)
	}
	return t.execute(ctx, row, spec, ss, userID, args, idem)
}

func (t Tools) finish(row model.FlowAgentToolCall, result ToolResult, args map[string]any) (ToolResult, error) {
	stored, _ := json.Marshal(result)
	err := t.DB.Model(&model.FlowAgentToolCall{}).Where("id = ?", row.ID).
		Updates(map[string]any{"status": result.Status, "result": string(stored), "updated_at": t.now()}).Error
	return result, err
}

func (t Tools) execute(ctx context.Context, row model.FlowAgentToolCall, spec ToolSpec, ss SubjectScope, userID string, args map[string]any, idem string) (ToolResult, error) {
	if id, _ := args["operation_id"].(string); strings.HasPrefix(id, "mt_") && spec.Name == "intel_get_operation" {
		// Platform model task (async synthesis), not an engine operation.
		op, err := ModelTaskOperation(t.DB, ss.Scope.TenantID, id)
		result := ToolResult{CallID: row.CallID, Tool: row.ToolName, Status: "succeeded"}
		if err != nil {
			result.Status, result.Error = "failed", "operation not found"
		} else {
			raw, _ := json.Marshal(op)
			result.Output = capJSON(raw, spec.OutputCap, &result.Truncated)
		}
		return t.finish(row, result, args)
	}
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
		var env struct {
			Msg string `json:"msg"`
		}
		if json.Unmarshal(out, &env) == nil && env.Msg != "" {
			result.Error += ": " + env.Msg
		}
		result.Output = capJSON(out, spec.OutputCap, &result.Truncated)
	default:
		result.Status = "succeeded"
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(out, &env) == nil && len(env.Data) > 0 {
			out = env.Data
		}
		if spec.after != nil {
			if post, perr := spec.after(t, ctx, ss, row.CallID, args, out); perr == nil {
				out = post
			} else {
				out = appendField(out, "synthesis_error", perr.Error())
			}
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
		t.recordEvidence(ctx, ss, userID, row, result.Output)
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

// IsTool reports whether name is an intel_* tool served by this package.
func IsTool(name string) bool {
	_, ok := FindTool(name)
	return ok || IsMCPToolName(name)
}

// mcpSpec synthesizes a spec for an approved MCP tool visible in this scope.
func (t Tools) mcpSpec(scope Scope, name string) (ToolSpec, bool) {
	if t.MCP == nil || !IsMCPToolName(name) {
		return ToolSpec{}, false
	}
	conn, tool, ok := t.MCP.ResolveAgentTool(scope, name)
	if !ok {
		return ToolSpec{}, false
	}
	return ToolSpec{Name: name, Class: "write", Scope: "intel.mcp.use", Timeout: time.Duration(conn.TimeoutMS) * time.Millisecond, OutputCap: conn.OutputCap, rawArgs: true, CostBasis: "mcp_call: per call (flow_mcp_usages) + reservation against the MCP connection budget",
		local: func(t Tools, ctx context.Context, _ model.FlowAgentConversation, ss SubjectScope, userID string, args map[string]any) (any, error) {
			key, _ := ctx.Value(callKeyCtx{}).(string)
			out, truncated, err := t.MCP.ExecuteKeyed(ctx, ss.Scope, userID, conn.ID, tool, args, key)
			if err != nil {
				return nil, err
			}
			return map[string]any{"connection": conn.Name, "tool": tool, "text": out, "truncated": truncated}, nil
		}}, true
}

// recordEvidence persists evidence rows in Loom Engine for search hits and
// read windows (document, revision, char span, quote). Best effort; keyed by
// the tool call so replays do not duplicate rows.
func (t Tools) recordEvidence(ctx context.Context, ss SubjectScope, userID string, row model.FlowAgentToolCall, out json.RawMessage) {
	var items []map[string]any
	switch row.ToolName {
	case "intel_search_documents":
		var r struct {
			Items []struct {
				DocumentID string `json:"document_id"`
				Revision   int    `json:"revision"`
				CharStart  int    `json:"char_start"`
				CharEnd    int    `json:"char_end"`
				Snippet    string `json:"snippet"`
			} `json:"items"`
		}
		if json.Unmarshal(out, &r) != nil {
			return
		}
		for _, it := range r.Items {
			items = append(items, map[string]any{"document_id": it.DocumentID, "revision": it.Revision, "char_start": it.CharStart, "char_end": it.CharEnd, "quote": it.Snippet})
		}
	case "intel_read_document":
		var r struct {
			DocumentID string `json:"document_id"`
			Revision   int    `json:"revision"`
			Offset     int    `json:"offset"`
			Text       string `json:"text"`
		}
		if json.Unmarshal(out, &r) != nil || r.DocumentID == "" {
			return
		}
		items = append(items, map[string]any{"document_id": r.DocumentID, "revision": r.Revision, "char_start": r.Offset, "char_end": r.Offset + len([]rune(r.Text)), "quote": truncRunes(r.Text, 500)})
	default:
		return
	}
	if len(items) == 0 {
		return
	}
	body, _ := json.Marshal(map[string]any{"items": items, "created_by": "agent:" + row.ConversationID + ":" + row.CallID})
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, _ = t.Engine.Do(cctx, ss.Scope, Actor{Type: "user", ID: userID}, http.MethodPost, "/v1/evidence", "evidence:"+row.ConversationID+":"+row.CallID, body)
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// RecordConfirmedPlan stores the latest confirmed plan on the conversation.
func RecordConfirmedPlan(db *gorm.DB, convID, runID string, plan any) error {
	entry, _ := json.Marshal([]any{map[string]any{"runId": runID, "items": plan, "confirmedAt": time.Now().UTC()}})
	return db.Model(&model.FlowAgentConversation{}).Where("id = ?", convID).Update("confirmed_plans", string(entry)).Error
}

func appendField(raw json.RawMessage, k string, v any) json.RawMessage {
	m := map[string]any{}
	if json.Unmarshal(raw, &m) != nil {
		return raw
	}
	m[k] = v
	b, _ := json.Marshal(m)
	return b
}

type callKeyCtx struct{}

func localResult(row model.FlowAgentToolCall, spec ToolSpec, out any, err error) ToolResult {
	r := ToolResult{CallID: row.CallID, Tool: row.ToolName, Status: "succeeded"}
	switch {
	case errors.Is(err, ErrMCPUnknownOutcome):
		r.Status, r.Error = "unknown", err.Error()
	case err != nil:
		r.Status, r.Error = "failed", err.Error()
	default:
		raw, _ := json.Marshal(out)
		r.Output = capJSON(raw, spec.OutputCap, &r.Truncated)
	}
	return r
}
