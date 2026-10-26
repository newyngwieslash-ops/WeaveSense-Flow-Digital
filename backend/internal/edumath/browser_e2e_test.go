package edumath

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestServeBrowserE2E serves a seeded edumath API for the real Chrome E2E
// (web/scripts/edu-chrome-e2e.mjs). It is skipped unless EDU_E2E_ADDR is set.
// The real isolated math service builds and verifies the lesson; identities are
// test-only (X-Test-User injected for the seeded student on /api/edu/student
// paths). Control endpoints exist only in this test file.
func TestServeBrowserE2E(t *testing.T) {
	addr := os.Getenv("EDU_E2E_ADDR")
	if addr == "" {
		t.Skip("EDU_E2E_ADDR not set")
	}
	h := newHarness(t, startMathService(t))
	tenant := h.setupTeacher()
	pubID, lesson := publishG1(t, h)
	_, out, _, _ := h.call("u_alice", "POST", "/api/edu/classes", map[string]any{"name": "浏览器验收班"})
	classID := data(out)["id"].(string)
	_, out, raw, _ := h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/assignments", map[string]any{"publicationId": pubID, "title": "浏览器验收", "answerPolicy": "teacher_release"})
	asID, _ := data(out)["id"].(string)
	if asID == "" {
		t.Fatalf("assignment: %s", raw)
	}
	_, out, _, _ = h.call("u_alice", "POST", "/api/edu/classes/"+classID+"/invites", map[string]any{"ttlHours": 24, "maxUses": 5})
	if c, _, raw, _ := h.call("u_bob", "POST", "/api/edu-join", map[string]any{"code": data(out)["code"]}); c != 200 {
		t.Fatalf("join: %d %s", c, raw)
	}
	h.call("u_alice", "POST", "/api/edu/consents", map[string]any{"studentUserId": "u_bob", "status": "granted", "evidenceRef": "form-e2e-1"})

	done := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/__e2e/release", func(w http.ResponseWriter, r *http.Request) {
		c, _, raw, _ := h.call("u_alice", "POST", "/api/edu/assignments/"+asID+"/release-answer", map[string]any{})
		w.WriteHeader(c)
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("/__e2e/summary", func(w http.ResponseWriter, r *http.Request) {
		c, _, raw, _ := h.call("u_alice", "GET", "/api/edu/assignments/"+asID+"/figure-summary", nil)
		w.WriteHeader(c)
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("/__e2e/done", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(204)
		select {
		case <-done:
		default:
			close(done)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/edu/student/") || strings.HasPrefix(r.URL.Path, "/api/edu/files/") {
			r.Header.Set("X-Test-User", "u_bob")
		} else if strings.HasPrefix(r.URL.Path, "/api/edu/") {
			r.Header.Del("X-Test-User") // the browser acts only as the seeded student
		}
		h.r.ServeHTTP(w, r)
	})
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	defer srv.Close()

	markers := []string{}
	for _, m := range []string{lesson.Answer.Exact, lesson.Answer.Latex, lesson.Answer.Plain} {
		if len(m) >= 3 {
			markers = append(markers, m)
		}
	}
	ready, _ := json.Marshal(map[string]any{"ready": true, "assignmentId": asID, "tenant": tenant, "answerMarkers": markers})
	fmt.Println("EDU_E2E_READY " + string(ready))
	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		t.Fatal("browser E2E did not finish within 10 minutes")
	}
}
