package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A canvas conversation continued from the intelligence page is a full canvas
// run: canvas tools on the server-side document, plus directory recording and
// read-only intelligence retrieval tools (never intelligence write tools).
func TestCanvasConversationContinuedFromIntelligenceUsesCanvasTools(t *testing.T) {
	s, db, _, _ := flowTestSetup(t)
	if err := db.AutoMigrate(&model.FlowCanvasConversationDoc{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CanvasProject{ID: "cv_m", UserID: "user", PayloadJSON: `{"nodes":[]}`}).Error; err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"activeId":"c1","conversations":[{"id":"c1","title":"Board","messages":[{"id":"m1","role":"user","text":"make a poster"}]}]}`
	db.Create(&model.FlowCanvasConversationDoc{UserID: "user", CanvasID: "cv_m", Document: doc, Version: 1, UpdatedAt: time.Now()})
	if err := flow.MirrorCanvasConversations(db, "user", "cv_m", []flow.CanvasConv{{ID: "c1", Title: "Board", Messages: []flow.CanvasConvMessage{{ID: "m1", Role: "user", Text: "make a poster"}}}}); err != nil {
		t.Fatal(err)
	}
	convID := flow.CanvasConversationIndexID("user", "cv_m", "c1")
	req := CloudAgentRequest{Prompt: "Add a title node", Model: "text-test", ChannelID: "channel", ChannelModelKey: "text-test", PermissionMode: "request_approval",
		IdempotencyKey: "canvas-mirror-1", FlowConversationID: convID, CanvasID: "cv_m", Subject: &flow.AgentSubject{Kind: flow.SubjectCanvas, ID: "cv_m"}}
	req.Budget.MaxCredits = 1
	run, err := s.CreateCloudAgentRun("user", req, "")
	if err != nil {
		t.Fatal(err)
	}
	_, state := agentInterjectionState(t, s, run.ID)
	names := map[string]bool{}
	for _, tool := range state.Canonical.Tools {
		names[stringValue(tool["function"].(map[string]any)["name"])] = true
	}
	// Canvas tool set + read-only intelligence lookup tools, never intelligence write tools.
	if !names["plan_update"] || !names["intel_search_documents"] || !names["intel_read_document"] || !names["intel_list_sources"] {
		t.Fatalf("canvas or read-only intel tools missing: %v", names)
	}
	for _, tool := range cloudAgentIntelTools(flowRunRequest(model.FlowAgentConversation{}, "x")) {
		n := stringValue(tool["function"].(map[string]any)["name"])
		if n != "plan_update" && n != "ask_user" && n != "task_get" && !canvasReadIntelTool(n) && names[n] {
			t.Fatalf("intel-only tool %s exposed on a canvas run", n)
		}
	}
	ctx := false
	for _, m := range state.Canonical.Messages {
		if m[cloudAgentContextSourceKey] == "flow_context" {
			ctx = true
		}
	}
	if !ctx {
		t.Fatal("directory context missing")
	}
	var d model.FlowCanvasConversationDoc
	db.First(&d, "canvas_id = ?", "cv_m")
	if !strings.Contains(d.Document, "Add a title node") {
		t.Fatalf("user turn not written back to the sidebar: %s", d.Document)
	}
	// Someone else's canvas / conversation is rejected.
	db.Create(&model.CanvasProject{ID: "cv_other", UserID: "other", PayloadJSON: `{"nodes":[]}`})
	bad := req
	bad.IdempotencyKey, bad.CanvasID, bad.Subject = "canvas-mirror-2", "cv_other", &flow.AgentSubject{Kind: flow.SubjectCanvas, ID: "cv_other"}
	if _, err := s.CreateCloudAgentRun("user", bad, ""); err == nil {
		t.Fatal("run on a foreign canvas/conversation pair accepted")
	}
}

func TestCanvasAgentExecutesReadOnlyIntelAndMCPTools(t *testing.T) {
	s, db, fe, _ := flowTestSetup(t)
	if err := db.AutoMigrate(&model.FlowMCPConnection{}, &model.FlowMCPUsage{}, &model.FlowMCPCall{}, &model.FlowMCPSecret{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CanvasProject{ID: "cv_standalone", UserID: "user", PayloadJSON: `{"nodes":[]}`}).Error; err != nil {
		t.Fatal(err)
	}

	// Start a local streamable-HTTP MCP server with an approved tool.
	mcpSrv := mcp.NewServer(&mcp.Implementation{Name: "kb-mcp", Version: "1"}, nil)
	schema := map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}, "required": []any{"q"}}
	mcpSrv.AddTool(&mcp.Tool{Name: "kb_query", Description: "Query external KB via MCP", InputSchema: schema}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in map[string]any
		_ = json.Unmarshal(req.Params.Arguments, &in)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"items":[{"id":"kb_1","topic":"` + stringValue(in["q"]) + `","score":95}]}`}}}, nil
	})
	httpSrv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return mcpSrv }, nil))
	t.Cleanup(httpSrv.Close)

	ss, err := flow.ResolveSubject(db, "user", flow.AgentSubject{Kind: flow.SubjectWorkspace, ID: ""})
	if err != nil {
		t.Fatal(err)
	}
	gw := &flow.MCPGateway{DB: db, AllowInsecure: true}
	s.ConfigureFlowMCP(gw)
	conn, err := gw.Create(ss.Scope, "user", flow.MCPConnectionInput{Name: "KB", Endpoint: httpSrv.URL + "/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Approve(context.Background(), ss.Scope, conn.ID, "user", []string{"kb_query"}); err != nil {
		t.Fatal(err)
	}
	mcpTools := gw.AgentTools(ss.Scope)
	if len(mcpTools) != 1 {
		t.Fatalf("expected 1 approved MCP tool, got %+v", mcpTools)
	}
	mcpToolName := stringValue(mcpTools[0]["function"].(map[string]any)["name"])

	// Client-supplied FlowIntelTools must be stripped on validation.
	evilReq := CloudAgentRequest{FlowIntelTools: []map[string]any{{"type": "function", "function": map[string]any{"name": "intel_evil"}}}}
	_ = validateCloudAgentRequest(&evilReq)
	if evilReq.FlowIntelTools != nil {
		t.Fatal("client-supplied FlowIntelTools must be discarded")
	}

	// Standalone canvas run (no FlowConversationID) gets read-only intel tools + approved MCP tools.
	req := CloudAgentRequest{
		CanvasID: "cv_standalone", Prompt: "Search intel and MCP then model on canvas",
		Model: "text-test", ChannelID: "channel", ChannelModelKey: "text-test",
		PermissionMode: "auto", ContextScope: []string{"canvas"}, IdempotencyKey: "canvas-intel-mcp-1",
	}
	req.Budget.MaxCredits = 10
	run, err := s.CreateCloudAgentRun("user", req, "")
	if err != nil {
		t.Fatal(err)
	}
	_, state := agentInterjectionState(t, s, run.ID)
	names := map[string]bool{}
	for _, tool := range state.Canonical.Tools {
		names[stringValue(tool["function"].(map[string]any)["name"])] = true
	}
	if !names["intel_search_documents"] || !names["intel_read_document"] || !names["intel_list_sources"] || !names[mcpToolName] {
		t.Fatalf("expected read-only intel + MCP tools on standalone canvas run, got %v", names)
	}
	if names["intel_apply_monitor"] || names["intel_handoff_to_canvas"] {
		t.Fatalf("write/handoff intel tools must not be exposed on canvas run: %v", names)
	}

	// Execute intel_search_documents and the MCP tool inside the standalone canvas run.
	flowModelStep(t, s, db, run.ID, []map[string]any{
		{"id": "call_intel_1", "type": "function", "function": map[string]any{"name": "intel_search_documents", "arguments": `{"query":"lithium"}`}},
		{"id": "call_mcp_1", "type": "function", "function": map[string]any{"name": mcpToolName, "arguments": `{"q":"battery"}`}},
	})
	intelOut, err := s.cloudAgentPiTool(context.Background(), "user", run.ID, piToolPayload("call_intel_1", "intel_search_documents", `{"query":"lithium"}`))
	if err != nil {
		t.Fatalf("canvas run intel_search_documents failed: %v", err)
	}
	if b, _ := json.Marshal(intelOut); !strings.Contains(string(b), "doc_1") {
		t.Fatalf("unexpected intel output: %s", b)
	}
	if fe.calls["POST /v1/documents/search"] != 1 {
		t.Fatalf("expected 1 search call to engine, got %v", fe.calls)
	}

	mcpOut, err := s.cloudAgentPiTool(context.Background(), "user", run.ID, piToolPayload("call_mcp_1", mcpToolName, `{"q":"battery"}`))
	if err != nil {
		t.Fatalf("canvas run MCP tool failed: %v", err)
	}
	if b, _ := json.Marshal(mcpOut); !strings.Contains(string(b), "kb_1") || !strings.Contains(string(b), "battery") {
		t.Fatalf("unexpected MCP output: %s", b)
	}

	// Read canvas state to obtain snapshotHash, then build data-source -> logic -> skill -> image topology via canvas_apply_ops.
	flowModelStep(t, s, db, run.ID, []map[string]any{
		{"id": "call_read_1", "type": "function", "function": map[string]any{"name": "canvas_get_state", "arguments": `{}`}},
	})
	stateOut, err := s.cloudAgentPiTool(context.Background(), "user", run.ID, piToolPayload("call_read_1", "canvas_get_state", `{}`))
	if err != nil {
		t.Fatalf("canvas_get_state failed: %v", err)
	}
	var stateToolEnvelope struct {
		Content string `json:"content"`
	}
	rawStateOut, _ := json.Marshal(stateOut)
	_ = json.Unmarshal(rawStateOut, &stateToolEnvelope)
	var statePayload struct {
		SnapshotHash string `json:"snapshotHash"`
	}
	_ = json.Unmarshal([]byte(stateToolEnvelope.Content), &statePayload)
	if statePayload.SnapshotHash == "" {
		t.Fatalf("missing snapshotHash from canvas_get_state: %s", rawStateOut)
	}

	opsArgs, _ := json.Marshal(map[string]any{
		"snapshotHash": statePayload.SnapshotHash,
		"ops": []map[string]any{
			{"type": "add_node", "id": "node-ds", "nodeType": "data-source", "title": "情报数据源", "content": `[{"id":"doc_1","title":"Lithium supply","score":95}]`},
			{"type": "add_node", "id": "node-logic", "nodeType": "logic", "title": "高分筛选"},
			{"type": "update_node", "id": "node-logic", "patch": map[string]any{"operator": "if_else", "fieldPath": "score", "comparator": "gt", "compareValue": "80"}},
			{"type": "add_node", "id": "node-skill", "nodeType": "skill", "title": "视觉提炼技能", "content": "提炼实验室特写镜头"},
			{"type": "update_node", "id": "node-skill", "patch": map[string]any{"skillId": "lab-shot", "skillName": "视觉提炼技能", "skillDescription": "转化为电影感提示词"}},
			{"type": "add_node", "id": "node-img", "nodeType": "image", "title": "主视觉草稿", "content": "生成电池特写"},
			{"type": "connect_nodes", "id": "edge-1", "fromNodeId": "node-ds", "toNodeId": "node-logic"},
			{"type": "connect_nodes", "id": "edge-2", "fromNodeId": "node-logic", "fromHandleId": "true", "toNodeId": "node-skill"},
			{"type": "connect_nodes", "id": "edge-3", "fromNodeId": "node-skill", "toNodeId": "node-img"},
		},
	})
	flowModelStep(t, s, db, run.ID, []map[string]any{
		{"id": "call_ops_1", "type": "function", "function": map[string]any{"name": "canvas_apply_ops", "arguments": string(opsArgs)}},
	})
	opsOut, err := s.cloudAgentPiTool(context.Background(), "user", run.ID, piToolPayload("call_ops_1", "canvas_apply_ops", string(opsArgs)))
	if err != nil {
		t.Fatalf("canvas_apply_ops modeling pipeline failed: %v", err)
	}
	if b, _ := json.Marshal(opsOut); !strings.Contains(string(b), "node-skill") {
		t.Fatalf("unexpected canvas_apply_ops output: %s", b)
	}
}
