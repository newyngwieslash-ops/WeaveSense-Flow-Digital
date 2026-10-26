package edumath

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

func TestScheduledLedgerReconcile(t *testing.T) {
	runScheduledLedger(t, newHarness(t, startMathService(t)))
}

func TestPGScheduledLedgerReconcile(t *testing.T) {
	runScheduledLedger(t, newHarnessDB(t, startMathService(t), pgTestDB(t)))
}

// runScheduledLedger (DEV-063 scheduled reconciliation): the worker's periodic
// pass reconciles every tenant with recent math jobs in that tenant's own
// transaction, records findings as the system actor, never duplicates an open
// finding on later passes, and leaves clean tenants without findings.
func runScheduledLedger(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	rev := h.confirmedRevision("2", "1")
	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev+"/analyze", map[string]any{"idempotencyKey": "sched"})
	if code >= 300 {
		t.Fatalf("start: %d %s", code, raw)
	}
	h.waitJob("u_alice", data(out)["id"].(string))
	n, found, err := h.svc.ReconcileAll(context.Background(), ledgerWindow)
	if err != nil || n != 1 || found != 0 {
		t.Fatalf("clean pass: tenants %d findings %d err %v", n, found, err)
	}
	now := time.Now().UTC()
	if err := flow.TenantTx(h.db, tenant, func(tx *gorm.DB) error {
		var ws model.Workspace
		tx.First(&ws, "tenant_id = ?", tenant)
		return tx.Create(&model.EduMathJob{ID: "emj_sched_stale", TenantID: tenant, WorkspaceID: ws.ID, Kind: "analyze", IdemKey: "sched-stale", InputHash: "sha256:x",
			ProblemRevisionID: rev, Status: "reconciliation_required", Settlement: "open", CreatedBy: "u_alice", CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)}).Error
	}); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if n, found, err = h.svc.ReconcileAll(context.Background(), ledgerWindow); err != nil || n != 1 || found != 1 {
			t.Fatalf("pass %d: tenants %d findings %d err %v", pass, n, found, err)
		}
	}
	var rows []model.EduLedgerAnomaly
	h.db.Where("tenant_id = ?", tenant).Find(&rows)
	if len(rows) != 1 || rows[0].Kind != AnomUnknownStale || rows[0].Status != "open" {
		t.Fatalf("one open finding expected: %+v", rows)
	}
	var audits int64
	h.db.Model(&model.EduAuditEvent{}).Where("tenant_id = ? AND action = ? AND actor_id = ?", tenant, "ledger.reconcile.scheduled", "system:ledger").Count(&audits)
	if audits != 3 {
		t.Fatalf("each pass is audited as the system actor: %d", audits)
	}
	// The admin view lists the scheduled finding like any other.
	code, out, raw, _ = h.call("u_alice", "GET", "/api/edu/ledger/anomalies", nil)
	if list, _ := out["data"].([]any); code != 200 || len(list) != 1 {
		t.Fatalf("anomalies: %d %s", code, raw)
	}
}

func TestLedgerReconcileEveryEnv(t *testing.T) {
	for v, want := range map[string]time.Duration{"": 24 * time.Hour, "off": 0, "0": 0, "6h": 6 * time.Hour, "1s": 5 * time.Minute, "junk": 24 * time.Hour} {
		t.Setenv("EDU_LEDGER_RECONCILE_EVERY", v)
		if got := LedgerReconcileEvery(); got != want {
			t.Fatalf("%q: got %v want %v", v, got, want)
		}
	}
}
