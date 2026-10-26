package edumath

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// TestFaultInjectionRecovery (DEV-066): a matrix of faults against the real math
// service, each followed by recovery, then one consistency check across all of
// them: every job terminal and settled once, each logical job executed exactly
// once on the service, no anomaly in the ledger reconciliation (stage runs,
// child reservations, usage lines, root reservations), no credit left on hold,
// no duplicate artifacts, and no access across tenants.
//
// Faults: service down at submit (>30 s) with a platform restart before
// recovery; ACK lost after the service accepted; service down mid-run for >30 s;
// cancel racing completion; cancel reply lost. Duplicate and out-of-order
// receipts and replays are covered by TestStageSettlementOutcomes (E27), worker
// restart and ACK loss by E4, invalid and truncated receipts by E30.
func TestFaultInjectionRecovery(t *testing.T) {
	sc, base := newScripted(t)
	h := newHarness(t, base)
	tenant := h.setupTeacher()
	rev := h.confirmedRevision("2", "1")
	start := func(key string) map[string]any {
		t.Helper()
		code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev+"/analyze", map[string]any{"idempotencyKey": key})
		if code >= 300 {
			t.Fatalf("start %s: %d %s", key, code, raw)
		}
		return data(out)
	}
	get := func(id string) map[string]any {
		_, out, _, _ := h.call("u_alice", "GET", "/api/edu/jobs/"+id, nil)
		return data(out)
	}
	advance := func(d time.Duration) { h.now.Store(h.svc.Now().Add(d).Unix()) }
	set := func(f func(*scriptedService)) { sc.set(f) }
	var jobs []string

	// 1) Service down at submit for more than 30 s; the platform restarts before it comes back.
	set(func(s *scriptedService) { s.down = true })
	d := start("fault-down-submit")
	id := d["id"].(string)
	jobs = append(jobs, id)
	if d["status"] != "reconciliation_required" {
		t.Fatalf("submit while down must be an unknown outcome: %v", d)
	}
	advance(45 * time.Second)
	if n, _ := h.svc.SyncOnce(context.Background(), 50); n != 1 || get(id)["status"] != "reconciliation_required" {
		t.Fatal("while down the job must stay held and unknown")
	}
	set(func(s *scriptedService) { s.down = false })
	restarted := &Service{DB: h.db, Client: h.svc.Client, URLKey: h.svc.URLKey, Now: h.svc.Now}
	if _, err := restarted.SyncOnce(context.Background(), 50); err != nil {
		t.Fatal(err)
	}
	if g := h.waitJob("u_alice", id); g["status"] != "succeeded" || g["solutionRevisionId"] == nil {
		t.Fatalf("recovered submit: %v", g)
	}

	// 2) ACK lost after the service accepted: reconciled by key, not resubmitted.
	set(func(s *scriptedService) { s.ackLost = true })
	d = start("fault-ack-lost")
	id = d["id"].(string)
	jobs = append(jobs, id)
	set(func(s *scriptedService) { s.ackLost = false })
	if d["status"] != "reconciliation_required" {
		t.Fatalf("lost ACK must be an unknown outcome: %v", d)
	}
	if g := h.waitJob("u_alice", id); g["status"] != "succeeded" {
		t.Fatalf("lost ACK job: %v", g)
	}

	// 3) Service down mid-run for more than 30 s.
	d = start("fault-down-midrun")
	id = d["id"].(string)
	jobs = append(jobs, id)
	set(func(s *scriptedService) { s.down = true })
	before := get(id)
	for i := 0; i < 5; i++ {
		advance(10 * time.Second)
		_, _ = h.svc.SyncOnce(context.Background(), 50)
		if g := get(id); g["status"] != before["status"] || g["seq"] != before["seq"] {
			t.Fatalf("outage must keep the last known state: %v vs %v", g, before)
		}
	}
	set(func(s *scriptedService) { s.down = false })
	if g := h.waitJob("u_alice", id); g["status"] != "succeeded" {
		t.Fatalf("job after outage: %v", g)
	}

	// 4) Cancel racing completion: whichever wins, the outcome is consistent.
	d = start("fault-cancel-race")
	id = d["id"].(string)
	jobs = append(jobs, id)
	if code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/jobs/"+id+"/cancel", nil); code != 200 {
		t.Fatalf("cancel: %d %s", code, raw)
	}
	if g := h.waitJob("u_alice", id); g["status"] != "cancelled" && g["status"] != "succeeded" {
		t.Fatalf("cancel race: %v", g)
	}

	// 5) Cancel reply lost: the job converges from the service's own state.
	d = start("fault-cancel-lost")
	id = d["id"].(string)
	jobs = append(jobs, id)
	set(func(s *scriptedService) { s.dropCxl = true })
	if code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/jobs/"+id+"/cancel", nil); code != 200 || data(out)["cancelRequested"] != true {
		t.Fatalf("cancel with lost reply: %d %s", code, raw)
	}
	set(func(s *scriptedService) { s.dropCxl = false })
	if g := h.waitJob("u_alice", id); g["status"] != "cancelled" && g["status"] != "succeeded" {
		t.Fatalf("cancel lost: %v", g)
	}

	// Consistency across the whole matrix.
	if got := sc.real.Load(); got != int64(len(jobs)) {
		t.Fatalf("each logical job must execute exactly once on the service: %d submissions for %d jobs", got, len(jobs))
	}
	for i := 0; i < 3; i++ { // the worker finds nothing left to do, repeatedly
		_, _ = h.svc.SyncOnce(context.Background(), 50)
	}
	err := flow.UserTx(h.db, "u_alice", func(tx *gorm.DB) error {
		lines, err := h.svc.LedgerReport(tx, tenant, time.Now().Add(-24*time.Hour), time.Now().Add(24*time.Hour))
		if err != nil {
			return err
		}
		if len(lines) != len(jobs) {
			t.Fatalf("ledger lines %d for %d jobs", len(lines), len(jobs))
		}
		for _, l := range lines {
			if len(l.Anomalies) != 0 || l.Settlement == "open" || (l.Status != "succeeded" && l.Status != "cancelled") {
				t.Fatalf("job %s not consistent after faults: %+v %v", l.JobID, l, l.AnomalyDetails)
			}
			var arts, distinct int64
			tx.Model(&model.EduArtifact{}).Where("job_id = ?", l.JobID).Count(&arts)
			tx.Model(&model.EduArtifact{}).Where("job_id = ?", l.JobID).Distinct("service_artifact_id").Count(&distinct)
			if arts != distinct {
				t.Fatalf("job %s duplicated artifacts", l.JobID)
			}
			var sols int64
			tx.Model(&model.EduSolutionRevision{}).Where("job_id = ?", l.JobID).Count(&sols)
			if (l.Status == "succeeded") != (sols == 1) {
				t.Fatalf("job %s status %s with %d solution revisions", l.JobID, l.Status, sols)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var acc model.CreditAccount
	h.db.First(&acc, "user_id = ?", "u_alice")
	if acc.ReservedMicrocredits != 0 {
		t.Fatalf("credit still on hold after recovery: %d", acc.ReservedMicrocredits)
	}
	for _, id := range jobs {
		if code, _, _, _ := h.call("u_dave", "GET", "/api/edu/jobs/"+id, nil); code != 403 && code != 404 {
			t.Fatalf("other tenant reached job %s: %d", id, code)
		}
	}
}
