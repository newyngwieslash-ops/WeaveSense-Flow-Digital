package edumath

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

// PackID is the industry pack that switches the agent math tools on for a workspace.
const PackID = "education-math"

// AgentTools adapts the agent's mcp_platform_math_* tools to platform jobs
// (flow.MathAgent). Math work never bypasses the Go ledger: every start goes
// through startJob (reservation, frozen price version, stage settlement), and
// waiting only reads and syncs the job, it never restarts or re-charges it.
type AgentTools struct {
	Svc *Service
	// Poll is the wait loop interval (default 1s; tests shorten it).
	Poll time.Duration
	// MaxWait caps one math_job_wait call (default 20s).
	MaxWait time.Duration
}

var _ flow.MathAgent = AgentTools{}

func (t AgentTools) Enabled(db *gorm.DB, scope flow.Scope) bool {
	var n int64
	db.Model(&model.FlowSkillInstall{}).Where("tenant_id = ? AND workspace_id = ? AND pack_id = ?", scope.TenantID, scope.WorkspaceID, PackID).Count(&n)
	return n > 0
}

func str(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return s
}

// AgentError is a model-readable error with a stable code.
type AgentError struct{ Code, Message string }

func (e AgentError) Error() string { return e.Code + ": " + e.Message }

func agentErr(err error) error {
	var ae AgentError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ae):
		return ae
	case errors.Is(err, ErrNoRole), errors.Is(err, ErrForbidden):
		return AgentError{"FORBIDDEN", "需要本机构教师角色；学生或已撤销的成员不能调用数学工具"}
	case errors.Is(err, ErrNotFound):
		return AgentError{"NOT_FOUND", "对象不存在或不在当前工作区"}
	}
	return AgentError{"MATH_TOOL_FAILED", err.Error()}
}

func (t AgentTools) Call(ctx context.Context, db *gorm.DB, scope flow.Scope, userID, tool string, args map[string]any, idemKey string) (any, error) {
	if t.Svc == nil {
		return nil, AgentError{"MATH_SERVICE_UNAVAILABLE", "数学服务未配置"}
	}
	// Re-resolved on every call: a removed teacher or a revoked workspace
	// membership stops working at once, including on resume.
	a, err := StaffActor(db, userID, scope.WorkspaceID)
	if err != nil {
		return nil, agentErr(err)
	}
	if a.TenantID != scope.TenantID || !a.Has("teacher") {
		return nil, agentErr(ErrNoRole)
	}
	if idemKey == "" {
		return nil, AgentError{"IDEMPOTENCY_REQUIRED", "缺少工具调用幂等键"}
	}
	key := "agent:" + idemKey
	switch tool {
	case "math_analyze_start":
		j, err := t.Svc.StartAnalysis(ctx, db, a, str(args, "problem_revision_id"), key)
		if err != nil {
			return nil, agentErr(err)
		}
		return t.view(db, j, false), nil
	case "math_lesson_build_start":
		j, err := t.Svc.StartLessonBuild(ctx, db, a, str(args, "solution_revision_id"), key)
		if err != nil {
			return nil, agentErr(err)
		}
		return t.view(db, j, false), nil
	case "math_job_wait":
		return t.wait(ctx, db, a, str(args, "job_id"), args["max_wait_seconds"])
	case "math_job_cancel":
		j, err := t.job(db, a, str(args, "job_id"))
		if err != nil {
			return nil, err
		}
		j, err = t.Svc.Cancel(ctx, db, a, j)
		if err != nil {
			return nil, agentErr(err)
		}
		return t.view(db, j, false), nil
	}
	return nil, AgentError{"UNKNOWN_TOOL", tool}
}

func (t AgentTools) job(db *gorm.DB, a Actor, id string) (model.EduMathJob, error) {
	var j model.EduMathJob
	if id == "" || db.First(&j, "id = ? AND tenant_id = ? AND workspace_id = ?", id, a.TenantID, a.WorkspaceID).Error != nil {
		return j, agentErr(ErrNotFound)
	}
	return j, nil
}

func waitSeconds(v any, max time.Duration) time.Duration {
	n := 10.0
	switch x := v.(type) {
	case float64:
		n = x
	case int:
		n = float64(x)
	case json.Number:
		n, _ = x.Float64()
	}
	d := time.Duration(n * float64(time.Second))
	if d < time.Second {
		d = time.Second
	}
	if d > max {
		d = max
	}
	return d
}

func done(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "needs_confirmation":
		return true
	}
	return false
}

// wait polls until the job settles or the bounded wait ends, then returns
// either the result or a resume handle (AC-38: long tasks suspend and resume).
func (t AgentTools) wait(ctx context.Context, db *gorm.DB, a Actor, id string, maxArg any) (any, error) {
	j, err := t.job(db, a, id)
	if err != nil {
		return nil, err
	}
	maxWait := t.MaxWait
	if maxWait <= 0 {
		maxWait = 20 * time.Second
	}
	poll := t.Poll
	if poll <= 0 {
		poll = time.Second
	}
	deadline := time.Now().Add(waitSeconds(maxArg, maxWait))
	for {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		j, err = t.Svc.Sync(cctx, db, j)
		cancel()
		if err != nil {
			return nil, agentErr(err)
		}
		if done(j.Status) || !time.Now().Add(poll).Before(deadline) {
			return t.view(db, j, true), nil
		}
		select {
		case <-ctx.Done():
			return t.view(db, j, true), nil
		case <-time.After(poll):
		}
	}
}

// view is the model-facing job summary: identifiers, states and evidence
// levels only. Courseware HTML, media and student material never enter the
// model context (FR-P07); the teacher opens them in the workbench.
func (t AgentTools) view(db *gorm.DB, j model.EduMathJob, waited bool) map[string]any {
	out := map[string]any{"job_id": j.ID, "kind": j.Kind, "status": j.Status, "stage": j.Stage, "progress": j.Progress,
		"price_version": j.PriceVersion, "budget_micro": j.BudgetMicro, "settlement": j.Settlement, "cancel_requested": j.CancelRequested}
	if j.ErrorJSON != "" {
		var e any
		if json.Unmarshal([]byte(j.ErrorJSON), &e) == nil {
			out["error"] = e
		}
	}
	var sol model.EduSolutionRevision
	if db.First(&sol, "job_id = ? AND tenant_id = ?", j.ID, j.TenantID).Error == nil {
		var ev []string
		_ = json.Unmarshal([]byte(sol.EvidenceLevels), &ev)
		out["solution_revision_id"], out["math_state"], out["evidence_levels"] = sol.ID, sol.MathState, ev
		if sol.MathState != "verified" {
			out["next_action"] = "数学状态不是 verified，不得生成课件；请教师查看报告"
		}
	}
	var l model.EduLessonRevision
	if db.First(&l, "job_id = ? AND tenant_id = ?", j.ID, j.TenantID).Error == nil {
		out["lesson_revision_id"], out["lesson_state"] = l.ID, l.State
		out["next_action"] = "课件需教师在备课页审核后才能发布；Agent 不能审核或发布"
	}
	if !done(j.Status) {
		out["done"] = false
		out["resume"] = map[string]any{"tool": flow.MathToolPrefix + "math_job_wait", "arguments": map[string]any{"job_id": j.ID}}
		if j.Status == "reconciliation_required" {
			out["next_action"] = "调用结果未知，平台正按同一幂等键对账；不要重新发起，继续等待"
		}
	} else {
		out["done"] = true
	}
	if waited {
		out["waited"] = true
	}
	return out
}

// MintRef is the credential ref that mints a short-lived, tenant-bound math
// service MCP token for an exact internal target (FR-P04).
const MintRef = "mint:edumath"

// ConfigureGateway enables the platform math tools on the MCP gateway and the
// "mint:edumath" credential for an exact internal target. The platform key
// never leaves the server; each token carries the tenant and a 5 minute expiry.
func (s *Service) ConfigureGateway(g *flow.MCPGateway) {
	g.Math = AgentTools{Svc: s}
	g.Mint = func(_ context.Context, ref, tenantID string) (string, error) {
		if ref != MintRef || tenantID == "" {
			return "", AgentError{"CREDENTIAL_REF", "unknown minted credential ref"}
		}
		return MCPToken(s.Client.Key, tenantID, 5*time.Minute, s.now()), nil
	}
}
