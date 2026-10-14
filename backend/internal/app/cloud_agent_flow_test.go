package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

var flowTestKey = []byte(strings.Repeat("k", 32))

type flowFakeEngine struct {
	mu    sync.Mutex
	calls map[string]int
	seen  []string // tenant ids seen
	// monitorStatus, when set, is the HTTP status for POST /v1/monitors (e.g. 410 preview expired).
	monitorStatus int
}

func newFlowFakeEngine(t *testing.T) (*flowFakeEngine, flow.EngineClient) {
	f := &flowFakeEngine{calls: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := flow.Verify(flowTestKey, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "loom-engine", time.Now())
		if err != nil {
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		f.calls[r.Method+" "+r.URL.Path]++
		f.seen = append(f.seen, ctx.TenantID)
		status := f.monitorStatus
		f.mu.Unlock()
		if status != 0 && r.URL.Path == "/v1/monitors" {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "message": "preview expired"})
			return
		}
		_, _ = io.ReadAll(r.Body)
		var data any = map[string]any{"ok": true}
		switch r.URL.Path {
		case "/v1/documents/search":
			data = map[string]any{"items": []map[string]any{{"document_id": "doc_1", "revision": 1, "snippet": "Acme signs lithium deal"}}}
		case "/v1/monitors":
			data = map[string]any{"monitor": map[string]any{"id": "mon_1"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	t.Cleanup(srv.Close)
	return f, flow.EngineClient{Cfg: flow.Config{EngineURL: srv.URL, SigningKey: flowTestKey, Issuer: "flow-platform", Audience: "loom-engine", TTL: flow.MaxTokenTTL}}
}

func flowTestSetup(t *testing.T) (*Service, *gorm.DB, *flowFakeEngine, model.FlowAgentConversation) {
	t.Helper()
	s, db, _, _ := creationTestService(t)
	s.legacyCloudAgentRootTask = false
	if err := db.AutoMigrate(&model.Tenant{}, &model.Membership{}, &model.Workspace{}, &model.WorkspaceMembership{}, &model.FlowTopic{},
		&model.FlowAgentConversation{}, &model.FlowAgentMessage{}, &model.FlowAgentToolCall{}, &model.FlowOperationProjection{}); err != nil {
		t.Fatal(err)
	}
	fe, client := newFlowFakeEngine(t)
	s.ConfigureFlowEngine(client)
	ss, err := flow.ResolveSubject(db, "user", flow.AgentSubject{Kind: flow.SubjectWorkspace, ID: ""})
	if err != nil {
		t.Fatal(err)
	}
	ss.Subject.ID = ss.Scope.WorkspaceID
	conv, err := flow.Conversations{DB: db}.Create("user", ss, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	return s, db, fe, conv
}

func flowRunRequest(conv model.FlowAgentConversation, key string) CloudAgentRequest {
	req := CloudAgentRequest{Prompt: "Track Acme lithium news", Model: "text-test", ChannelID: "channel", ChannelModelKey: "text-test",
		PermissionMode: "request_approval", IdempotencyKey: key, FlowConversationID: conv.ID,
		Subject: &flow.AgentSubject{Kind: flow.SubjectWorkspace, ID: conv.SubjectID}}
	req.Budget.MaxCredits = 1
	return req
}

func flowModelStep(t *testing.T, s *Service, db *gorm.DB, runID string, calls []map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"text": "", "toolCalls": calls})
	messages := []map[string]any{{"role": "user", "content": []any{map[string]any{"type": "text", "text": "Track Acme lithium news"}}}}
	interjectionModelStep(t, s, db, "user", runID, messages, string(raw))
}

func piToolPayload(id, name, args string) map[string]json.RawMessage {
	return map[string]json.RawMessage{"callId": json.RawMessage(`"` + id + `"`), "name": json.RawMessage(`"` + name + `"`), "arguments": json.RawMessage(args)}
}

func TestFlowAgentRunUsesIntelToolsViaPiBridge(t *testing.T) {
	s, db, fe, conv := flowTestSetup(t)
	run, err := s.CreateCloudAgentRun("user", flowRunRequest(conv, "flow-run-key-1"), "")
	if err != nil {
		t.Fatal(err)
	}
	_, state := agentInterjectionState(t, s, run.ID)
	names := map[string]bool{}
	for _, tool := range state.Canonical.Tools {
		names[stringValue(tool["function"].(map[string]any)["name"])] = true
	}
	if !names["intel_search_documents"] || !names["intel_apply_monitor"] || names["canvas_get_state"] || names["generate_media"] {
		t.Fatalf("intel run must expose intel tools only: %v", names)
	}
	foundCtx := false
	for _, m := range state.Canonical.Messages {
		if m[cloudAgentContextSourceKey] == "flow_context" {
			foundCtx = true
		}
	}
	if !foundCtx {
		t.Fatal("flow turn context missing from canonical request")
	}
	// Mocked model asks for a search; the Pi runtime forwards it to the Go bridge.
	flowModelStep(t, s, db, run.ID, []map[string]any{{"id": "call_s1", "type": "function", "function": map[string]any{"name": "intel_search_documents", "arguments": `{"query":"lithium"}`}}})
	out, err := s.cloudAgentPiTool(context.Background(), "user", run.ID, piToolPayload("call_s1", "intel_search_documents", `{"query":"lithium"}`))
	if err != nil {
		t.Fatalf("bridge tool: %v", err)
	}
	if b, _ := json.Marshal(out); !strings.Contains(string(b), "doc_1") {
		t.Fatalf("tool result not returned to Pi: %s", b)
	}
	if fe.calls["POST /v1/documents/search"] != 1 || fe.calls["POST /v1/evidence"] != 1 {
		t.Fatalf("engine calls: %v", fe.calls)
	}
	var msgs []model.FlowAgentMessage
	db.Where("conversation_id = ?", conv.ID).Order("seq").Find(&msgs)
	if len(msgs) != 2 || msgs[0].Role != "user" || msgs[0].RunID != run.ID || msgs[1].ToolName != "intel_search_documents" {
		t.Fatalf("conversation not linked: %+v", msgs)
	}
	// Next turn's context carries the evidence scope (reference resolution plumbing).
	var c model.FlowAgentConversation
	db.First(&c, "id = ?", conv.ID)
	if !strings.Contains(c.EvidenceScope, "doc_1") {
		t.Fatalf("evidence scope not updated: %s", c.EvidenceScope)
	}
}

func TestFlowApplyMonitorGoesThroughPiApprovalChain(t *testing.T) {
	s, db, fe, conv := flowTestSetup(t)
	run, err := s.CreateCloudAgentRun("user", flowRunRequest(conv, "flow-run-key-2"), "")
	if err != nil {
		t.Fatal(err)
	}
	flowModelStep(t, s, db, run.ID, []map[string]any{{"id": "call_a1", "type": "function", "function": map[string]any{"name": "intel_apply_monitor", "arguments": `{"preview_id":"mpv_1"}`}}})
	out, err := s.cloudAgentPiTool(context.Background(), "user", run.ID, piToolPayload("call_a1", "intel_apply_monitor", `{"preview_id":"mpv_1"}`))
	if err != nil {
		t.Fatal(err)
	}
	result, _ := out.(map[string]any)
	approvalID, _ := result["approvalId"].(string)
	if result["pause"] != true || approvalID == "" {
		t.Fatalf("want pause for approval, got %#v", out)
	}
	stored, _ := agentInterjectionState(t, s, run.ID)
	if stored.Status != "waiting_approval" || fe.calls["POST /v1/monitors"] != 0 {
		t.Fatalf("status=%s monitor calls=%d", stored.Status, fe.calls["POST /v1/monitors"])
	}
	if err := s.DecideCloudAgentApproval("user", run.ID, approvalID, "approve", ""); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if fe.calls["POST /v1/monitors"] != 1 {
		t.Fatalf("approved monitor must be applied once, calls=%d", fe.calls["POST /v1/monitors"])
	}
	_, state := agentInterjectionState(t, s, run.ID)
	content, ok := cloudAgentToolMessage(&state, "call_a1")
	if !ok || !strings.Contains(content, "mon_1") {
		t.Fatalf("approved result not recorded for Pi: %q", content)
	}
	var call model.FlowAgentToolCall
	if err := db.First(&call, "call_id = ?", "call_a1").Error; err != nil || call.ApprovalID != approvalID || call.Status != "succeeded" {
		t.Fatalf("flow receipt: %+v %v", call, err)
	}
}

func TestFlowAgentRunRejectsForeignConversationAndSubject(t *testing.T) {
	s, db, fe, conv := flowTestSetup(t)
	// Another user's personal workspace + conversation.
	other, err := flow.ResolveSubject(db, "intruder", flow.AgentSubject{Kind: flow.SubjectWorkspace})
	if err != nil {
		t.Fatal(err)
	}
	other.Subject.ID = other.Scope.WorkspaceID
	foreign, _ := flow.Conversations{DB: db}.Create("intruder", other, "x")
	if _, err := s.CreateCloudAgentRun("user", flowRunRequest(foreign, "flow-run-key-3"), ""); err == nil {
		t.Fatal("must not run on another tenant's conversation")
	}
	req := flowRunRequest(conv, "flow-run-key-4")
	req.Subject.ID = other.Scope.WorkspaceID // subject mismatch with own conversation
	if _, err := s.CreateCloudAgentRun("user", req, ""); err == nil {
		t.Fatal("subject mismatch must be rejected")
	}
	req = flowRunRequest(conv, "flow-run-key-5")
	req.CanvasID = "agent-canvas"
	if _, err := s.CreateCloudAgentRun("user", req, ""); err == nil {
		t.Fatal("intel subject with canvas id must be rejected")
	}
	if len(fe.seen) != 0 {
		t.Fatalf("engine was called: %v", fe.seen)
	}
}

func TestFlowRunNeverAcceptsClientSuppliedMCPTools(t *testing.T) {
	req := CloudAgentRequest{FlowMCPTools: []map[string]any{{"type": "function", "function": map[string]any{"name": "mcp_evil_x"}}}}
	_ = validateCloudAgentRequest(&req)
	if req.FlowMCPTools != nil {
		t.Fatal("client-supplied MCP tool snapshot must be discarded")
	}
}
