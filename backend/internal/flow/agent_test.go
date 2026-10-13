package flow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

// fakeEngine verifies the execution context like Loom Engine and keeps per-tenant state.
type fakeEngine struct {
	mu       sync.Mutex
	calls    map[string]int // path -> count
	monitors map[string][]map[string]any
	idem     map[string]bool
}

func newFakeEngine(t *testing.T) (*fakeEngine, EngineClient) {
	f := &fakeEngine{calls: map[string]int{}, monitors: map[string][]map[string]any{}, idem: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := Verify(key, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "loom-engine", timeNow())
		if err != nil {
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls[r.Method+" "+r.URL.Path]++
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		var data any
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/documents/search":
			data = map[string]any{"items": []map[string]any{{"document_id": "doc_" + ctx.TenantID, "title": "Acme lithium deal"}}}
		case "POST /v1/monitors/preview":
			data = map[string]any{"preview_id": "mpv_1", "keywords": in["keywords"]}
		case "POST /v1/monitors":
			k := ctx.TenantID + r.Header.Get("X-Idempotency-Key")
			if !f.idem[k] {
				f.idem[k] = true
				f.monitors[ctx.TenantID] = append(f.monitors[ctx.TenantID], map[string]any{"id": "mon_1", "approval_id": in["approval_id"]})
			}
			data = map[string]any{"monitor": f.monitors[ctx.TenantID][0]}
		case "POST /v1/evidence":
			data = map[string]any{"created": 1}
		case "GET /v1/monitors":
			data = f.monitors[ctx.TenantID]
		case "POST /v1/collection-runs":
			data = map[string]any{"operation_id": "op_" + r.Header.Get("X-Idempotency-Key")}
		default:
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
	}))
	t.Cleanup(srv.Close)
	return f, EngineClient{Cfg: Config{EngineURL: srv.URL, SigningKey: key, Issuer: "flow-platform", Audience: "loom-engine", TTL: MaxTokenTTL}}
}

func setupAgent(t *testing.T) (Tools, Conversations, *fakeEngine, model.FlowAgentConversation, SubjectScope) {
	db := testDB(t)
	fe, client := newFakeEngine(t)
	convs := Conversations{DB: db}
	ws := model.PersonalWorkspaceID("u_alice")
	ss, err := ResolveSubject(db, "u_alice", AgentSubject{Kind: SubjectWorkspace, ID: ws})
	if err != nil {
		t.Fatal(err)
	}
	conv, err := convs.Create("u_alice", ss, "Acme")
	if err != nil {
		t.Fatal(err)
	}
	return Tools{DB: db, Engine: client}, convs, fe, conv, ss
}

func TestMultiTurnContextCarriesPriorTurnEvidenceAndReceipts(t *testing.T) {
	tools, convs, _, conv, ss := setupAgent(t)
	ctx := context.Background()
	convs.Append(conv, model.FlowAgentMessage{Role: "user", Content: "Find news about Acme lithium"})
	r, err := tools.Invoke(ctx, conv, ss, "u_alice", "call_1", "intel_search_documents", map[string]any{"query": "lithium"}, "")
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("search %v %+v", err, r)
	}
	b, _ := json.Marshal(r)
	convs.Append(conv, model.FlowAgentMessage{Role: "tool", Content: string(b), ToolCallID: "call_1", ToolName: r.Tool})
	if _, err := tools.Invoke(ctx, conv, ss, "u_alice", "call_2", "intel_collect_once", map[string]any{"source_ids": []any{"src_1"}}, ""); err != nil {
		t.Fatal(err)
	}
	convs.Append(conv, model.FlowAgentMessage{Role: "user", Content: "Summarize that article"})
	conv, ss, _ = convs.Get("u_alice", conv.ID)
	tc, err := BuildTurnContext(tools.DB, conv, ss)
	if err != nil {
		t.Fatal(err)
	}
	if len(tc.Recent) != 3 || tc.Recent[0].Content != "Find news about Acme lithium" || tc.Recent[2].Content != "Summarize that article" {
		t.Fatalf("recent turns not carried: %+v", tc.Recent)
	}
	ids, _ := tc.EvidenceScope["recent_document_ids"].([]any)
	if len(ids) != 1 || ids[0] != "doc_"+ss.Scope.TenantID {
		t.Fatalf("evidence scope missing prior search hit: %+v", tc.EvidenceScope)
	}
	if len(tc.ActionReceipts) != 1 || tc.ActionReceipts[0].Tool != "intel_collect_once" || tc.ActionReceipts[0].Status != "succeeded" {
		t.Fatalf("receipts: %+v", tc.ActionReceipts)
	}
	if len(tc.BackgroundJobs) != 1 {
		t.Fatalf("background job from receipt missing: %+v", tc.BackgroundJobs)
	}
	msgs := tc.Messages()
	if msgs[0]["role"] != "system" || !strings.Contains(msgs[0]["content"].(string), "recent_document_ids") || len(msgs) != 4 {
		t.Fatalf("messages: %+v", msgs)
	}
}

func TestApplyMonitorNeedsPiApprovalIDAndExecutesOnce(t *testing.T) {
	tools, _, fe, conv, ss := setupAgent(t)
	ctx := context.Background()
	if _, err := tools.Invoke(ctx, conv, ss, "u_alice", "c1", "intel_apply_monitor", map[string]any{"preview_id": "mpv_1"}, ""); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("want ErrApprovalRequired, got %v", err)
	}
	if fe.calls["POST /v1/monitors"] != 0 {
		t.Fatal("write executed without approval")
	}
	r, err := tools.Invoke(ctx, conv, ss, "u_alice", "c2", "intel_apply_monitor", map[string]any{"preview_id": "mpv_1"}, "run-1-0")
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("approved apply: %v %+v", err, r)
	}
	again, _ := tools.Invoke(ctx, conv, ss, "u_alice", "c2", "intel_apply_monitor", map[string]any{"preview_id": "mpv_1"}, "run-1-0")
	if !again.Replayed || fe.calls["POST /v1/monitors"] != 1 {
		t.Fatalf("approved write must execute once: calls=%d", fe.calls["POST /v1/monitors"])
	}
	status, body, err := tools.Engine.Do(ctx, ss.Scope, Actor{Type: "user", ID: "u_alice"}, "GET", "/v1/monitors", "", nil)
	if err != nil || status != 200 || !strings.Contains(string(body), "run-1-0") {
		t.Fatalf("monitor not visible with approval id: %d %s %v", status, body, err)
	}
}

func TestSearchWritesEvidenceRows(t *testing.T) {
	tools, _, fe, conv, ss := setupAgent(t)
	if _, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "s1", "intel_search_documents", map[string]any{"query": "lithium"}, ""); err != nil {
		t.Fatal(err)
	}
	if fe.calls["POST /v1/evidence"] != 1 {
		t.Fatalf("evidence not recorded: %+v", fe.calls)
	}
}

func TestCrossTenantToolCallsRejected(t *testing.T) {
	tools, convs, fe, conv, _ := setupAgent(t)
	ctx := context.Background()
	if _, _, err := convs.Get("u_bob", conv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bob read alice conversation: %v", err)
	}
	bob, _ := ResolveSubject(tools.DB, "u_bob", AgentSubject{Kind: SubjectWorkspace, ID: ""})
	if _, err := ResolveSubject(tools.DB, "u_bob", AgentSubject{Kind: SubjectWorkspace, ID: conv.WorkspaceID}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob resolved alice workspace: %v", err)
	}
	if _, err := tools.Invoke(ctx, conv, bob, "u_bob", "x", "intel_search_documents", map[string]any{"query": "q"}, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant invoke: %v", err)
	}
	if fe.calls["POST /v1/documents/search"] != 0 {
		t.Fatal("engine called for cross-tenant request")
	}
	// viewer cannot write
	tools.DB.Model(&model.WorkspaceMembership{}).Where("user_id = ?", "u_alice").Update("role", "viewer")
	_, ss, _ := convs.Get("u_alice", conv.ID)
	if _, err := tools.Invoke(ctx, conv, ss, "u_alice", "y", "intel_collect_once", map[string]any{"source_ids": []any{"s"}}, ""); !errors.Is(err, ErrToolPermission) {
		t.Fatalf("viewer write: %v", err)
	}
}

func TestIdempotentToolReplayAndArgValidation(t *testing.T) {
	tools, _, fe, conv, ss := setupAgent(t)
	ctx := context.Background()
	a := map[string]any{"source_ids": []any{"src_1"}}
	r1, _ := tools.Invoke(ctx, conv, ss, "u_alice", "call_9", "intel_collect_once", a, "")
	r2, err := tools.Invoke(ctx, conv, ss, "u_alice", "call_9", "intel_collect_once", a, "")
	if err != nil || !r2.Replayed || string(r2.Output) != string(r1.Output) || fe.calls["POST /v1/collection-runs"] != 1 {
		t.Fatalf("replay: %v %+v calls=%d", err, r2, fe.calls["POST /v1/collection-runs"])
	}
	if _, err := tools.Invoke(ctx, conv, ss, "u_alice", "call_9", "intel_collect_once", map[string]any{"source_ids": []any{"other"}}, ""); !errors.Is(err, ErrToolConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if _, err := tools.Invoke(ctx, conv, ss, "u_alice", "c", "intel_read_document", map[string]any{"document_id": 5}, ""); !errors.Is(err, ErrToolArgs) {
		t.Fatalf("type check: %v", err)
	}
	if _, err := tools.Invoke(ctx, conv, ss, "u_alice", "c", "intel_read_document", map[string]any{"document_id": "d", "evil": "x"}, ""); !errors.Is(err, ErrToolArgs) {
		t.Fatalf("unknown field: %v", err)
	}
}

func TestMessagePaginationAndTopicSubject(t *testing.T) {
	_, convs, _, conv, ss := setupAgent(t)
	for i := 0; i < 5; i++ {
		convs.Append(conv, model.FlowAgentMessage{Role: "user", Content: "m"})
	}
	page, next, _ := convs.Messages(conv.ID, 0, 2)
	if len(page) != 2 || page[0].Seq != 5 || next != 4 {
		t.Fatalf("page1 %+v next=%d", page, next)
	}
	page, next, _ = convs.Messages(conv.ID, next, 10)
	if len(page) != 3 || next != 0 {
		t.Fatalf("page2 %d next=%d", len(page), next)
	}
	topic := model.FlowTopic{ID: "tp_1", TenantID: ss.Scope.TenantID, WorkspaceID: ss.Scope.WorkspaceID, Title: "Acme"}
	convs.DB.Create(&topic)
	if got, err := ResolveSubject(convs.DB, "u_alice", AgentSubject{Kind: SubjectTopic, ID: "tp_1"}); err != nil || got.TopicID != "tp_1" {
		t.Fatalf("topic subject %v", err)
	}
	if _, err := ResolveSubject(convs.DB, "u_bob", AgentSubject{Kind: SubjectTopic, ID: "tp_1"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("bob topic %v", err)
	}
}
func timeNow() time.Time { return time.Now() }
