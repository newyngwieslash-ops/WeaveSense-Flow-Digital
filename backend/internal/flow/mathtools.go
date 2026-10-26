package flow

import (
	"context"
	"strings"
	"time"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

// MathAgent is the trusted platform adapter behind the agent's math tools
// (FR-P04 trusted service adapter). It is implemented by the edumath package
// and runs every call as a platform education job: the actor is re-resolved on
// every call (revocation takes effect at once), the job is reserved and
// settled by the Go ledger, and the idempotency key is the agent tool call.
type MathAgent interface {
	// Enabled reports whether the workspace has the education-math pack
	// installed; other workspaces never see the tools (FR-P03).
	Enabled(db *gorm.DB, scope Scope) bool
	// Call runs one tool. Errors are model-readable.
	Call(ctx context.Context, db *gorm.DB, scope Scope, userID, tool string, args map[string]any, idemKey string) (any, error)
}

// MathToolPrefix names platform math tools. "platform" is not hex, so it can
// never collide with a connection slug (connection slugs are 8 hex chars).
const MathToolPrefix = mcpToolPrefix + "platform_"

type mathToolDef struct {
	Tool, Description string
	Params            map[string]any
	Timeout           time.Duration
}

// MathToolCatalog is the exact tool list of the platform math adapter.
var MathToolCatalog = []mathToolDef{
	{"math_analyze_start", "Start the platform analysis job (validate, solve, verify) for a teacher-confirmed problem revision. Returns a job at once; follow with math_job_wait. Charged to the workspace budget by the platform.",
		obj([]string{"problem_revision_id"}, map[string]any{"problem_revision_id": tStr}), 20 * time.Second},
	{"math_lesson_build_start", "Start building courseware from a verified solution revision. Returns a job at once; follow with math_job_wait.",
		obj([]string{"solution_revision_id"}, map[string]any{"solution_revision_id": tStr}), 20 * time.Second},
	{"math_job_wait", "Wait up to max_wait_seconds (1 to 20, default 10) for a math job. Returns the job state; if it is still running, returns a resume handle: call math_job_wait again with the same job_id. Waiting never restarts or re-charges the job.",
		obj([]string{"job_id"}, map[string]any{"job_id": tStr, "max_wait_seconds": tInt}), 30 * time.Second},
	{"math_job_cancel", "Request cancellation of a math job. Queued work is not dispatched; running work stops at the next stage and only completed stages are charged.",
		obj([]string{"job_id"}, map[string]any{"job_id": tStr}), 20 * time.Second},
}

func IsMathToolName(name string) bool { return strings.HasPrefix(name, MathToolPrefix) }

func (g MCPGateway) mathAgentTools(scope Scope) []map[string]any {
	if g.Math == nil || g.DB == nil || !g.Math.Enabled(g.DB, scope) {
		return nil
	}
	out := make([]map[string]any, 0, len(MathToolCatalog))
	for _, d := range MathToolCatalog {
		out = append(out, map[string]any{"type": "function", "function": map[string]any{
			"name": MathToolPrefix + d.Tool, "description": "[数学服务·平台作业] " + d.Description, "parameters": d.Params}})
	}
	return out
}

// mathSpec synthesizes the spec for a platform math tool in this scope.
func (t Tools) mathSpec(scope Scope, name string) (ToolSpec, bool) {
	if t.MCP == nil || t.MCP.Math == nil || !IsMathToolName(name) {
		return ToolSpec{}, false
	}
	tool := strings.TrimPrefix(name, MathToolPrefix)
	for _, d := range MathToolCatalog {
		if d.Tool != tool {
			continue
		}
		return ToolSpec{Name: name, Description: d.Description, Class: "write", Scope: "intel.mcp.use", Timeout: d.Timeout, OutputCap: 8000, Parameters: d.Params,
			CostBasis: "edu_math_job: platform reservation per job, settled once per completed stage (edu_stage_runs)",
			local: func(t Tools, ctx context.Context, _ model.FlowAgentConversation, ss SubjectScope, userID string, args map[string]any) (any, error) {
				if !t.MCP.Math.Enabled(t.DB, ss.Scope) {
					return nil, ErrToolUnknown
				}
				key, _ := ctx.Value(callKeyCtx{}).(string)
				return t.MCP.Math.Call(ctx, t.DB, ss.Scope, userID, tool, args, key)
			}}, true
	}
	return ToolSpec{}, false
}
