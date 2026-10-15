package flow

import (
	"errors"
	"context"
	"testing"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

// Compile-time: the intelligence adapter implements all five §8.1 interfaces.
var (
	_ ScopeResolver   = subjectScopeResolver{}
	_ ContextProvider = flowContextProvider{}
	_ ToolProvider    = IntelToolProvider{}
	_ ToolExecutor    = IntelToolExecutor{}
	_ UsageRecorder   = LedgerUsageRecorder{}
)

type countingRecorder struct{ n int }

func (c *countingRecorder) Record(db *gorm.DB, in UsageInput, now time.Time) (model.FlowUsageRecord, error) {
	c.n++
	return RecordUsage(db, in, now)
}

func TestRunChainIntelAdapterDrivesScopeContextToolsExecAndUsage(t *testing.T) {
	tools, convs, _, conv, ss := setupAgent(t)
	chain := IntelRunChain(tools, []map[string]any{{"type": "function", "function": map[string]any{"name": "plan_update"}}})
	if err := chain.Validate(); err != nil {
		t.Fatal(err)
	}
	if (RunChain{Kind: "x"}).Validate() == nil {
		t.Fatal("an incomplete chain must not validate")
	}
	got, err := chain.Scope.ResolveSubject(tools.DB, "u_alice", AgentSubject{Kind: SubjectWorkspace, ID: ss.Scope.WorkspaceID})
	if err != nil || got.Scope.TenantID != ss.Scope.TenantID {
		t.Fatalf("scope: %+v %v", got, err)
	}
	if _, err := chain.Scope.ResolveSubject(tools.DB, "u_mallory", AgentSubject{Kind: SubjectWorkspace, ID: ss.Scope.WorkspaceID}); err == nil {
		t.Fatal("another user's workspace must not resolve")
	}
	names := func(req ToolRequest) map[string]bool {
		out := map[string]bool{}
		for _, t := range chain.Tools.Tools(req) {
			out[t["function"].(map[string]any)["name"].(string)] = true
		}
		return out
	}
	all := names(ToolRequest{SubjectKind: SubjectWorkspace, MCPTools: []map[string]any{{"function": map[string]any{"name": "mcp_x_lookup"}}}})
	if !all["plan_update"] || !all["intel_apply_monitor"] || !all["mcp_x_lookup"] {
		t.Fatalf("workspace tools: %v", all)
	}
	ro := names(ToolRequest{SubjectKind: SubjectWorkspace, PermissionMode: "read_only", MCPTools: []map[string]any{{"function": map[string]any{"name": "mcp_x_lookup"}}}})
	if ro["intel_apply_monitor"] || ro["mcp_x_lookup"] || !ro["intel_search_documents"] {
		t.Fatalf("read_only tools: %v", ro)
	}
	topic := names(ToolRequest{SubjectKind: SubjectTopic})
	for n := range topic {
		if n != "plan_update" && !ToolAllowedForSubject(SubjectTopic, n) {
			t.Fatalf("topic offered %s", n)
		}
	}
	convs.Append(conv, model.FlowAgentMessage{Role: "user", Content: "lithium"})
	tc, err := chain.Context.TurnContext(tools.DB, conv, ss)
	if err != nil || len(tc.Recent) == 0 {
		t.Fatalf("context: %+v %v", tc, err)
	}
	// §9.2 fail-closed: a run without a frozen snapshot cannot execute tools.
	if _, err := chain.Exec.Execute(context.Background(), tools.DB, ToolCall{Conv: conv, Subject: ss, UserID: "u_alice", RunID: "run_nosnap", CallID: "c0", Name: "intel_search_documents", Args: map[string]any{"query": "lithium"}}); !errors.Is(err, ErrNoCapabilitySnapshot) {
		t.Fatalf("run without snapshot must be rejected, got %v", err)
	}
	if _, err := FreezeCapabilitySnapshot(tools.DB, CapabilityInput{TenantID: ss.Scope.TenantID, RunID: "run_1", SubjectKind: SubjectWorkspace,
		Tools: chain.Tools.Tools(ToolRequest{SubjectKind: SubjectWorkspace})}, time.Now()); err != nil {
		t.Fatal(err)
	}
	r, err := chain.Exec.Execute(context.Background(), tools.DB, ToolCall{Conv: conv, Subject: ss, UserID: "u_alice", RunID: "run_1", CallID: "c1", Name: "intel_search_documents", Args: map[string]any{"query": "lithium"}})
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("exec: %+v %v", r, err)
	}
	// Gateways record usage through the replaceable UsageRecorder.
	rec := &countingRecorder{}
	prev := DefaultUsageRecorder
	DefaultUsageRecorder = rec
	t.Cleanup(func() { DefaultUsageRecorder = prev })
	if _, err := DefaultUsageRecorder.Record(tools.DB, UsageInput{TenantID: ss.Scope.TenantID, UsageType: UsageMCPCalls, Quantity: 1, IdemKey: "k1"}, time.Now()); err != nil || rec.n != 1 {
		t.Fatalf("usage recorder: %d %v", rec.n, err)
	}
}
