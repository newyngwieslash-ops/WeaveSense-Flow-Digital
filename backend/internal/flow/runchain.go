package flow

import (
	"context"
	"fmt"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

// PRD §8.1: every agent run is assembled from five minimal interfaces. Two
// adapters implement them: the intelligence_workspace / topic adapter below
// (used by the Pi run in the app package and by the REST agent routes), and the
// canvas adapter in the app package (app/cloud_agent_runchain.go), which owns the
// canvas catalog. The Pi run picks the adapter by subject and calls only the
// chain for scope, context, tool table and tool execution.
// Adapter-specific data travels in the Payload fields.

// ScopeResolver turns an authenticated user + AgentSubject into tenant/workspace/role.
type ScopeResolver interface {
	ResolveSubject(db *gorm.DB, userID string, s AgentSubject) (SubjectScope, error)
}

// ContextProvider assembles the server-side turn context for a conversation.
type ContextProvider interface {
	TurnContext(db *gorm.DB, conv model.FlowAgentConversation, ss SubjectScope) (TurnContext, error)
}

// ToolRequest describes what a run may see: subject kind, permission mode and
// any per-run snapshot of approved MCP tools.
type ToolRequest struct {
	SubjectKind    string
	PermissionMode string
	Scope          Scope
	MCPTools       []map[string]any
	// Payload carries adapter-specific input (the canvas adapter passes the run request).
	Payload any
}

// ToolProvider lists the function tools offered to the model for a run.
type ToolProvider interface {
	Tools(req ToolRequest) []map[string]any
}

// ToolCall is one model-issued tool call bound to its conversation and run.
type ToolCall struct {
	Conv       model.FlowAgentConversation
	Subject    SubjectScope
	UserID     string
	RunID      string
	CallID     string
	Name       string
	Args       map[string]any
	ApprovalID string
	// Payload carries adapter-specific execution state (canvas: run checkpoint, policy, prepared reads).
	Payload any
}

// ToolExecutor executes a tool call (idempotent per call id, approvals enforced).
type ToolExecutor interface {
	Execute(ctx context.Context, db *gorm.DB, call ToolCall) (ToolResult, error)
}

// UsageRecorder writes metered usage once per (tenant, idempotency key).
type UsageRecorder interface {
	Record(db *gorm.DB, in UsageInput, now time.Time) (model.FlowUsageRecord, error)
}

// RunChain bundles the five interfaces for one subject adapter.
type RunChain struct {
	Kind    string
	Scope   ScopeResolver
	Context ContextProvider
	Tools   ToolProvider
	Exec    ToolExecutor
	Usage   UsageRecorder
}

// ---- intelligence_workspace / topic adapter ----

type subjectScopeResolver struct{}

func (subjectScopeResolver) ResolveSubject(db *gorm.DB, userID string, s AgentSubject) (SubjectScope, error) {
	return ResolveSubject(db, userID, s)
}

type flowContextProvider struct{}

func (flowContextProvider) TurnContext(db *gorm.DB, conv model.FlowAgentConversation, ss SubjectScope) (TurnContext, error) {
	return BuildTurnContext(db, conv, ss)
}

// IntelToolProvider offers intel_* tools (read-only runs get read tools only; topic
// subjects get the topic allow-list) plus the run's approved MCP tools.
type IntelToolProvider struct {
	// Base are adapter-specific leading tools (the app adds plan_update).
	Base []map[string]any
}

func (p IntelToolProvider) Tools(req ToolRequest) []map[string]any {
	out := append([]map[string]any{}, p.Base...)
	for _, t := range PiFunctionTools() {
		name := t["function"].(map[string]any)["name"].(string)
		if req.PermissionMode == "read_only" {
			if spec, _ := FindTool(name); spec.Class != "read" {
				continue
			}
		}
		if ToolAllowedForSubject(req.SubjectKind, name) {
			out = append(out, t)
		}
	}
	if req.PermissionMode != "read_only" {
		out = append(out, req.MCPTools...)
	}
	return out
}

// IntelToolExecutor runs calls through Tools.Invoke on the request's DB handle.
type IntelToolExecutor struct{ Tools Tools }

func (e IntelToolExecutor) Execute(ctx context.Context, db *gorm.DB, c ToolCall) (ToolResult, error) {
	t := e.Tools
	if c.RunID != "" {
		t.RunID = c.RunID
	}
	if db != nil {
		t = t.withDB(db)
	}
	if t.RunID != "" && t.DB != nil {
		// The run's frozen tool table is authoritative (capability snapshot).
		if err := CheckRunTool(t.DB, t.RunID, c.Name); err != nil {
			return ToolResult{CallID: c.CallID, Tool: c.Name, Status: "failed"}, err
		}
		if snap, err := RunCapability(t.DB, t.RunID); err == nil {
			ctx = WithCapabilitySnapshot(ctx, snap.ID)
		}
	}
	return t.Invoke(ctx, c.Conv, c.Subject, c.UserID, c.CallID, c.Name, c.Args, c.ApprovalID)
}

// LedgerUsageRecorder is the default UsageRecorder (frozen price version, idempotent).
type LedgerUsageRecorder struct{}

func (LedgerUsageRecorder) Record(db *gorm.DB, in UsageInput, now time.Time) (model.FlowUsageRecord, error) {
	return RecordUsage(db, in, now)
}

// DefaultUsageRecorder is used by the gateways (MCP, notifications, engine usage events).
var DefaultUsageRecorder UsageRecorder = LedgerUsageRecorder{}

// IntelRunChain is the intelligence_workspace / topic adapter.
func IntelRunChain(tools Tools, base []map[string]any) RunChain {
	return RunChain{Kind: SubjectWorkspace, Scope: subjectScopeResolver{}, Context: flowContextProvider{},
		Tools: IntelToolProvider{Base: base}, Exec: IntelToolExecutor{Tools: tools}, Usage: DefaultUsageRecorder}
}

// Validate reports a missing interface (a chain must be complete to run).
func (c RunChain) Validate() error {
	if c.Scope == nil || c.Context == nil || c.Tools == nil || c.Exec == nil || c.Usage == nil {
		return fmt.Errorf("flow: run chain %q is incomplete", c.Kind)
	}
	return nil
}
