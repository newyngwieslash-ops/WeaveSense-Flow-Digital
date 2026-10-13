package flow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"yingce/backend/internal/model"
)

// idemMCP executes "lookup" (optionally losing the response by stalling past
// the client timeout) and answers "lookup_status" for reconciliation.
type idemMCP struct {
	mu       sync.Mutex
	stall    bool
	executed map[string]int
	results  map[string]string
}

func startIdemMCP(t *testing.T) (*idemMCP, string) {
	m := &idemMCP{executed: map[string]int{}, results: map[string]string{}}
	srv := func(*http.Request) *mcp.Server {
		s := mcp.NewServer(&mcp.Implementation{Name: "idem", Version: "1"}, nil)
		s.AddTool(&mcp.Tool{Name: "lookup", Description: "d", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			key, _ := req.Params.GetMeta()["idempotency_key"].(string)
			m.mu.Lock()
			m.executed[key]++
			m.results[key] = "booked"
			stall := m.stall
			m.mu.Unlock()
			if stall {
				time.Sleep(1500 * time.Millisecond)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "booked"}}}, nil
		})
		s.AddTool(&mcp.Tool{Name: "lookup_status", Description: "r", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var in map[string]any
			_ = json.Unmarshal(req.Params.Arguments, &in)
			m.mu.Lock()
			out, ok := m.results[in["idempotency_key"].(string)]
			m.mu.Unlock()
			b, _ := json.Marshal(map[string]any{"status": map[bool]string{true: "succeeded", false: "not_found"}[ok], "output": out})
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
		})
		return s
	}
	h := mcp.NewStreamableHTTPHandler(srv, nil)
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return m, ts.URL
}

func TestA16MCPUnknownOutcomeReconciledNotRetriedBilledOnce(t *testing.T) {
	tools, _, _, _, ss := setupAgent(t)
	m, url := startIdemMCP(t)
	t.Setenv("FLOW_MCP_CRED_IDEM", "s3cret-value-123")
	gw := &MCPGateway{DB: tools.DB, AllowInsecure: true}
	ctx := context.Background()
	mk := func(reconcile string) model.FlowMCPConnection {
		c, err := gw.Create(ss.Scope, "u_alice", MCPConnectionInput{Name: "Bk" + reconcile, Endpoint: url + "/mcp", CredentialRef: "env:FLOW_MCP_CRED_IDEM", TimeoutMS: 1000,
			CostRule: MCPCostRule{PerCallCredits: 3, ReconcileTool: reconcile}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := gw.Approve(ctx, ss.Scope, c.ID, "op", []string{"lookup"}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	credits := func(c model.FlowMCPConnection) (n int) {
		var rows []model.FlowMCPUsage
		tools.DB.Where("connection_id = ?", c.ID).Find(&rows)
		for _, r := range rows {
			n += r.Credits
		}
		return
	}

	// With a reconcile tool: lost response => unknown; retry reconciles, does not re-execute, bills once.
	c := mk("lookup_status")
	m.stall = true
	if _, _, err := gw.ExecuteKeyed(ctx, ss.Scope, "u_alice", c.ID, "lookup", map[string]any{"q": "x"}, "k1"); !errors.Is(err, ErrMCPUnknownOutcome) {
		t.Fatalf("want unknown, got %v", err)
	}
	m.stall = false
	for i := 0; i < 2; i++ {
		out, _, err := gw.ExecuteKeyed(ctx, ss.Scope, "u_alice", c.ID, "lookup", map[string]any{"q": "x"}, "k1")
		if err != nil || out != "booked" {
			t.Fatalf("reconcile: %q %v", out, err)
		}
	}
	if m.executed["k1"] != 1 || credits(c) != 3 {
		t.Fatalf("executed=%d credits=%d (want 1, 3)", m.executed["k1"], credits(c))
	}
	if _, _, err := gw.ExecuteKeyed(ctx, ss.Scope, "u_alice", c.ID, "lookup", map[string]any{"q": "other"}, "k1"); !errors.Is(err, ErrMCPKeyConflict) {
		t.Fatalf("key reuse with new args: %v", err)
	}

	// Without a reconcile tool: unknown stays unknown, never blindly retried.
	c2 := mk("")
	m.stall = true
	gw.ExecuteKeyed(ctx, ss.Scope, "u_alice", c2.ID, "lookup", map[string]any{}, "k2")
	m.stall = false
	if _, _, err := gw.ExecuteKeyed(ctx, ss.Scope, "u_alice", c2.ID, "lookup", map[string]any{}, "k2"); !errors.Is(err, ErrMCPUnknownOutcome) {
		t.Fatalf("must not retry: %v", err)
	}
	if m.executed["k2"] != 1 || credits(c2) != 0 {
		t.Fatalf("executed=%d credits=%d", m.executed["k2"], credits(c2))
	}
}

func TestA15RuntimeBusyQueuesFIFOAndCancelPropagates(t *testing.T) {
	db := testDB(t)
	s := RunSlots{DB: db, Limit: 1}
	if ok, _, _ := s.Acquire("u", "agent_run", "r1", "u"); !ok {
		t.Fatal("r1 admitted")
	}
	time.Sleep(2 * time.Millisecond)
	if ok, pos, _ := s.Acquire("u", "agent_run", "r2", "u"); ok || pos != 1 {
		t.Fatalf("r2 queued pos1, got %v %d", ok, pos)
	}
	time.Sleep(2 * time.Millisecond)
	if ok, pos, _ := s.Acquire("u", "agent_run", "r3", "u"); ok || pos != 2 {
		t.Fatalf("r3 queued pos2, got %v %d", ok, pos)
	}
	if ok, _, _ := s.Acquire("other", "agent_run", "o1", "o"); !ok {
		t.Fatal("other owner not blocked")
	}
	s.Release("r2") // cancel a queued run
	next := s.Release("r1")
	if len(next) != 1 || next[0].ID != "r3" {
		t.Fatalf("next = %+v", next)
	}
	if ok, _, _ := s.Acquire("u", "agent_run", "r3", "u"); !ok {
		t.Fatal("r3 admitted after release")
	}
	// Synchronous executor waits in the queue, then runs.
	done := make(chan error, 1)
	go func() { done <- s.Wait(context.Background(), "u", "agent_run", "r4") }()
	time.Sleep(300 * time.Millisecond)
	s.Release("r3")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.Wait(ctx, "u", "agent_run", "r5"); !errors.Is(err, ErrSlotQueued) {
		t.Fatalf("cancelled wait: %v", err)
	}
	var n int64
	db.Model(&model.FlowRunSlot{}).Where("id = 'r5'").Count(&n)
	if n != 0 {
		t.Fatal("cancelled waiter must leave the queue")
	}
}

func TestA21WaitingExternalResumesOnceAfterDedupedCompletion(t *testing.T) {
	db := testDB(t)
	sc, _ := ResolveScope(db, "u_alice", "")
	w := Waits{DB: db}
	if _, err := w.Park(model.FlowExternalWait{TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, UserID: "u_alice", RunID: "run1", OperationID: "op1", Checkpoint: `{"prompt":"x"}`}); err != nil {
		t.Fatal(err)
	}
	if again, _ := w.Park(model.FlowExternalWait{TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, UserID: "u_alice", RunID: "run1", OperationID: "op1"}); again.Checkpoint != `{"prompt":"x"}` {
		t.Fatal("park must be idempotent per operation")
	}
	resumed := 0
	resume := func(model.FlowExternalWait) (string, error) { resumed++; return "run2", nil }
	if n, _ := w.ResumeReady(resume); n != 0 {
		t.Fatal("not ready before completion")
	}
	ev := Event{EventID: "e1", TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, OperationID: "op1", Type: "collection.completed", Sequence: 2, OccurredAt: time.Now()}
	for i := 0; i < 3; i++ { // duplicate deliveries
		if _, err := ConsumeEvent(db, "platform-projection", ev); err != nil {
			t.Fatal(err)
		}
	}
	w.ResumeReady(resume)
	w.ResumeReady(resume) // restart / second worker
	if resumed != 1 {
		t.Fatalf("resumed %d times", resumed)
	}
	var row model.FlowExternalWait
	db.First(&row, "operation_id = 'op1'")
	if row.Status != "resumed" || row.ResumeRunID != "run2" || row.Outcome != "succeeded" {
		t.Fatalf("%+v", row)
	}

	// Completion consumed before parking (restart window) is picked up from the projection.
	ConsumeEvent(db, "platform-projection", Event{EventID: "e2", TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, OperationID: "op2", Type: "collection.completed", Sequence: 2, OccurredAt: time.Now()})
	w.Park(model.FlowExternalWait{TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, UserID: "u_alice", RunID: "run3", OperationID: "op2"})
	// Re-authorization: membership revoked => denied, not resumed.
	db.Model(&model.Membership{}).Where("user_id = ?", "u_alice").Update("status", "revoked")
	w.ResumeReady(resume)
	row = model.FlowExternalWait{}
	db.First(&row, "operation_id = 'op2'")
	if row.Status != "denied" || resumed != 1 {
		t.Fatalf("revoked grant must not resume: %+v resumed=%d", row, resumed)
	}
}
