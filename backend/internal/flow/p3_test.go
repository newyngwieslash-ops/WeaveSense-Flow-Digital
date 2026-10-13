package flow

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

	"yingce/backend/internal/model"
)

// p3Engine fakes the P3 engine endpoints; evidence is visible only to its tenant.
type p3Engine struct {
	mu       sync.Mutex
	tenant   string
	evidence []ResolvedEvidence
	reports  []map[string]any
	research map[string]int
	parents  []string
}

func newP3Engine(t *testing.T, tenant string) (*p3Engine, EngineClient) {
	f := &p3Engine{tenant: tenant, research: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := Verify(key, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "loom-engine", timeNow())
		if err != nil {
			w.WriteHeader(401)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		var in map[string]any
		_ = json.Unmarshal(body, &in)
		send := func(status int, data any, msg string) {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data, "msg": msg})
		}
		switch r.URL.Path {
		case "/v1/evidence/resolve":
			out := []ResolvedEvidence{}
			if ctx.TenantID == f.tenant {
				want := map[string]bool{}
				for _, id := range in["evidence_ids"].([]any) {
					want[id.(string)] = true
				}
				for _, e := range f.evidence {
					if want[e.EvidenceID] {
						out = append(out, e)
					}
				}
			}
			send(200, out, "ok")
		case "/v1/reports":
			f.reports = append(f.reports, in)
			send(201, map[string]any{"report_id": "rpt_1"}, "ok")
		case "/v1/research-runs":
			p := in["parent_run_id"].(string)
			f.parents = append(f.parents, p)
			if f.research[p] >= 3 {
				send(429, nil, "research child limit (3) reached for this run")
				return
			}
			f.research[p]++
			send(201, map[string]any{"id": "rr_x", "status": "succeeded"}, "ok")
		default:
			send(404, nil, "not found")
		}
	}))
	t.Cleanup(srv.Close)
	return f, EngineClient{Cfg: Config{EngineURL: srv.URL, SigningKey: key, Issuer: "flow-platform", Audience: "loom-engine", TTL: MaxTokenTTL}}
}

func setupP3(t *testing.T) (Tools, *p3Engine, model.FlowAgentConversation, SubjectScope) {
	tools, _, _, conv, ss := setupAgent(t)
	fe, client := newP3Engine(t, ss.Scope.TenantID)
	fe.evidence = []ResolvedEvidence{
		{EvidenceID: "ev_ok", DocumentID: "doc_1", SourceID: "src_1", Revision: 1, ChunkIndex: 0},
		{EvidenceID: "ev_b", DocumentID: "doc_2", SourceID: "src_2", Revision: 3, ChunkIndex: 2},
		{EvidenceID: "ev_trunc", DocumentID: "doc_1", SourceID: "src_1", Revision: 1, ChunkIndex: 1, Truncated: true},
	}
	tools.Engine, tools.RunID = client, "run_parent"
	return tools, fe, conv, ss
}

func claim(kind string, cites ...map[string]any) map[string]any {
	c := []any{}
	for _, x := range cites {
		c = append(c, x)
	}
	return map[string]any{"text": "statement", "kind": kind, "citations": c}
}

func cite(id string, rev float64) map[string]any {
	return map[string]any{"evidence_id": id, "revision": rev}
}

func TestSaveReportCitationValidation(t *testing.T) {
	tools, fe, conv, ss := setupP3(t)
	ctx := context.Background()
	scope := map[string]any{"source_ids": []any{"src_1"}}
	save := func(call string, claims []any, sc map[string]any) ToolResult {
		r, err := tools.Invoke(ctx, conv, ss, "u_alice", call, "intel_save_report", map[string]any{"title": "R", "claims": claims, "material_scope": sc}, "")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	cases := []struct {
		name   string
		claims []any
		scope  map[string]any
		want   string
	}{
		{"foreign or unknown evidence", []any{claim("source_statement", cite("ev_other_tenant", 1))}, scope, "not found"},
		{"wrong revision", []any{claim("source_statement", cite("ev_ok", 2))}, scope, "is revision 1"},
		{"wrong chunk", []any{claim("source_statement", map[string]any{"evidence_id": "ev_ok", "revision": 1.0, "chunk_index": 4.0})}, scope, "is chunk 0"},
		{"outside material scope", []any{claim("source_statement", cite("ev_b", 3))}, scope, "outside the report material scope"},
		{"no material scope", []any{claim("source_statement", cite("ev_ok", 1))}, map[string]any{}, "material_scope"},
		{"uncited statement", []any{claim("source_statement")}, scope, "needs at least one citation"},
		{"cross-source with one source", []any{claim("cross_source_verified", cite("ev_ok", 1), cite("ev_trunc", 1))}, scope, "≥2 sources"},
	}
	for i, c := range cases {
		r := save("bad_"+string(rune('a'+i)), c.claims, c.scope)
		if r.Status != "failed" || !strings.Contains(r.Error, c.want) {
			t.Fatalf("%s: want failure %q, got %+v", c.name, c.want, r)
		}
	}
	if len(fe.reports) != 0 {
		t.Fatalf("rejected reports must not reach the engine")
	}
	// Foreign tenant: same evidence ids, different caller → not found.
	bob := model.PersonalWorkspaceID("u_bob")
	bss, err := ResolveSubject(tools.DB, "u_bob", AgentSubject{Kind: SubjectWorkspace, ID: bob})
	if err != nil {
		t.Fatal(err)
	}
	bconv, err := Conversations{DB: tools.DB}.Create("u_bob", bss, "x")
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := tools.Invoke(ctx, bconv, bss, "u_bob", "b1", "intel_save_report", map[string]any{"title": "R", "claims": []any{claim("source_statement", cite("ev_ok", 1))}, "material_scope": scope}, ""); r.Status != "failed" || !strings.Contains(r.Error, "not found") {
		t.Fatalf("foreign tenant must not cite: %+v", r)
	}
	ok := save("good", []any{
		claim("source_statement", cite("ev_ok", 1)),
		claim("cross_source_verified", cite("ev_ok", 1), cite("ev_b", 3)),
		claim("source_statement", cite("ev_trunc", 1)),
		claim("model_inference"),
	}, map[string]any{"source_ids": []any{"src_1", "src_2"}})
	if ok.Status != "succeeded" || len(fe.reports) != 1 {
		t.Fatalf("valid report: %+v", ok)
	}
	cov := fe.reports[0]["coverage"].(map[string]any)
	if cov["truncated"] != true || len(cov["truncated_citations"].([]any)) != 1 {
		t.Fatalf("truncation must be flagged: %+v", cov)
	}
}

func TestResearchUsesRunAsParentAndSurfacesChildLimit(t *testing.T) {
	tools, fe, conv, ss := setupP3(t)
	for i := 0; i < 4; i++ {
		r, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "r"+string(rune('0'+i)), "intel_start_research", map[string]any{"question": "lithium", "source_ids": []any{"src_1"}}, "")
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 && r.Status != "succeeded" {
			t.Fatalf("child %d: %+v", i, r)
		}
		if i == 3 && (r.Status != "failed" || !strings.Contains(r.Error, "child limit")) {
			t.Fatalf("4th child must be refused: %+v", r)
		}
	}
	for _, p := range fe.parents {
		if p != "run_parent" {
			t.Fatalf("parent must be the agent run, got %s", p)
		}
	}
}

func TestApplyScheduleNeedsApproval(t *testing.T) {
	tools, _, conv, ss := setupP3(t)
	if _, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "s1", "intel_apply_schedule", map[string]any{"preview_id": "bpv_1"}, ""); err != ErrApprovalRequired {
		t.Fatalf("want ErrApprovalRequired, got %v", err)
	}
}

func TestSkillPacksAndRunPinning(t *testing.T) {
	ps, err := SkillPacks()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	var industry *SkillPack
	for i, p := range ps {
		for _, s := range p.Skills {
			if s.Hash == "" || len(s.Steps) == 0 || len(s.OutputSchema) == 0 {
				t.Fatalf("incomplete skill %s", s.ID)
			}
			ids[s.ID] = true
		}
		if p.Kind == "industry" {
			industry = &ps[i]
		}
	}
	for _, want := range []string{"source-evaluation", "cross-source-verification", "change-analysis", "periodic-brief", "citation-integrity"} {
		if !ids[want] {
			t.Fatalf("missing first-batch skill %s", want)
		}
	}
	if industry == nil || len(industry.SourceTemplates) == 0 || len(industry.Glossary) == 0 || len(industry.ReportSchema) == 0 || len(industry.EvalSamples) == 0 || len(industry.Skills) == 0 {
		t.Fatalf("industry pack incomplete: %+v", industry)
	}
	tools, _, conv, ss := setupP3(t)
	if _, err := LoadSkillForRun(tools.DB, ss.Scope, "run_1", "periodic-brief"); err == nil {
		t.Fatal("uninstalled skill must not load")
	}
	if _, err := InstallSkill(tools.DB, ss.Scope, "u_alice", "periodic-brief", ""); err != nil {
		t.Fatal(err)
	}
	r, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "ls1", "intel_load_skill", map[string]any{"skill_id": "periodic-brief"}, "")
	if err != nil || r.Status != "succeeded" || !strings.Contains(string(r.Output), "\"hash\"") {
		t.Fatalf("load skill: %v %+v", err, r)
	}
	// The workspace pin changes mid-run (e.g. reinstall); the run stays on its frozen hash.
	tools.DB.Model(&model.FlowSkillInstall{}).Where("skill_id = ?", "periodic-brief").Update("hash", "changed")
	if _, err := LoadSkillForRun(tools.DB, ss.Scope, "run_parent", "periodic-brief"); err != nil {
		t.Fatalf("pinned run must keep its version: %v", err)
	}
	if _, err := LoadSkillForRun(tools.DB, ss.Scope, "run_new", "periodic-brief"); err == nil {
		t.Fatal("new run must see the changed install")
	}
}

func TestReportCompletedCreatesOneNotification(t *testing.T) {
	db := testDB(t)
	ev := Event{EventID: "evt_1", OperationID: "rpt_1", JobID: "rpt_1", TenantID: "tn_a", WorkspaceID: "ws_a", Sequence: 1, Type: "report.completed",
		OccurredAt: time.Now(), Payload: json.RawMessage(`{"report_id":"rpt_1","title":"Weekly brief"}`)}
	for i := 0; i < 2; i++ {
		if _, err := ConsumeEvent(db, "platform-projection", ev); err != nil {
			t.Fatal(err)
		}
	}
	var rows []model.FlowNotification
	db.Find(&rows)
	if len(rows) != 1 || rows[0].RefID != "rpt_1" || rows[0].Title != "Weekly brief" {
		t.Fatalf("notifications: %+v", rows)
	}
}
