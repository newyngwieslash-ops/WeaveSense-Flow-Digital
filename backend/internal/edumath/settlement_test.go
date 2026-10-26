package edumath

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"yingce/backend/internal/model"
)

// scriptedService fronts the real math service; while scripted it answers the job
// endpoints from a script so every settlement outcome is reachable deterministically.
type scriptedService struct {
	mu       sync.Mutex
	scripted bool
	view     JobView // view returned by GET /v1/jobs/{id}
	onCancel JobView // view returned by cancel
	found    bool    // reconcile result
	accept   bool    // POST /v1/jobs: accept (true) or drop the connection (false)
	raw      []byte  // when set, GET /v1/jobs/{id} returns these bytes verbatim
	artMode  string  // "", "truncate", "media": tamper with proxied artifact downloads
	starts   atomic.Int64
	cancels  atomic.Int64
}

func (s *scriptedService) set(f func(*scriptedService)) { s.mu.Lock(); f(s); s.mu.Unlock() }

func sv(id, status string, seq int64, stages ...StageView) JobView {
	full := make([]StageView, 0, 3)
	for _, name := range jobStages["analyze"] {
		st := StageView{Name: name, Status: "pending"}
		for _, x := range stages {
			if x.Name == name {
				st = x
			}
		}
		full = append(full, st)
	}
	v := JobView{SchemaVersion: JobSchema, JobID: id, Kind: "analyze", Status: status, Seq: seq, Stages: full,
		InputHash: "sha256:" + strings.Repeat("ab", 32)}
	for _, st := range stages {
		if st.ComputeMS > 0 {
			v.Usage = append(v.Usage, UsageLine{Stage: st.Name, Unit: "compute_ms", Quantity: st.ComputeMS})
		}
	}
	if status == "failed" {
		v.Error = &ServiceError{Code: "VALIDATION_FAILED", Message: "scripted"}
	}
	return v
}

func newScripted(t *testing.T) (*scriptedService, string) {
	real, _ := url.Parse(startMathService(t))
	proxy := httputil.NewSingleHostReverseProxy(real)
	s := &scriptedService{accept: true}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		scripted, view, onCancel, found, accept, raw, artMode := s.scripted, s.view, s.onCancel, s.found, s.accept, s.raw, s.artMode
		s.mu.Unlock()
		if artMode != "" && strings.HasPrefix(r.URL.Path, "/v1/artifacts/") {
			rec := httptest.NewRecorder()
			proxy.ServeHTTP(rec, r)
			body := rec.Body.Bytes()
			for k, v := range rec.Header() {
				w.Header()[k] = v
			}
			switch artMode {
			case "truncate":
				body = body[:len(body)/2]
			case "media":
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.WriteHeader(rec.Code)
			_, _ = w.Write(body)
			return
		}
		isJob := strings.HasPrefix(r.URL.Path, "/v1/jobs") || r.URL.Path == "/v1/reconcile"
		if r.Method == "POST" && r.URL.Path == "/v1/jobs" {
			s.starts.Add(1)
		}
		if !scripted || !isJob {
			proxy.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1/jobs":
			if !accept {
				hj, _ := w.(http.Hijacker)
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
			w.WriteHeader(202)
			_ = json.NewEncoder(w).Encode(view)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/cancel"):
			s.cancels.Add(1)
			_ = json.NewEncoder(w).Encode(onCancel)
		case r.URL.Path == "/v1/reconcile":
			if found {
				_ = json.NewEncoder(w).Encode(map[string]any{"found": true, "job": view})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"found": false})
			}
		default:
			if raw != nil {
				_, _ = w.Write(raw)
				return
			}
			_ = json.NewEncoder(w).Encode(view)
		}
	}))
	t.Cleanup(srv.Close)
	return s, srv.URL
}

func svcID(n int) string { return fmt.Sprintf("mj_%032x", n) }

// TestStageSettlementOutcomes (DEV-061/062/063, AC-41, AC-46, AC-48): price
// version frozen at acceptance; queued cancel and unknown-then-cancelled never
// dispatch and release the whole hold; failure before any stage releases;
// partial failure and mid-run cancel charge only finished stages; 100 polls and
// replayed views settle each stage exactly once; stale views are ignored.
func TestStageSettlementOutcomes(t *testing.T) {
	sc, base := newScripted(t)
	h := newHarness(t, base)
	h.setupTeacher()
	t.Setenv("FLOW_PRICE_BOOK", `{"version":"pv_a","unit_micro":{"math_compute_ms":1}}`)

	balance := func() (avail, reserved int64) {
		var a model.CreditAccount
		h.db.First(&a, "user_id = ?", "u_alice")
		return a.AvailableMicrocredits, a.ReservedMicrocredits
	}
	job := func(id string) model.EduMathJob {
		var j model.EduMathJob
		h.db.First(&j, "id = ?", id)
		return j
	}
	stageCost := func(id string) (n, sum int64) {
		var rows []model.EduStageRun
		h.db.Where("job_id = ?", id).Find(&rows)
		for _, r := range rows {
			sum += r.CostMicro
		}
		return int64(len(rows)), sum
	}
	reservation := func(id string) model.FlowBudgetReservation {
		var r model.FlowBudgetReservation
		h.db.First(&r, "id = ?", id)
		return r
	}
	start := func(rev, key string) (int, map[string]any) {
		code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev+"/analyze", map[string]any{"idempotencyKey": key})
		if code >= 300 {
			t.Fatalf("start %s: %d %s", key, code, raw)
		}
		return code, data(out)
	}
	get := func(id string) map[string]any {
		_, out, _, _ := h.call("u_alice", "GET", "/api/edu/jobs/"+id, nil)
		return data(out)
	}
	expectBalance := func(label string, wantAvail int64) {
		t.Helper()
		avail, reserved := balance()
		if avail != wantAvail || reserved != 0 {
			t.Fatalf("%s: available %d reserved %d, want %d and 0", label, avail, reserved, wantAvail)
		}
	}
	avail0, _ := balance()

	// 1) Price freeze: real job accepted under pv_a; the book changes mid-run; every stage settles at pv_a.
	rev := h.confirmedRevision("2", "1")
	_, d := start(rev, "freeze")
	freezeID := d["id"].(string)
	if d["priceVersion"] != "pv_a" {
		t.Fatalf("job must record pv_a: %v", d)
	}
	t.Setenv("FLOW_PRICE_BOOK", `{"version":"pv_b","unit_micro":{"math_compute_ms":7}}`)
	if done := h.waitJob("u_alice", freezeID); done["status"] != "succeeded" || done["settlement"] != "settled" {
		t.Fatalf("freeze job: %v", done)
	}
	var stages []model.EduStageRun
	h.db.Where("job_id = ?", freezeID).Find(&stages)
	var spent int64
	for _, st := range stages {
		if st.CostMicro != st.ComputeMS {
			t.Fatalf("stage %s repriced: compute %d cost %d", st.Stage, st.ComputeMS, st.CostMicro)
		}
		spent += st.CostMicro
	}
	var usage []model.FlowUsageRecord
	h.db.Where("subject_ref LIKE ?", "edu_job:"+freezeID+":%").Find(&usage)
	if len(usage) != len(stages) || len(usage) == 0 {
		t.Fatalf("usage lines %d stages %d", len(usage), len(stages))
	}
	for _, u := range usage {
		if u.PriceVersion != "pv_a" || u.UnitPriceMicro != 1 {
			t.Fatalf("usage must carry the frozen book: %+v", u)
		}
	}
	expectBalance("after freeze job", avail0-spent)
	avail0 -= spent
	if j := job(freezeID); JobPriceBook(j).Version != "pv_a" {
		t.Fatal("frozen book must be stored on the job")
	}

	// Scripted outcomes from here on (current book pv_b, unit 7).
	sc.set(func(s *scriptedService) { s.scripted = true })

	// 2) Queued then cancelled before running: no stage, whole hold released.
	sc.set(func(s *scriptedService) {
		s.view = sv(svcID(2), "queued", 1)
		s.onCancel = sv(svcID(2), "cancelled", 2)
	})
	_, d = start(rev, "queued-cancel")
	qid := d["id"].(string)
	if _, r := balance(); r == 0 {
		t.Fatal("accepted job must hold its budget")
	}
	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/jobs/"+qid+"/cancel", nil)
	if code != 200 || data(out)["status"] != "cancelled" || data(out)["settlement"] != "released" {
		t.Fatalf("queued cancel: %d %s", code, raw)
	}
	if n, _ := stageCost(qid); n != 0 {
		t.Fatal("cancelled-before-running job must have no stage runs")
	}
	if r := reservation(job(qid).ReservationID); r.Status != "released" || r.ConsumedMicro != 0 {
		t.Fatalf("root reservation: %+v", r)
	}
	expectBalance("after queued cancel", avail0)

	// 3) Failure before any stage finished: released, nothing charged.
	sc.set(func(s *scriptedService) { s.view = sv(svcID(3), "queued", 1) })
	_, d = start(rev, "early-fail")
	fid := d["id"].(string)
	sc.set(func(s *scriptedService) {
		s.view = sv(svcID(3), "failed", 2, StageView{Name: "validate_input", Status: "failed"})
	})
	if g := get(fid); g["status"] != "failed" || g["settlement"] != "released" {
		t.Fatalf("early fail: %v", g)
	}
	expectBalance("after early failure", avail0)

	// 4) Partial: stage 1 succeeded, stage 2 failed. Both finished stages are charged once at the frozen pv_b price.
	sc.set(func(s *scriptedService) {
		s.view = sv(svcID(4), "running", 1, StageView{Name: "validate_input", Status: "succeeded", ComputeMS: 100})
	})
	_, d = start(rev, "partial")
	pid := d["id"].(string)
	if j := job(pid); j.PriceVersion != "pv_b" {
		t.Fatalf("partial job priced under %s", j.PriceVersion)
	}
	t.Setenv("FLOW_PRICE_BOOK", `{"version":"pv_c","unit_micro":{"math_compute_ms":1000}}`)
	final := sv(svcID(4), "failed", 3, StageView{Name: "validate_input", Status: "succeeded", ComputeMS: 100},
		StageView{Name: "solve", Status: "failed", ComputeMS: 50})
	sc.set(func(s *scriptedService) { s.view = final })
	// 100 polls of the same terminal view, then a replay of an older view: one settlement.
	for i := 0; i < 100; i++ {
		if g := get(pid); g["status"] != "failed" || g["settlement"] != "settled" {
			t.Fatalf("partial poll %d: %v", i, g)
		}
	}
	sc.set(func(s *scriptedService) {
		s.view = sv(svcID(4), "running", 2, StageView{Name: "validate_input", Status: "succeeded", ComputeMS: 100})
	})
	if g := get(pid); g["status"] != "failed" {
		t.Fatalf("stale view must be ignored: %v", g)
	}
	if n, sum := stageCost(pid); n != 2 || sum != 150*7 {
		t.Fatalf("partial stages %d cost %d, want 2 and %d", n, sum, 150*7)
	}
	var lines int64
	h.db.Model(&model.FlowUsageRecord{}).Where("subject_ref LIKE ? AND price_version = ?", "edu_job:"+pid+":%", "pv_b").Count(&lines)
	if lines != 2 {
		t.Fatalf("usage lines at pv_b: %d", lines)
	}
	expectBalance("after partial", avail0-150*7)
	avail0 -= 150 * 7

	// 5) Cancel while running after one stage: only the finished stage is charged.
	sc.set(func(s *scriptedService) {
		s.view = sv(svcID(5), "running", 1, StageView{Name: "validate_input", Status: "succeeded", ComputeMS: 40}, StageView{Name: "solve", Status: "running"})
		s.onCancel = sv(svcID(5), "cancelled", 2, StageView{Name: "validate_input", Status: "succeeded", ComputeMS: 40}, StageView{Name: "solve", Status: "cancelled"})
	})
	_, d = start(rev, "run-cancel")
	cid := d["id"].(string)
	if j := job(cid); j.PriceVersion != "pv_c" {
		t.Fatalf("run-cancel priced under %s", j.PriceVersion)
	}
	code, out, raw, _ = h.call("u_alice", "POST", "/api/edu/jobs/"+cid+"/cancel", nil)
	if code != 200 || data(out)["status"] != "cancelled" || data(out)["settlement"] != "settled" {
		t.Fatalf("running cancel: %d %s", code, raw)
	}
	sc.set(func(s *scriptedService) { s.view = s.onCancel })
	for i := 0; i < 5; i++ {
		get(cid)
	}
	if n, sum := stageCost(cid); n != 1 || sum != 40*1000 {
		t.Fatalf("running cancel stages %d cost %d", n, sum)
	}
	expectBalance("after running cancel", avail0-40*1000)
	avail0 -= 40 * 1000

	// 6) Unknown submit outcome, cancelled while unknown, then proven never executed: held until
	// reconciled, never dispatched again, whole hold released.
	startsBefore := sc.starts.Load()
	sc.set(func(s *scriptedService) { s.accept = false; s.found = false })
	_, d = start(rev, "unknown-cancel")
	uid := d["id"].(string)
	if d["status"] != "reconciliation_required" {
		t.Fatalf("want reconciliation_required: %v", d)
	}
	if _, r := balance(); r == 0 {
		t.Fatal("unknown outcome must keep the hold")
	}
	code, out, _, _ = h.call("u_alice", "POST", "/api/edu/jobs/"+uid+"/cancel", nil)
	if code != 200 || data(out)["status"] != "reconciliation_required" || data(out)["settlement"] != "open" {
		t.Fatalf("cancel while unknown must stay held: %v", out)
	}
	sc.set(func(s *scriptedService) { s.accept = true })
	if g := get(uid); g["status"] != "cancelled" || g["settlement"] != "released" {
		t.Fatalf("unknown then cancelled: %v", g)
	}
	if sc.starts.Load() != startsBefore+1 {
		t.Fatalf("a cancelled job must never be resubmitted (starts %d -> %d)", startsBefore, sc.starts.Load())
	}
	expectBalance("after unknown cancel", avail0)

	// 7) Unknown outcome that the service did accept, cancel asked meanwhile: the cancel is delivered on reconcile.
	cancelsBefore := sc.cancels.Load()
	sc.set(func(s *scriptedService) {
		s.accept = false
		s.found = true
		s.view = sv(svcID(7), "queued", 1)
		s.onCancel = sv(svcID(7), "cancelled", 2)
	})
	_, d = start(rev, "unknown-found-cancel")
	ufid := d["id"].(string)
	h.call("u_alice", "POST", "/api/edu/jobs/"+ufid+"/cancel", nil)
	if g := get(ufid); g["status"] != "cancelled" || g["settlement"] != "released" {
		t.Fatalf("found then cancelled: %v", g)
	}
	if sc.cancels.Load() != cancelsBefore+1 {
		t.Fatal("pending cancel must be delivered once after reconcile")
	}
	expectBalance("after found cancel", avail0)
}
