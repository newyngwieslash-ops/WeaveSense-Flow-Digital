package flow

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"yingce/backend/internal/model"

	"github.com/gin-gonic/gin"
)

// TestContractRoutesForwardToEngine covers the PRD §11.1 routes added in the gap round:
// method/path/query forwarding, idempotency header, and direct-approval injection for REST monitor saves.
func TestContractRoutesForwardToEngine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testDB(t)
	type seen struct{ method, uri, idem, body string }
	var mu sync.Mutex
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := Verify(key, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "loom-engine", timeNow()); err != nil {
			w.WriteHeader(401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{r.Method, r.URL.RequestURI(), r.Header.Get("X-Idempotency-Key"), string(b)})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"code":0,"data":{},"msg":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	cfg := Config{EngineURL: srv.URL, SigningKey: key, Issuer: "flow-platform", Audience: "loom-engine", TTL: MaxTokenTTL}
	r := gin.New()
	RegisterRoutes(r.Group("/api"), db, cfg, func(*gin.Context) (*model.User, error) { return &model.User{ID: "u_alice"}, nil })
	cases := []struct{ method, path, idem, body, wantURI string }{
		{"PATCH", "/api/intelligence/sources/src_1", "", `{"expected_revision":1,"title":"x"}`, "/v1/sources/src_1"},
		{"GET", "/api/intelligence/sources/health", "", "", "/v1/sources/health"},
		{"POST", "/api/intelligence/monitors/preview", "", `{}`, "/v1/monitors/preview"},
		{"POST", "/api/intelligence/monitors", "k1", `{"preview_id":"mpv_1"}`, "/v1/monitors"},
		{"GET", "/api/intelligence/documents/doc_1/revisions", "", "", "/v1/documents/doc_1/revisions"},
		{"GET", "/api/intelligence/documents/doc_1/revisions/2", "", "", "/v1/documents/doc_1/revisions/2"},
		{"POST", "/api/intelligence/research-runs", "rk", `{"question":"q"}`, "/v1/research-runs?async=1"},
		{"GET", "/api/intelligence/research-runs/rr_1", "", "", "/v1/research-runs/rr_1"},
		{"POST", "/api/intelligence/research-runs/rr_1/cancel", "", `{}`, "/v1/research-runs/rr_1/cancel"},
		{"POST", "/api/intelligence/operations/op_1/cancel", "", `{}`, "/v1/operations/op_1/cancel"},
		{"POST", "/api/intelligence/evidence/compare", "", `{"evidence_ids":["a","b"]}`, "/v1/evidence/compare"},
		{"GET", "/api/intelligence/terms", "", "", "/v1/terms"},
		{"POST", "/api/intelligence/terms", "", `{"items":[]}`, "/v1/terms"},
	}
	for _, c := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		if c.idem != "" {
			req.Header.Set("X-Idempotency-Key", c.idem)
		}
		r.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%s %s: status %d %s", c.method, c.path, w.Code, w.Body.String())
		}
		last := got[len(got)-1]
		if last.method != c.method || last.uri != c.wantURI || last.idem != c.idem {
			t.Fatalf("%s %s forwarded as %+v", c.method, c.path, last)
		}
		if c.wantURI == "/v1/monitors" {
			var m map[string]any
			_ = json.Unmarshal([]byte(last.body), &m)
			if m["approval_id"] != "user-direct:u_alice:k1" || m["preview_id"] != "mpv_1" {
				t.Fatalf("monitor body %s", last.body)
			}
		}
	}
	// A REST monitor save without an idempotency key is rejected before reaching the engine.
	n := len(got)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/intelligence/monitors", strings.NewReader(`{}`)))
	if w.Code != 400 || len(got) != n {
		t.Fatalf("monitor without key: %d", w.Code)
	}
}

func TestGapToolsRegistered(t *testing.T) {
	for name, path := range map[string]string{"intel_list_sources": "/v1/sources/health", "intel_compare_evidence": "/v1/evidence/compare"} {
		spec, ok := FindTool(name)
		if !ok || spec.Class != "read" || spec.NeedsApproval {
			t.Fatalf("%s: %+v", name, spec)
		}
		_, p, _ := spec.route(map[string]any{"evidence_ids": []any{"a", "b"}})
		if p != path || !ToolAllowedForSubject(SubjectTopic, name) {
			t.Fatalf("%s routes to %s", name, p)
		}
	}
}
