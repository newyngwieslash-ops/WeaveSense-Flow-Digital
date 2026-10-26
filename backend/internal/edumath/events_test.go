package edumath

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

func signEvent(body []byte) string {
	mac := hmac.New(sha256.New, []byte(testKey))
	mac.Write([]byte("event\n"))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *harness) postEvent(body []byte, sig string) (int, map[string]any) {
	h.t.Helper()
	req := httptest.NewRequest("POST", "/api/edu-math-events", bytes.NewReader(body))
	req.Header.Set("X-Math-Event-Signature", sig)
	w := httptest.NewRecorder()
	h.r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func eventBody(ev map[string]any) []byte { b, _ := json.Marshal(ev); return b }

// TestJobEventOutbox (DEV-057): every job fact is written in the transaction of
// its state change, numbered per job without gaps; a consumer resumes after its
// last seq; repeated polls and worker passes add nothing; other tenants see nothing.
func TestJobEventOutbox(t *testing.T) { runOutbox(t, newHarness(t, startMathService(t))) }

func TestPGJobEventOutbox(t *testing.T) {
	runOutbox(t, newHarnessDB(t, startMathService(t), pgTestDB(t)))
}

func runOutbox(t *testing.T, h *harness) {
	h.setupTeacher()
	rev := h.confirmedRevision("2", "1")
	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev+"/analyze", map[string]any{"idempotencyKey": "events"})
	if code >= 300 {
		t.Fatalf("start: %d %s", code, raw)
	}
	id := data(out)["id"].(string)
	h.waitJob("u_alice", id)
	for i := 0; i < 50; i++ {
		h.call("u_alice", "GET", "/api/edu/jobs/"+id, nil)
	}
	events := func(after int) []map[string]any {
		t.Helper()
		code, out, raw, _ := h.call("u_alice", "GET", fmt.Sprintf("/api/edu/jobs/%s/events?after=%d", id, after), nil)
		if code != 200 {
			t.Fatalf("events: %d %s", code, raw)
		}
		var evs []map[string]any
		for _, e := range data(out)["events"].([]any) {
			evs = append(evs, e.(map[string]any))
		}
		return evs
	}
	all := events(0)
	var types []string
	for i, e := range all {
		if e["seq"].(float64) != float64(i+1) {
			t.Fatalf("seq must be gap free: %v", all)
		}
		types = append(types, e["type"].(string))
	}
	if types[0] != "job.accepted" || types[len(types)-1] != "job.settled" || !contains(types, "job.succeeded") {
		t.Fatalf("event chain: %v", types)
	}
	seen := map[string]bool{}
	for _, ty := range types {
		if seen[ty] {
			t.Fatalf("each fact once: %v", types)
		}
		seen[ty] = true
	}
	if tail := events(len(all) - 1); len(tail) != 1 || tail[0]["type"] != "job.settled" {
		t.Fatalf("resume after seq: %v", tail)
	}
	if code, _, _, _ := h.call("u_alice", "GET", "/api/edu/jobs/"+id+"/events?after=-1", nil); code != 400 {
		t.Fatalf("negative cursor must be 400, got %d", code)
	}
	if code, _, _, _ := h.call("u_dave", "GET", "/api/edu/jobs/"+id+"/events", nil); code != 403 && code != 404 {
		t.Fatalf("other tenant read events: %d", code)
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// TestMathEventPush (DEV-056): the real service pushes signed events; the
// platform finishes a job with no client polling. Events are hints: forged,
// expired, malformed or oversized events are refused; duplicates and stale
// seqs change nothing; an event claiming a status the service does not confirm
// is checked against the service and the service wins; another tenant's
// signed event cannot reach this tenant's job.
func TestMathEventPush(t *testing.T) { runPush(t, testDB(t)) }

func TestPGMathEventPush(t *testing.T) { runPush(t, pgTestDB(t)) }

func runPush(t *testing.T, db *gorm.DB) {
	var mu sync.Mutex
	var platform http.Handler
	var pushed []string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		p := platform
		pushed = append(pushed, r.URL.Path)
		mu.Unlock()
		if p == nil {
			w.WriteHeader(503)
			return
		}
		p.ServeHTTP(w, r)
	}))
	t.Cleanup(sink.Close)
	base := startMathServiceEnv(t, "MATH_EVENT_URL="+sink.URL+"/api/edu-math-events")
	h := newHarnessDB(t, base, db)
	mu.Lock()
	platform = h.r
	mu.Unlock()
	tenant := h.setupTeacher()
	in := func(fn func(tx *gorm.DB) error) {
		t.Helper()
		if err := flow.TenantTx(h.db, tenant, fn); err != nil {
			t.Fatal(err)
		}
	}
	rev := h.confirmedRevision("2", "1")
	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev+"/analyze", map[string]any{"idempotencyKey": "push"})
	if code >= 300 {
		t.Fatalf("start: %d %s", code, raw)
	}
	id := data(out)["id"].(string)

	// No GET /jobs/:id and no worker: only pushed events move the job.
	var job model.EduMathJob
	for i := 0; i < 200; i++ {
		in(func(tx *gorm.DB) error { return tx.First(&job, "id = ?", id).Error })
		if job.Status == "succeeded" && job.Settlement == "settled" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if job.Status != "succeeded" || job.Settlement != "settled" {
		t.Fatalf("pushed events must finish the job: %+v", job)
	}
	var sol int64
	var applied int64
	in(func(tx *gorm.DB) error {
		tx.Model(&model.EduSolutionRevision{}).Where("job_id = ?", id).Count(&sol)
		return tx.Model(&model.EduMathEvent{}).Where("job_id = ? AND result = ?", id, "applied").Count(&applied).Error
	})
	if sol != 1 || applied == 0 {
		t.Fatalf("solution %d, applied events %d", sol, applied)
	}

	now := time.Now().Unix()
	ev := map[string]any{"schema_version": EventSchema, "event_id": "me_" + strings.Repeat("a", 32), "tenant_id": tenant, "job_id": job.ServiceJobID,
		"request_id": id, "seq": job.Seq + 50, "status": "failed", "sent_at": now}
	b := eventBody(ev)
	// Forged, expired, malformed and oversized events are refused.
	if code, _ := h.postEvent(b, strings.Repeat("0", 64)); code != 401 {
		t.Fatalf("forged signature: %d", code)
	}
	old := map[string]any{}
	for k, v := range ev {
		old[k] = v
	}
	old["sent_at"] = now - 3600
	if code, _ := h.postEvent(eventBody(old), signEvent(eventBody(old))); code != 401 {
		t.Fatalf("expired event: %d", code)
	}
	extra := append(b[:len(b)-1:len(b)-1], []byte(`,"answer":"x"}`)...)
	if code, _ := h.postEvent(extra, signEvent(extra)); code != 400 {
		t.Fatalf("unknown field: %d", code)
	}
	big := bytes.Repeat([]byte(" "), maxEventBytes+1)
	if code, _ := h.postEvent(big, signEvent(big)); code != 413 {
		t.Fatalf("oversized: %d", code)
	}
	// A signed event claiming failure for a succeeded job: re-read, service wins.
	code, res := h.postEvent(b, signEvent(b))
	if code != 202 || data(res)["result"] != "applied" {
		t.Fatalf("claim event: %d %v", code, res)
	}
	in(func(tx *gorm.DB) error { return tx.First(&job, "id = ?", id).Error })
	if job.Status != "succeeded" {
		t.Fatalf("an event must never set status by itself: %+v", job)
	}
	// Duplicate delivery of the same event.
	if code, res := h.postEvent(b, signEvent(b)); code != 202 || data(res)["result"] != "duplicate" {
		t.Fatalf("duplicate: %d %v", code, res)
	}
	// Stale seq for a known job.
	ev["event_id"], ev["seq"] = "me_"+strings.Repeat("b", 32), 1
	b = eventBody(ev)
	if code, res := h.postEvent(b, signEvent(b)); code != 202 || data(res)["result"] != "stale" {
		t.Fatalf("stale: %d %v", code, res)
	}
	// Another tenant's signed event naming this job reaches nothing (RLS + tenant filter).
	ev["event_id"], ev["seq"], ev["tenant_id"] = "me_"+strings.Repeat("c", 32), job.Seq+99, "tn_u_u_dave"
	b = eventBody(ev)
	if code, res := h.postEvent(b, signEvent(b)); code != 202 || data(res)["result"] != "unknown_job" {
		t.Fatalf("cross tenant: %d %v", code, res)
	}
	var evs int64
	in(func(tx *gorm.DB) error {
		return tx.Model(&model.EduJobEvent{}).Where("job_id = ? AND type = ?", id, "job.settled").Count(&evs).Error
	})
	if evs != 1 {
		t.Fatalf("settled exactly once across push and checks: %d", evs)
	}
	mu.Lock()
	n := len(pushed)
	mu.Unlock()
	if n < 3 {
		t.Fatalf("service must have pushed events: %d", n)
	}
}
