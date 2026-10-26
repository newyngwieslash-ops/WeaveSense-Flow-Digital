package edumath

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// TestAgentMathTools covers FR-P01/P03/P04/P08, AC-36 and AC-38 on the real math
// service: the platform adapter is offered only to workspaces with the
// education-math pack, runs math as a ledger-reserved platform job keyed by the
// tool call, waits in bounded slices with a resume handle, keeps courseware out
// of the model context, and re-checks the teacher role on every call.
func TestAgentMathTools(t *testing.T) { runAgentMath(t, newHarness(t, startMathService(t))) }

func TestPGAgentMathTools(t *testing.T) {
	runAgentMath(t, newHarnessDB(t, startMathService(t), pgTestDB(t)))
}

func runAgentMath(t *testing.T, h *harness) {
	h.setupTeacher()
	rev := h.confirmedRevision("2", "3")
	sc, err := flow.ResolveScope(h.db, "u_alice", "")
	if err != nil {
		t.Fatal(err)
	}
	at := AgentTools{Svc: h.svc, Poll: 50 * time.Millisecond, MaxWait: 20 * time.Second}
	ctx := context.Background()
	// Production runs the adapter inside the tool bridge's tenant-scoped
	// transaction (RLS); the test does the same.
	call := func(user, tool string, args map[string]any, key string) (out any, err error) {
		if terr := flow.UserTx(h.db, user, func(tx *gorm.DB) error {
			out, err = at.Call(ctx, tx, sc, user, tool, args, key)
			return nil
		}); terr != nil && err == nil {
			err = terr
		}
		return out, err
	}
	if at.Enabled(h.db, sc) {
		t.Fatal("tools must be off until the education-math pack is installed")
	}
	ps, _ := flow.SkillPacks()
	for _, p := range ps {
		if p.ID == PackID {
			for _, s := range p.Skills {
				if _, err := flow.InstallSkill(h.db, sc, "u_alice", s.ID, ""); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if !at.Enabled(h.db, sc) {
		t.Fatal("installed pack enables the tools")
	}
	if _, err := call("u_alice", "math_analyze_start", map[string]any{"problem_revision_id": rev}, ""); err == nil {
		t.Fatal("an idempotency key is required")
	}
	if _, err := call("u_bob", "math_analyze_start", map[string]any{"problem_revision_id": rev}, "tool:c:x"); err == nil || !strings.Contains(err.Error(), "FORBIDDEN") {
		t.Fatalf("non member must be refused: %v", err)
	}
	out, err := call("u_alice", "math_analyze_start", map[string]any{"problem_revision_id": rev}, "tool:c:1")
	if err != nil {
		t.Fatal(err)
	}
	first := out.(map[string]any)
	jobID := first["job_id"].(string)
	again, err := call("u_alice", "math_analyze_start", map[string]any{"problem_revision_id": rev}, "tool:c:1")
	if err != nil || again.(map[string]any)["job_id"] != jobID {
		t.Fatalf("same tool call replays the same job: %v %v", err, again)
	}
	var n int64
	h.db.Model(&model.EduMathJob{}).Where("problem_revision_id = ?", rev).Count(&n)
	if n != 1 {
		t.Fatalf("one platform job (one reservation) per tool call key, got %d", n)
	}
	// Bounded waits; each unfinished result carries a resume handle and never restarts the job.
	var view map[string]any
	for i := 0; i < 120; i++ {
		v, err := call("u_alice", "math_job_wait", map[string]any{"job_id": jobID, "max_wait_seconds": float64(1)}, "tool:c:w")
		if err != nil {
			t.Fatal(err)
		}
		view = v.(map[string]any)
		if view["done"] == true {
			break
		}
		resume := view["resume"].(map[string]any)
		if resume["tool"] != flow.MathToolPrefix+"math_job_wait" || resume["arguments"].(map[string]any)["job_id"] != jobID {
			t.Fatalf("resume handle: %v", view)
		}
	}
	if view["status"] != "succeeded" || view["math_state"] != "verified" || view["settlement"] != "settled" || view["solution_revision_id"] == "" {
		t.Fatalf("finished view: %v", view)
	}
	if ev, _ := view["evidence_levels"].([]string); len(ev) == 0 {
		t.Fatalf("evidence levels must be reported: %v", view)
	}
	h.db.Model(&model.EduMathJob{}).Where("problem_revision_id = ?", rev).Count(&n)
	if n != 1 {
		t.Fatal("waiting must not start jobs")
	}
	var stages int64
	h.db.Model(&model.EduStageRun{}).Where("job_id = ?", jobID).Count(&stages)
	if stages == 0 {
		t.Fatal("stages are settled by the platform ledger")
	}

	// Lesson build through the adapter; courseware HTML never enters the model output.
	lo, err := call("u_alice", "math_lesson_build_start", map[string]any{"solution_revision_id": view["solution_revision_id"]}, "tool:c:2")
	if err != nil {
		t.Fatal(err)
	}
	lj := lo.(map[string]any)["job_id"].(string)
	var lv map[string]any
	for i := 0; i < 120; i++ {
		v, err := call("u_alice", "math_job_wait", map[string]any{"job_id": lj, "max_wait_seconds": float64(2)}, "tool:c:w2")
		if err != nil {
			t.Fatal(err)
		}
		if lv = v.(map[string]any); lv["done"] == true {
			break
		}
	}
	raw, _ := json.Marshal(lv)
	if lv["status"] != "succeeded" || lv["lesson_state"] != "ready_for_review" || strings.Contains(string(raw), "<html") || strings.Contains(string(raw), "<!doctype") {
		t.Fatalf("lesson view: %s", raw)
	}

	// FR-P04 on the real service: an exact internal target with a minted,
	// tenant-bound token; only the read tools listed for the target are approvable
	// (start tools stay on the platform adapter so the ledger is never bypassed).
	ep := strings.TrimRight(h.svc.Client.BaseURL, "/") + "/mcp"
	gw := &flow.MCPGateway{DB: h.db, Internal: []flow.InternalTarget{{Endpoint: ep, Tools: []string{"math_job_get", "math_artifact_get"}, CredentialRef: MintRef}}}
	h.svc.ConfigureGateway(gw)
	conn, err := gw.Create(sc, "u_alice", flow.MCPConnectionInput{Name: "math-service", Endpoint: ep, CredentialRef: MintRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Approve(ctx, sc, conn.ID, "op", []string{"math_analyze_start"}); err == nil {
		t.Fatal("start tools are not on the internal target's list")
	}
	if _, err := gw.Approve(ctx, sc, conn.ID, "op", []string{"math_job_get"}); err != nil {
		t.Fatalf("approve over the minted credential: %v", err)
	}
	var sj model.EduMathJob
	h.db.First(&sj, "id = ?", jobID)
	txt, _, err := gw.Execute(ctx, sc, "u_alice", conn.ID, "math_job_get", map[string]any{"job_id": sj.ServiceJobID})
	if err != nil || !strings.Contains(txt, "succeeded") {
		t.Fatalf("math_job_get through the gateway: %v %s", err, txt)
	}

	// Jobs of another workspace or a made-up id are not visible.
	if _, err := call("u_alice", "math_job_wait", map[string]any{"job_id": "nope"}, "tool:c:w3"); err == nil || !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Fatalf("unknown job: %v", err)
	}
	// AC-36: revoking the teacher role blocks the next call, including a resume.
	h.db.Model(&model.EduRoleBinding{}).Where("user_id = ?", "u_alice").Update("status", "revoked")
	if _, err := call("u_alice", "math_job_wait", map[string]any{"job_id": jobID}, "tool:c:w4"); err == nil || !strings.Contains(err.Error(), "FORBIDDEN") {
		t.Fatalf("revoked role must block: %v", err)
	}
	// Minted MCP credential is tenant bound and verifiable by the math service.
	g := &flow.MCPGateway{}
	h.svc.ConfigureGateway(g)
	tok, err := g.Mint(ctx, MintRef, sc.TenantID)
	if err != nil || !strings.HasPrefix(tok, "v1.") {
		t.Fatalf("mint: %v %q", err, tok)
	}
	if _, err := g.Mint(ctx, "mint:other", sc.TenantID); err == nil {
		t.Fatal("unknown mint ref")
	}
	if g.Math == nil {
		t.Fatal("adapter wired")
	}
}
