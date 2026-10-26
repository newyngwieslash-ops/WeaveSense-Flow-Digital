package edumath

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

func TestLedgerReconcileAndExport(t *testing.T) { runLedger(t, newHarness(t, startMathService(t))) }

func TestPGLedgerReconcileAndExport(t *testing.T) {
	runLedger(t, newHarnessDB(t, startMathService(t), pgTestDB(t)))
}

// runLedger (DEV-063, AC-46): a clean real job reconciles with no findings and
// matching totals; 100 polls change nothing; the CSV export is private and
// complete; tampered usage, a stuck settlement and a stale unknown outcome are
// found once each, carry an owner and status, reopen when they persist after
// being resolved, and stay resolved once fixed; non-admin staff and other
// tenants are refused.
func runLedger(t *testing.T, h *harness) {
	tenant := h.setupTeacher()
	as := func(fn func(tx *gorm.DB) error) {
		t.Helper()
		if err := flow.UserTx(h.db, "u_alice", fn); err != nil {
			t.Fatal(err)
		}
	}
	rev := h.confirmedRevision("2", "1")
	code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev+"/analyze", map[string]any{"idempotencyKey": "ledger"})
	if code >= 300 {
		t.Fatalf("start: %d %s", code, raw)
	}
	jobID := data(out)["id"].(string)
	if d := h.waitJob("u_alice", jobID); d["status"] != "succeeded" || d["settlement"] != "settled" {
		t.Fatalf("job: %v", d)
	}
	for i := 0; i < 100; i++ {
		h.call("u_alice", "GET", "/api/edu/jobs/"+jobID, nil)
	}

	// Clean report.
	code, out, raw, _ = h.call("u_alice", "GET", "/api/edu/ledger/report", nil)
	if code != 200 {
		t.Fatalf("report: %d %s", code, raw)
	}
	lines, _ := data(out)["lines"].([]any)
	if len(lines) != 1 {
		t.Fatalf("want 1 line: %s", raw)
	}
	l := lines[0].(map[string]any)
	if len(l["anomalies"].([]any)) != 0 || l["stageCostMicro"].(float64) <= 0 || l["stageCostMicro"] != l["childConsumedMicro"] ||
		l["stageCostMicro"] != l["usageCostMicro"] || l["rootConsumedMicro"] != l["childConsumedMicro"] || l["rootStatus"] != "settled" {
		t.Fatalf("clean job must reconcile: %v", l)
	}
	if l["stages"] != l["usageLines"] {
		t.Fatalf("one usage line per stage: %v", l)
	}

	// CSV audit export.
	code, _, raw, hdr := h.call("u_alice", "GET", "/api/edu/ledger/report?format=csv", nil)
	rows := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") || hdr.Get("Cache-Control") != "private, no-store" ||
		len(rows) != 2 || !strings.HasPrefix(rows[0], "job_id,kind,status,settlement,price_version") || !strings.HasPrefix(rows[1], jobID+",analyze,succeeded,settled,") {
		t.Fatalf("csv export: %d %v %q", code, hdr, raw)
	}
	if code, _, _, _ := h.call("u_alice", "GET", "/api/edu/ledger/report?from=yesterday", nil); code != 400 {
		t.Fatalf("bad range must be 400, got %d", code)
	}

	// Non-admin teacher in the same tenant and a user of another tenant are refused.
	now := time.Now().UTC()
	h.db.Create(&model.Membership{ID: "m_carol_l", TenantID: tenant, UserID: "u_carol", Role: "member", Status: "active", CreatedAt: now})
	var ws model.Workspace
	h.db.First(&ws, "tenant_id = ?", tenant)
	h.db.Create(&model.WorkspaceMembership{ID: "wm_carol_l", WorkspaceID: ws.ID, UserID: "u_carol", Role: "editor", CreatedAt: now})
	if code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/roles", map[string]any{"tenantId": tenant, "userId": "u_carol", "role": "teacher"}); code != 200 {
		t.Fatalf("bind carol: %d %s", code, raw)
	}
	carol := []string{"X-Workspace-Id", ws.ID}
	for _, p := range []string{"GET /api/edu/ledger/report", "GET /api/edu/ledger/report?format=csv", "POST /api/edu/ledger/reconcile", "GET /api/edu/ledger/anomalies"} {
		m, path, _ := strings.Cut(p, " ")
		if code, _, raw, _ := h.call("u_carol", m, path, nil, carol...); code != 403 {
			t.Fatalf("%s as non-admin teacher: %d %s", p, code, raw)
		}
	}

	// Inject three faults.
	var use model.FlowUsageRecord
	var orig int64
	as(func(tx *gorm.DB) error {
		if err := tx.First(&use, "subject_ref LIKE ?", "edu_job:"+jobID+":%").Error; err != nil {
			return err
		}
		orig = use.CostMicro
		if err := tx.Model(&use).Update("cost_micro", orig+5).Error; err != nil {
			return err
		}
		base := model.EduMathJob{TenantID: tenant, WorkspaceID: ws.ID, Kind: "analyze", InputHash: "sha256:x", ProblemRevisionID: rev, PriceVersion: "price_v1",
			CreatedBy: "u_alice", CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)}
		stuck, stale := base, base
		stuck.ID, stuck.IdemKey, stuck.Status, stuck.Settlement = "emj_ledger_stuck", "ledger-stuck", "failed", "open"
		stale.ID, stale.IdemKey, stale.Status, stale.Settlement = "emj_ledger_stale", "ledger-stale", "reconciliation_required", "open"
		if err := tx.Create(&stuck).Error; err != nil {
			return err
		}
		return tx.Create(&stale).Error
	})
	reconcile := func() map[string]any {
		t.Helper()
		code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/ledger/reconcile", nil)
		if code != 200 {
			t.Fatalf("reconcile: %d %s", code, raw)
		}
		return data(out)
	}
	r := reconcile()
	if r["findings"].(float64) != 3 || len(r["open"].([]any)) != 3 {
		t.Fatalf("want 3 findings: %v", r)
	}
	kinds := map[string]string{}
	for _, x := range r["open"].([]any) {
		m := x.(map[string]any)
		kinds[m["kind"].(string)] = m["id"].(string)
		if m["status"] != "open" {
			t.Fatalf("new anomaly must be open: %v", m)
		}
	}
	for _, k := range []string{AnomUsageMismatch, AnomSettlementStuck, AnomUnknownStale} {
		if kinds[k] == "" {
			t.Fatalf("missing %s in %v", k, kinds)
		}
	}
	if r = reconcile(); len(r["open"].([]any)) != 3 {
		t.Fatalf("rerun must not duplicate: %v", r)
	}

	// Handling workflow.
	id := kinds[AnomUsageMismatch]
	upd := func(body map[string]any) (int, map[string]any) {
		code, out, _, _ := h.call("u_alice", "POST", "/api/edu/ledger/anomalies/"+id, body)
		return code, data(out)
	}
	if code, _ := upd(map[string]any{"status": "investigating"}); code != 400 {
		t.Fatal("investigating needs an owner")
	}
	if code, d := upd(map[string]any{"status": "investigating", "owner": "u_alice"}); code != 200 || d["owner"] != "u_alice" {
		t.Fatalf("investigate: %d %v", code, d)
	}
	if code, _ := upd(map[string]any{"status": "resolved"}); code != 400 {
		t.Fatal("resolving needs a note")
	}
	if code, d := upd(map[string]any{"status": "resolved", "note": "复核中"}); code != 200 || d["status"] != "resolved" || d["resolvedBy"] != "u_alice" {
		t.Fatalf("resolve: %d %v", code, d)
	}
	reconcile() // still wrong → reopened
	var a model.EduLedgerAnomaly
	as(func(tx *gorm.DB) error { return tx.First(&a, "id = ?", id).Error })
	if a.Status != "open" || a.ResolvedAt != nil {
		t.Fatalf("a persisting anomaly must reopen: %+v", a)
	}
	as(func(tx *gorm.DB) error { return tx.Model(&use).Update("cost_micro", orig).Error })
	upd(map[string]any{"status": "resolved", "note": "用量行已按阶段记录更正"})
	if r = reconcile(); r["findings"].(float64) != 2 {
		t.Fatalf("fixed usage must not be found again: %v", r)
	}
	as(func(tx *gorm.DB) error { return tx.First(&a, "id = ?", id).Error })
	if a.Status != "resolved" {
		t.Fatalf("fixed anomaly must stay resolved: %+v", a)
	}
	code, out, _, _ = h.call("u_alice", "GET", "/api/edu/ledger/anomalies?status=resolved", nil)
	if res, _ := out["data"].([]any); code != 200 || len(res) != 1 {
		t.Fatalf("resolved list: %d %v", code, out)
	}

	// Another tenant sees nothing and cannot touch this tenant's anomaly.
	if code, _, raw, _ := h.call("u_dave", "POST", "/api/edu/roles", map[string]any{"tenantId": model.PersonalTenantID("u_dave"), "userId": "u_dave", "role": "teacher"}); code != 200 {
		t.Fatalf("bind dave: %d %s", code, raw)
	}
	code, out, _, _ = h.call("u_dave", "GET", "/api/edu/ledger/report", nil)
	if code != 200 || len(data(out)["lines"].([]any)) != 0 {
		t.Fatalf("other tenant report: %d %v", code, out)
	}
	if code, _, _, _ := h.call("u_dave", "POST", "/api/edu/ledger/anomalies/"+id, map[string]any{"status": "resolved", "note": "x"}); code != 404 {
		t.Fatalf("cross tenant update must be 404, got %d", code)
	}
	var audits int64
	as(func(tx *gorm.DB) error {
		return tx.Model(&model.EduAuditEvent{}).Where("tenant_id = ? AND action LIKE ?", tenant, "ledger.%").Count(&audits).Error
	})
	if audits < 8 {
		t.Fatalf("ledger actions must be audited, got %d", audits)
	}
}
