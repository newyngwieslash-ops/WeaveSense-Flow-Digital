package flow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"yingce/backend/internal/model"
)

// §5.2 saved topics: create/update/list, optimistic version, cross-tenant denial, tool.
func TestSavedTopicsCRUDAndTool(t *testing.T) {
	db := testDB(t)
	a, _ := ResolveScope(db, "u_alice", "")
	b, _ := ResolveScope(db, "u_bob", "")
	title, q := "锂电池价格", "碳酸锂价格走势"
	kw := []string{"碳酸锂", " 碳酸锂 ", "锂价"}
	v, err := CreateTopic(db, a, "u_alice", TopicInput{Title: &title, Question: &q, Keywords: &kw}, time.Now())
	if err != nil || v.Version != 1 || len(v.Keywords) != 2 {
		t.Fatalf("create: %v %+v", err, v)
	}
	nt := "锂电池价格周报"
	v2, err := UpdateTopic(db, a, v.ID, 1, TopicInput{Title: &nt}, time.Now())
	if err != nil || v2.Version != 2 || v2.Title != nt || v2.Question != q {
		t.Fatalf("update: %v %+v", err, v2)
	}
	if _, err := UpdateTopic(db, a, v.ID, 1, TopicInput{Title: &nt}, time.Now()); !errors.Is(err, ErrTopicConflict) {
		t.Fatalf("stale version must conflict: %v", err)
	}
	if _, err := GetTopic(db, b, v.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant get must be denied: %v", err)
	}
	if _, err := UpdateTopic(db, b, v.ID, 0, TopicInput{Title: &nt}, time.Now()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant update must be denied: %v", err)
	}
	if l, _ := ListTopics(db, b); len(l) != 0 {
		t.Fatalf("bob sees %d topics", len(l))
	}
	ro := a
	ro.Role = "viewer"
	if _, err := CreateTopic(db, ro, "u_alice", TopicInput{Title: &title}, time.Now()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer must not create topics: %v", err)
	}
	// SubjectTopic resolves the saved topic.
	if ss, err := ResolveSubject(db, "u_alice", AgentSubject{Kind: SubjectTopic, ID: v.ID}); err != nil || ss.TopicID != v.ID {
		t.Fatalf("resolve topic subject: %v", err)
	}
	spec, ok := FindTool("intel_save_topic")
	if !ok || spec.local == nil || spec.Class != "write" {
		t.Fatal("intel_save_topic missing")
	}
	out, err := spec.local(Tools{DB: db}, context.Background(), model.FlowAgentConversation{}, SubjectScope{Scope: a}, "u_alice",
		map[string]any{"title": "储能", "keywords": []any{"储能"}, "source_ids": []any{"src_1"}})
	if err != nil || out.(TopicView).SourceIDs[0] != "src_1" {
		t.Fatalf("tool create: %v %+v", err, out)
	}
	if l, _ := ListTopics(db, a); len(l) != 2 {
		t.Fatalf("alice topics %d", len(l))
	}
}

// §11.1 projection follows cancel / retry events.
func TestProjectionCancelAndRetryEvents(t *testing.T) {
	db := testDB(t)
	sc, _ := ResolveScope(db, "u_alice", "")
	ev := func(id, typ string, seq int64) Event {
		return Event{EventID: id, OperationID: "op_1", JobID: "j1", TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, Sequence: seq, Type: typ,
			OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{}`)}
	}
	for i, step := range []struct{ typ, want string }{
		{"collection.accepted", "running"}, {"operation.retrying", "retry_wait"},
		{"operation.cancel_requested", "cancel_requested"}, {"operation.cancelled", "cancelled"},
	} {
		if _, err := ConsumeEvent(db, "platform", ev("e"+step.typ, step.typ, int64(i+1))); err != nil {
			t.Fatal(err)
		}
		var p model.FlowOperationProjection
		db.First(&p, "operation_id = ?", "op_1")
		if p.Status != step.want {
			t.Fatalf("after %s: %s", step.typ, p.Status)
		}
	}
}

// §8.3 every tool definition declares its cost basis.
func TestEveryToolHasCostBasis(t *testing.T) {
	for _, s := range Catalog {
		if s.CostBasis == "" {
			t.Errorf("%s has no cost basis", s.Name)
		}
	}
}

// Audit-8 REST: priced preview + target validation, PATCH re-approval confirmation,
// export/media binary passthrough with attachment headers, engine 404 passthrough (A02).
func TestGaps9RoutesForwardAndRender(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	sc, _ := ResolveScope(db, "u_alice", "")
	db.Create(&model.FlowNotificationTarget{ID: "nt_ok", TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, Channel: "email", Address: "a@x.io", Status: "active"})
	type seen struct{ method, uri, body string }
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.RequestURI(), string(b)})
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/reports/rpt_foreign"):
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"code":404,"data":null,"msg":"report not found"}`))
		case strings.HasSuffix(r.URL.Path, "/export"):
			switch r.URL.Query().Get("format") {
			case "pdf":
				_, _ = w.Write([]byte(`{"code":0,"data":{"filename":"周报.pdf","content_type":"application/pdf","content_base64":"JVBERi0xLjcK"},"msg":"ok"}`))
			case "docx":
				_, _ = w.Write([]byte(`{"code":0,"data":{"filename":"周报.docx","content_type":"application/vnd.openxmlformats-officedocument.wordprocessingml.document","content_base64":"UEsDBBQAAAA="},"msg":"ok"}`))
			default:
				_, _ = w.Write([]byte(`{"code":0,"data":{"filename":"周报.md","content_type":"text/markdown; charset=utf-8","content":"# 周报"},"msg":"ok"}`))
			}
		case r.URL.Path == "/v1/media":
			_, _ = w.Write([]byte(`{"code":0,"data":{"filename":"chan_9","content_type":"image/png","sha256":"abc","data_base64":"UE5HREFUQQ=="},"msg":"ok"}`))
		default:
			_, _ = w.Write([]byte(`{"code":0,"data":{},"msg":"ok"}`))
		}
	}))
	t.Cleanup(srv.Close)
	cfg := Config{EngineURL: srv.URL, SigningKey: key, Issuer: "flow-platform", Audience: "loom-engine", TTL: MaxTokenTTL}
	r := gin.New()
	RegisterRoutes(r.Group("/api"), db, cfg, func(*gin.Context) (*model.User, error) { return &model.User{ID: "u_alice"}, nil })
	do := func(method, path, idem, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if idem != "" {
			req.Header.Set("X-Idempotency-Key", idem)
		}
		r.ServeHTTP(w, req)
		return w
	}
	if w := do("POST", "/api/intelligence/monitors/preview", "", `{"keywords":["x"],"notification_target_ids":["nt_ok"]}`); w.Code != 200 {
		t.Fatalf("preview %d", w.Code)
	}
	var pv map[string]any
	_ = json.Unmarshal([]byte(got[len(got)-1].body), &pv)
	if price, _ := pv["price"].(map[string]any); price == nil || price["version"] == "" {
		t.Fatalf("preview must carry the price hint: %s", got[len(got)-1].body)
	}
	n := len(got)
	if w := do("POST", "/api/intelligence/monitors/preview", "", `{"keywords":["x"],"notification_target_ids":["nt_foreign"]}`); w.Code != 400 || len(got) != n {
		t.Fatalf("foreign notification target must be rejected before the engine: %d", w.Code)
	}
	do("PATCH", "/api/intelligence/monitors/mon_1", "", `{"budget_micro_per_window":9,"approval_id":"forged"}`)
	if strings.Contains(got[len(got)-1].body, "approval_id") {
		t.Fatalf("client-supplied approval ids are dropped: %s", got[len(got)-1].body)
	}
	if w := do("PATCH", "/api/intelligence/monitors/mon_1", "", `{"confirmExpansion":true}`); w.Code != 400 {
		t.Fatalf("confirming an expansion needs an idempotency key: %d", w.Code)
	}
	do("PATCH", "/api/intelligence/monitors/mon_1", "ck", `{"budget_micro_per_window":9,"confirmExpansion":true}`)
	if last := got[len(got)-1]; last.method != "PATCH" || last.uri != "/v1/monitors/mon_1" || !strings.Contains(last.body, `"approval_id":"user-direct:u_alice:ck"`) || strings.Contains(last.body, "confirmExpansion") {
		t.Fatalf("confirmed PATCH forwarded as %+v", last)
	}
	w := do("GET", "/api/intelligence/reports/rpt_1/export?format=md", "", "")
	if w.Code != 200 || w.Body.String() != "# 周报" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/markdown") || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if got[len(got)-1].uri != "/v1/reports/rpt_1/export?format=md" {
		t.Fatalf("export forwarded as %s", got[len(got)-1].uri)
	}
	wPdf := do("GET", "/api/intelligence/reports/rpt_1/export?format=pdf", "", "")
	if wPdf.Code != 200 || !strings.HasPrefix(wPdf.Body.String(), "%PDF-1.7") || wPdf.Header().Get("Content-Type") != "application/pdf" {
		t.Fatalf("pdf export: %d %q %v", wPdf.Code, wPdf.Body.String(), wPdf.Header())
	}
	wDocx := do("GET", "/api/intelligence/reports/rpt_1/export?format=docx", "", "")
	if wDocx.Code != 200 || !strings.HasPrefix(wDocx.Body.String(), "PK\x03\x04") || !strings.Contains(wDocx.Header().Get("Content-Type"), "wordprocessingml.document") {
		t.Fatalf("docx export: %d %q %v", wDocx.Code, wDocx.Body.String(), wDocx.Header())
	}
	if w := do("GET", "/api/intelligence/reports/rpt_foreign/export?format=pdf", "", ""); w.Code != 404 {
		t.Fatalf("A02: foreign report export must be 404, got %d", w.Code)
	}
	w = do("GET", "/api/intelligence/media?document_id=doc_1&object_key=tenants/x/workspaces/y/telegram/c/9", "", "")
	if w.Code != 200 || w.Body.String() != "PNGDATA" || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("media: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if !strings.HasPrefix(got[len(got)-1].uri, "/v1/media?document_id=doc_1&object_key=tenants") {
		t.Fatalf("media forwarded as %s", got[len(got)-1].uri)
	}
}

// §7.3 platform citation check honours the engine's snippet/url verdicts and echoed values.
func TestCitationSnippetHashAndURL(t *testing.T) {
	f, tr := false, true
	scope := MaterialScope{SourceIDs: []string{"s1"}}
	base := ResolvedEvidence{EvidenceID: "e1", DocumentID: "d1", SourceID: "s1", Revision: 1, SnippetHash: "h1", URL: "https://a/1", SnippetOK: &tr, URLOK: &tr}
	claim := func(c Citation) []ReportClaim {
		return []ReportClaim{{Text: "t", Kind: "source_statement", Citations: []Citation{c}}}
	}
	if _, err := ValidateCitations(claim(Citation{EvidenceID: "e1", Revision: 1, SnippetHash: "h1", URL: "https://a/1"}), scope, []ResolvedEvidence{base}); err != nil {
		t.Fatal(err)
	}
	bad := base
	bad.SnippetOK = &f
	if _, err := ValidateCitations(claim(Citation{EvidenceID: "e1", Revision: 1}), scope, []ResolvedEvidence{bad}); !errors.Is(err, ErrCitation) || !strings.Contains(err.Error(), "snippet hash") {
		t.Fatalf("tampered snippet: %v", err)
	}
	bad = base
	bad.URLOK = &f
	if _, err := ValidateCitations(claim(Citation{EvidenceID: "e1", Revision: 1}), scope, []ResolvedEvidence{bad}); !errors.Is(err, ErrCitation) {
		t.Fatalf("moved url: %v", err)
	}
	for _, c := range []Citation{{EvidenceID: "e1", Revision: 1, SnippetHash: "other"}, {EvidenceID: "e1", Revision: 1, URL: "https://evil/"}} {
		if _, err := ValidateCitations(claim(c), scope, []ResolvedEvidence{base}); !errors.Is(err, ErrCitation) {
			t.Fatalf("echo mismatch %+v: %v", c, err)
		}
	}
}

func TestMonitorPreviewToolPricesAndValidatesTargets(t *testing.T) {
	db := testDB(t)
	sc, _ := ResolveScope(db, "u_alice", "")
	spec, _ := FindTool("intel_preview_monitor")
	props := spec.Parameters["properties"].(map[string]any)
	for _, k := range []string{"budget_micro_per_window", "notification_target_ids", "monitor_id"} {
		if props[k] == nil {
			t.Fatalf("preview tool lacks %s", k)
		}
	}
	out, err := spec.prepare(Tools{DB: db}, context.Background(), model.FlowAgentConversation{}, SubjectScope{Scope: sc}, "u_alice", map[string]any{"keywords": []any{"x"}})
	if err != nil || out["price"] == nil {
		t.Fatalf("prepare: %v %v", err, out)
	}
	t.Setenv("FLOW_PRICE_BOOK", `{"version":"pv_t","unit_micro":{"paid_source_calls":700}}`)
	h := MonitorPriceHint()
	if h["version"] != "pv_t" || h["per_source_run_micro"].(map[string]int64)["wechat"] != 700 || h["per_source_run_micro"].(map[string]int64)["rss"] != 0 {
		t.Fatalf("price hint %v", h)
	}
	if _, err := spec.prepare(Tools{DB: db}, context.Background(), model.FlowAgentConversation{}, SubjectScope{Scope: sc}, "u_alice", map[string]any{"notification_target_ids": []any{"nt_x"}}); !errors.Is(err, ErrToolArgs) {
		t.Fatalf("unknown target: %v", err)
	}
}
