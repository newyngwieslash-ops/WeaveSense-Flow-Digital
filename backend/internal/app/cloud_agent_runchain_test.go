package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

// chainProbe records every §8.1 interface call made by the runtime, per adapter kind.
type chainProbe struct {
	mu    sync.Mutex
	calls map[string]int // "<kind>.<iface>[.<tool>]"
	// failExec, when set, makes the canvas ToolExecutor refuse that tool.
	failExec string
}

func (p *chainProbe) hit(key string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[key]++
}

func (p *chainProbe) count(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[key]
}

type probeScope struct {
	p     *chainProbe
	kind  string
	inner flow.ScopeResolver
}

func (w probeScope) ResolveSubject(db *gorm.DB, userID string, s flow.AgentSubject) (flow.SubjectScope, error) {
	w.p.hit(w.kind + ".scope")
	return w.inner.ResolveSubject(db, userID, s)
}

type probeContext struct {
	p     *chainProbe
	kind  string
	inner flow.ContextProvider
}

func (w probeContext) TurnContext(db *gorm.DB, c model.FlowAgentConversation, ss flow.SubjectScope) (flow.TurnContext, error) {
	w.p.hit(w.kind + ".context")
	return w.inner.TurnContext(db, c, ss)
}

type probeTools struct {
	p     *chainProbe
	kind  string
	inner flow.ToolProvider
}

func (w probeTools) Tools(r flow.ToolRequest) []map[string]any {
	w.p.hit(w.kind + ".tools")
	return w.inner.Tools(r)
}

type probeExec struct {
	p     *chainProbe
	kind  string
	inner flow.ToolExecutor
}

func (w probeExec) Execute(ctx context.Context, db *gorm.DB, c flow.ToolCall) (flow.ToolResult, error) {
	w.p.hit(w.kind + ".exec." + c.Name)
	if w.p.failExec != "" && c.Name == w.p.failExec {
		return flow.ToolResult{CallID: c.CallID, Tool: c.Name, Status: "failed"}, BadAuthRequest("probe executor refused " + c.Name)
	}
	return w.inner.Execute(ctx, db, c)
}

type probeUsage struct {
	p     *chainProbe
	kind  string
	inner flow.UsageRecorder
}

func (w probeUsage) Record(db *gorm.DB, in flow.UsageInput, now time.Time) (model.FlowUsageRecord, error) {
	w.p.hit(w.kind + ".usage")
	return w.inner.Record(db, in, now)
}

func installChainProbe(t *testing.T) *chainProbe {
	t.Helper()
	p := &chainProbe{calls: map[string]int{}}
	hook := func(c flow.RunChain) flow.RunChain {
		if err := c.Validate(); err != nil {
			t.Errorf("runtime built an incomplete chain: %v", err)
		}
		c.Scope = probeScope{p, c.Kind, c.Scope}
		c.Context = probeContext{p, c.Kind, c.Context}
		c.Tools = probeTools{p, c.Kind, c.Tools}
		c.Exec = probeExec{p, c.Kind, c.Exec}
		c.Usage = probeUsage{p, c.Kind, c.Usage}
		return c
	}
	agentRunChainHook.Store(&hook)
	t.Cleanup(func() { agentRunChainHook.Store(nil) })
	return p
}

// PRD §8.1: a canvas run is admitted, given its tool table, and executes both
// batched reads and approved writes through the canvas adapter's interfaces.
func TestCanvasRunGoesThroughRunChainInterfaces(t *testing.T) {
	p := installChainProbe(t)
	TestCloudAgentToolLoopPersistsApprovalAndAppliesCanvasWrite(t)
	for _, key := range []string{"canvas.scope", "canvas.tools", "canvas.exec.canvas_get_state", "canvas.exec.canvas_apply_ops"} {
		if p.count(key) == 0 {
			t.Errorf("canvas run never called %s; calls=%v", key, p.calls)
		}
	}
	for key := range p.calls {
		if strings.HasPrefix(key, flow.SubjectWorkspace+".") {
			t.Errorf("canvas run used the intelligence adapter: %s", key)
		}
	}
}

// The canvas ToolExecutor is the only route to a canvas write: when the chain's
// executor refuses, the approved write never reaches the canvas.
func TestCanvasWriteBlockedWhenRunChainExecutorRefuses(t *testing.T) {
	p := installChainProbe(t)
	p.failExec = "canvas_apply_ops"
	s, db, _, _ := creationTestService(t)
	canvas := model.CanvasProject{ID: "agent-canvas", UserID: "user", Title: "test", PayloadJSON: `{"nodes":[]}`}
	if err := db.Create(&canvas).Error; err != nil {
		t.Fatal(err)
	}
	req := agentTestRequest()
	req.PermissionMode = "auto"
	root, err := s.CreateCloudAgentRun("user", req, "")
	if err != nil {
		t.Fatal(err)
	}
	completeAgentTaskWithCalls(t, s, db, root.ID, `{"snapshotHash":"`+canvasHashOf(t, canvas.PayloadJSON)+`","ops":[{"type":"add_node","id":"agent-note","nodeType":"text","title":"n","content":"由 Agent 写入","x":24.0,"y":48.0}]}`)
	for i := 0; i < 4; i++ {
		_ = s.advanceCloudAgentByID("user", root.ID)
	}
	if p.count("canvas.exec.canvas_apply_ops") == 0 {
		t.Fatalf("canvas_apply_ops never reached the chain executor; calls=%v", p.calls)
	}
	var stored model.CanvasProject
	if err := db.First(&stored, "id = ?", canvas.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored.PayloadJSON, "由 Agent 写入") {
		t.Fatalf("canvas was written although the chain executor refused")
	}
}

// Canvas context for the Pi request comes from the canvas ContextProvider.
func TestCanvasPiRequestContextComesFromRunChain(t *testing.T) {
	p := installChainProbe(t)
	s, db, _, _ := creationTestService(t)
	if err := db.Create(&model.CanvasProject{ID: "agent-canvas", UserID: "user", Title: "t", PayloadJSON: `{"nodes":[]}`}).Error; err != nil {
		t.Fatal(err)
	}
	state := &cloudAgentRuntime{Request: agentTestRequest()}
	canonical := &canonicalAgentRequest{Tools: compileCloudAgentTools(state.Request, true)}
	_, err := s.buildEnhancedPiRequest(context.Background(), EnhancedPiRequestParams{UserID: "user", RunID: "run-ctx", CanvasID: "agent-canvas", RuntimeState: state, Canonical: canonical})
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Logf("pi request build returned %v (context provider still exercised)", err)
	}
	if p.count("canvas.context") == 0 || p.count("canvas.tools") == 0 {
		t.Fatalf("canvas Pi request bypassed the chain; calls=%v", p.calls)
	}
}

// Intelligence runs keep using the intelligence adapter (same five interfaces).
func TestIntelRunUsesIntelAdapterOfRunChain(t *testing.T) {
	p := installChainProbe(t)
	tools := cloudAgentIntelTools(CloudAgentRequest{Subject: &flow.AgentSubject{Kind: flow.SubjectWorkspace, ID: "ws"}, PermissionMode: "auto"})
	if len(tools) == 0 || p.count(flow.SubjectWorkspace+".tools") == 0 {
		t.Fatalf("intel tool table did not come from the intel adapter; calls=%v", p.calls)
	}
}

func canvasHashOf(t *testing.T, payload string) string {
	t.Helper()
	doc, err := creationDocument(payload)
	if err != nil {
		t.Fatal(err)
	}
	return cloudAgentCanvasHash(doc)
}

// completeAgentTaskWithCalls marks the run's model task as answered with one
// canvas_apply_ops call carrying args.
func completeAgentTaskWithCalls(t *testing.T, s *Service, db *gorm.DB, taskID, args string) {
	t.Helper()
	result, err := json.Marshal(map[string]any{
		"text": "写入画布。",
		"toolCalls": []map[string]any{
			{"id": "write-1", "type": "function", "function": map[string]any{"name": "canvas_apply_ops", "arguments": args}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var task model.Task
	if err := db.First(&task, "id = ?", taskID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", taskID).Updates(map[string]any{"status": model.TaskStatusSucceeded, "result_json": string(result), "input_json": publicTaskInputJSON(task.InputJSON)}).Error; err != nil {
		t.Fatal(err)
	}
}
