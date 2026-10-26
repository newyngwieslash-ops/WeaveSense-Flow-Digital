package edumath

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

func TestBudgetsAndFairness(t *testing.T) {
	sc, base := newScripted(t)
	runBudgets(t, sc, newHarness(t, base))
}

func TestPGBudgetsAndFairness(t *testing.T) {
	sc, base := newScripted(t)
	runBudgets(t, sc, newHarnessDB(t, base, pgTestDB(t)))
}

// runBudgets (DEV-064): organization and teacher monthly budgets, running-job
// caps per organization and teacher, render caps, alerts once per period and
// level. A refused start reserves nothing and reaches no service; idempotent
// replays and every read of existing work keep working; concurrent starts
// cannot overshoot a cap; only admins change limits.
func runBudgets(t *testing.T, sc *scriptedService, h *harness) {
	tenant := h.setupTeacher()
	t.Setenv("FLOW_PRICE_BOOK", `{"version":"pv_b","unit_micro":{"math_compute_ms":1}}`)
	rev := h.confirmedRevision("2", "1")
	n := 0
	start := func(key string) (int, map[string]any, string) {
		t.Helper()
		n++
		sc.set(func(s *scriptedService) {
			s.scripted, s.accept = true, true
			s.view = sv(fmt.Sprintf("mj_%032x", n), "running", 1)
		})
		code, out, raw, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev+"/analyze", map[string]any{"idempotencyKey": key})
		return code, out, string(raw)
	}
	setBudget := func(body map[string]any) {
		t.Helper()
		if code, _, raw, _ := h.call("u_alice", "PUT", "/api/edu/budget", body); code != 200 {
			t.Fatalf("set budget: %d %s", code, raw)
		}
	}
	reserved := func() int64 {
		var a model.CreditAccount
		h.db.First(&a, "user_id = ?", "u_alice")
		return a.ReservedMicrocredits
	}
	if code, _, _, _ := h.call("u_alice", "PUT", "/api/edu/budget", map[string]any{"monthlyMicro": -1}); code != 400 {
		t.Fatalf("negative limit must be 400, got %d", code)
	}
	if code, _, _, _ := h.call("u_dave", "PUT", "/api/edu/budget", map[string]any{"monthlyMicro": 1}); code < 400 {
		t.Fatalf("outsider changed budget: %d", code)
	}

	// Running-job cap per teacher.
	setBudget(map[string]any{"maxActivePerTeacher": 2})
	var ids []string
	for _, k := range []string{"b1", "b2"} {
		code, out, raw := start(k)
		if code >= 300 {
			t.Fatalf("start %s: %d %s", k, code, raw)
		}
		ids = append(ids, data(out)["id"].(string))
	}
	starts, held := sc.starts.Load(), reserved()
	code, out, raw := start("b3")
	if code != 429 || !strings.Contains(raw, "RATE_LIMITED") {
		t.Fatalf("third running job must be 429 RATE_LIMITED: %d %s", code, raw)
	}
	if sc.starts.Load() != starts || reserved() != held {
		t.Fatal("a refused start must reach no service and reserve nothing")
	}
	if code, out, raw = start("b1"); code >= 300 || data(out)["id"] != ids[0] {
		t.Fatalf("idempotent replay must not be rate limited: %d %s", code, raw)
	}
	// Organization cap counts every teacher's running jobs.
	setBudget(map[string]any{"maxActiveJobs": 2})
	if code, _, raw = start("b3"); code != 429 {
		t.Fatalf("organization cap: %d %s", code, raw)
	}

	// Monthly organization budget: 2 open holds of 120000 already committed.
	setBudget(map[string]any{"monthlyMicro": 300000, "alertPercent": 80})
	if code, _, raw = start("b3"); code != 402 || !strings.Contains(raw, "BUDGET_EXCEEDED") {
		t.Fatalf("over monthly budget must be 402: %d %s", code, raw)
	}
	if sc.starts.Load() != starts || reserved() != held {
		t.Fatal("budget refusal must reach no service and reserve nothing")
	}
	setBudget(map[string]any{"monthlyMicro": 400000, "alertPercent": 80})
	if code, out, raw = start("b3"); code >= 300 {
		t.Fatalf("within budget: %d %s", code, raw)
	}
	ids = append(ids, data(out)["id"].(string))
	setBudget(map[string]any{"monthlyMicro": 1000000, "teacherMonthlyMicro": 100000})
	if code, _, raw = start("b4"); code != 402 || !strings.Contains(raw, "teacher:u_alice") {
		t.Fatalf("teacher monthly budget: %d %s", code, raw)
	}
	start("b4") // second refusal: alert stays single

	// Reads of existing work are never budget gated.
	for _, id := range ids {
		var j model.EduMathJob
		h.db.First(&j, "id = ?", id)
		sc.set(func(s *scriptedService) { s.view = sv(j.ServiceJobID, "running", 1) })
		if code, _, raw, _ := h.call("u_alice", "GET", "/api/edu/jobs/"+id, nil); code != 200 {
			t.Fatalf("read existing job under exhausted budget: %d %s", code, raw)
		}
	}
	code, out, raw2, _ := h.call("u_alice", "GET", "/api/edu/budget", nil)
	if code != 200 {
		t.Fatalf("get budget: %d %s", code, raw2)
	}
	d := data(out)
	if d["organizationCommittedMicro"].(float64) != 360000 || d["myActiveJobs"].(float64) != 3 || d["timezone"] != "Asia/Shanghai" {
		t.Fatalf("budget view: %v", d)
	}
	levels := map[string]int{}
	for _, a := range d["alerts"].([]any) {
		m := a.(map[string]any)
		levels[m["scope"].(string)+"/"+m["level"].(string)]++
	}
	want := map[string]int{"organization/exhausted": 1, "organization/warn": 1, "teacher:u_alice/exhausted": 1}
	if fmt.Sprint(levels) != fmt.Sprint(want) {
		t.Fatalf("alerts once per period, scope and level: %v", levels)
	}
	if code, _, _, _ := h.call("u_dave", "GET", "/api/edu/budget", nil); code == 200 {
		var other []model.EduBudgetAlert
		h.db.Where("tenant_id <> ?", tenant).Find(&other)
		if len(other) != 0 {
			t.Fatal("alerts leaked")
		}
	}

	// Render cap only counts lesson renders.
	setBudget(map[string]any{"maxRenderJobs": 1})
	a := Actor{UserID: "u_alice", TenantID: tenant, Roles: map[string]bool{"teacher": true}}
	err := flow.TenantTx(h.db, tenant, func(tx *gorm.DB) error {
		tx.Create(&model.EduMathJob{ID: "emj_render", TenantID: tenant, WorkspaceID: "w", Kind: "lesson_build", IdemKey: "r", InputHash: "x", Status: "running", Settlement: "settled", CreatedBy: "u_alice"})
		if e := h.svc.admitJob(tx, a, "lesson_edit", 1, true); e == nil || !strings.Contains(e.Error(), "render") {
			t.Fatalf("render cap must refuse a second render: %v", e)
		}
		return h.svc.admitJob(tx, a, "analyze", 1, false)
	})
	if err != nil {
		t.Fatalf("analyze is not a render: %v", err)
	}

	// Concurrent starts cannot overshoot the organization cap.
	setBudget(map[string]any{"maxActiveJobs": 5})
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	sc.set(func(s *scriptedService) {
		s.scripted, s.accept, s.view = true, true, sv("mj_"+strings.Repeat("c", 32), "running", 1)
	})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _, _, _ := h.call("u_alice", "POST", "/api/edu/problem-revisions/"+rev+"/analyze", map[string]any{"idempotencyKey": fmt.Sprintf("race%d", i)})
			mu.Lock()
			codes[code]++
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	var active int64
	h.db.Model(&model.EduMathJob{}).Where("tenant_id = ? AND status IN ?", tenant, activeStatuses).Count(&active)
	if active > 5 || codes[429] == 0 {
		t.Fatalf("cap overshoot: active %d codes %v", active, codes)
	}
}
