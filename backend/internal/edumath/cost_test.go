package edumath

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

func TestDeliveryCostReport(t *testing.T) { runCostReport(t, newHarness(t, startMathService(t))) }

func TestPGDeliveryCostReport(t *testing.T) {
	runCostReport(t, newHarnessDB(t, startMathService(t), pgTestDB(t)))
}

// runCostReport (DEV-065): after the real G1 chain the period report splits
// delivered compute, rendering, failed or cancelled work and human support;
// its metered machine cost equals the ledger's stage cost to the micro unit;
// unconnected providers are flagged not metered rather than zero cost; support
// time is attributed and audited; denominators are accepted lessons and active
// classes; only admins see it.
func runCostReport(t *testing.T, h *harness) {
	runG1(t, h)
	tenant := model.PersonalTenantID("u_alice")
	period, _ := budgetPeriod(h.svc.now())
	report := func() map[string]any {
		t.Helper()
		code, out, raw, hd := h.call("u_alice", "GET", "/api/edu/cost-report?period="+period, nil)
		if code != 200 || hd.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("cost report: %d %s", code, raw)
		}
		return data(out)
	}
	r := report()
	lines := map[string]map[string]any{}
	var sum float64
	for _, x := range r["lines"].([]any) {
		m := x.(map[string]any)
		lines[m["component"].(string)] = m
		sum += m["costMicro"].(float64)
	}
	if sum != r["totalMicro"].(float64) || lines["compute_succeeded"]["costMicro"].(float64) <= 0 {
		t.Fatalf("report lines: %v", r)
	}
	for _, k := range []string{"tts", "egress", "storage"} {
		if lines[k]["metered"] != false {
			t.Fatalf("%s must be flagged not metered: %v", k, lines[k])
		}
	}
	if lines["storage"]["quantity"].(float64) <= 0 {
		t.Fatalf("stored artifact bytes must be counted: %v", lines["storage"])
	}
	// Machine cost equals the ledger's stage cost.
	var ledger int64
	if err := flow.TenantTx(h.db, tenant, func(tx *gorm.DB) error {
		ls, err := h.svc.LedgerReport(tx, tenant, time.Now().AddDate(0, -2, 0), time.Now().Add(time.Hour))
		for _, l := range ls {
			ledger += l.StageCost
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	machine := lines["compute_succeeded"]["costMicro"].(float64) + lines["render"]["costMicro"].(float64) + lines["failed_and_cancelled"]["costMicro"].(float64)
	if int64(machine) != ledger {
		t.Fatalf("machine cost %v != ledger stage cost %d", machine, ledger)
	}
	if r["acceptedLessons"].(float64) < 1 || r["perAcceptedLessonMicro"] == nil {
		t.Fatalf("denominators: %v", r)
	}

	// Human support with attribution.
	for _, bad := range []map[string]any{{"period": "2026-13", "category": "other", "minutes": 5}, {"period": period, "category": "lunch", "minutes": 5},
		{"period": period, "category": "other", "minutes": 0}, {"period": period, "category": "other", "minutes": 5, "costMicro": -1}} {
		if code, _, _, _ := h.call("u_alice", "POST", "/api/edu/support-costs", bad); code != 400 {
			t.Fatalf("bad support cost %v: %d", bad, code)
		}
	}
	code, _, raw, _ := h.call("u_alice", "POST", "/api/edu/support-costs", map[string]any{"period": period, "category": "review_support", "minutes": 30,
		"costMicro": 50000, "subjectRef": "class:c1", "note": "审核辅导"})
	if code != 201 {
		t.Fatalf("support cost: %d %s", code, raw)
	}
	h.call("u_alice", "POST", "/api/edu/support-costs", map[string]any{"period": period, "category": "incident", "minutes": 10, "costMicro": 10000})
	r2 := report()
	if r2["totalMicro"].(float64) != r["totalMicro"].(float64)+60000 || r2["supportUnattributed"].(float64) != 1 ||
		r2["supportByCategoryMicro"].(map[string]any)["review_support"].(float64) != 50000 {
		t.Fatalf("support in report: %v", r2)
	}
	code, out, _, _ := h.call("u_alice", "GET", "/api/edu/support-costs?period="+period, nil)
	if rows, _ := out["data"].([]any); code != 200 || len(rows) != 2 || rows[0].(map[string]any)["recordedBy"] != "u_alice" {
		t.Fatalf("support list: %d %v", code, out)
	}
	if code, _, _, _ := h.call("u_alice", "GET", "/api/edu/cost-report?period=26-1", nil); code != 400 {
		t.Fatalf("bad period must be 400, got %d", code)
	}
	if code, _, _, _ := h.call("u_dave", "GET", "/api/edu/cost-report", nil); code == 200 {
		t.Fatal("outsider read cost report")
	}
}
