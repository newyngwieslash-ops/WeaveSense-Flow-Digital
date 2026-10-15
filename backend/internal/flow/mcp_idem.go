package flow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gorm.io/gorm/clause"

	"yingce/backend/internal/model"
)

var (
	errMCPSent = errors.New("mcp call outcome unknown")
	// ErrMCPUnknownOutcome: the previous attempt may have executed remotely and
	// could not be reconciled; the call is not retried blindly.
	ErrMCPUnknownOutcome = errors.New("flow: mcp call outcome unknown; reconciliation pending, not retried")
	ErrMCPKeyConflict    = errors.New("flow: mcp idempotency key reused with different arguments")
)

// ExecuteKeyed runs an MCP tool at most once per (tenant, key):
//   - succeeded: returns the stored output, no new call and no new charge;
//   - unknown (request sent, result lost): reconciles first via the
//     connection's ReconcileTool; re-executes only if the server reports
//     not_found; otherwise stays unknown and returns ErrMCPUnknownOutcome;
//   - failed (definitely not executed): may be retried.
//
// Credits are charged once, when a call first becomes succeeded.
func (g MCPGateway) ExecuteKeyed(ctx context.Context, scope Scope, actorID, connID, tool string, args map[string]any, key string) (string, bool, error) {
	if key == "" {
		return g.Execute(ctx, scope, actorID, connID, tool, args)
	}
	raw, _ := json.Marshal(args)
	sum := sha256.Sum256(append([]byte(connID+"\x00"+tool+"\x00"), raw...))
	hash := hex.EncodeToString(sum[:])
	row := model.FlowMCPCall{ID: NewID("mc_"), TenantID: scope.TenantID, IdemKey: key, ConnectionID: connID, Tool: tool, ArgsHash: hash, Status: "pending", CreatedAt: g.now(), UpdatedAt: g.now()}
	ins := g.DB.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if ins.Error != nil {
		return "", false, ins.Error
	}
	if ins.RowsAffected == 0 {
		row = model.FlowMCPCall{}
		if err := g.DB.First(&row, "tenant_id = ? AND idem_key = ?", scope.TenantID, key).Error; err != nil {
			return "", false, err
		}
		if row.ArgsHash != hash {
			return "", false, ErrMCPKeyConflict
		}
		switch row.Status {
		case "succeeded":
			return row.Output, row.Truncated, nil
		case "pending", StatusReconciliation:
			found, out, err := g.reconcile(ctx, scope, row)
			if err != nil {
				return "", false, err
			}
			if found {
				return out, false, nil
			}
		}
		// failed, or reconciled as not executed: retry under the same key.
		g.DB.Model(&model.FlowMCPCall{}).Where("id = ?", row.ID).Updates(map[string]any{"status": "pending", "updated_at": g.now()})
	}
	g.DB.Model(&model.FlowMCPCall{}).Where("id = ?", row.ID).Update("attempts", row.Attempts+1)
	out, truncated, err := g.execute(ctx, scope, actorID, connID, tool, args, key)
	switch {
	case err == nil:
		g.DB.Model(&model.FlowMCPCall{}).Where("id = ?", row.ID).Updates(map[string]any{"status": "succeeded", "output": out, "truncated": truncated, "billed": true, "updated_at": g.now()})
		return out, truncated, nil
	case errors.Is(err, errMCPSent):
		CountMCPError("unknown_outcome")
		g.DB.Model(&model.FlowMCPCall{}).Where("id = ?", row.ID).Updates(map[string]any{"status": StatusReconciliation, "updated_at": g.now()})
		return "", false, fmt.Errorf("%w (%v)", ErrMCPUnknownOutcome, err)
	default:
		CountMCPError(mcpErrorKind(err))
		g.DB.Model(&model.FlowMCPCall{}).Where("id = ?", row.ID).Updates(map[string]any{"status": "failed", "updated_at": g.now()})
		return "", false, err
	}
}

// reconcile asks the server what happened to an unknown call. found=true means
// it executed: the stored row becomes succeeded and is charged exactly once.
func (g MCPGateway) reconcile(ctx context.Context, scope Scope, row model.FlowMCPCall) (bool, string, error) {
	c, err := g.get(scope, row.ConnectionID)
	if err != nil {
		return false, "", err
	}
	v := g.View(c)
	if v.CostRule.ReconcileTool == "" {
		return false, "", ErrMCPUnknownOutcome
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutMS)*time.Millisecond)
	defer cancel()
	cs, secret, err := g.session(cctx, c)
	if err != nil {
		return false, "", ErrMCPUnknownOutcome
	}
	defer cs.Close()
	res, err := cs.CallTool(cctx, &mcp.CallToolParams{Name: v.CostRule.ReconcileTool, Arguments: map[string]any{"idempotency_key": row.IdemKey}})
	if err != nil || res.IsError {
		return false, "", ErrMCPUnknownOutcome
	}
	var b strings.Builder
	for _, ct := range res.Content {
		if t, ok := ct.(*mcp.TextContent); ok {
			b.WriteString(t.Text)
		}
	}
	var st struct {
		Status string `json:"status"`
		Output string `json:"output"`
	}
	if json.Unmarshal([]byte(b.String()), &st) != nil {
		return false, "", ErrMCPUnknownOutcome
	}
	switch st.Status {
	case "succeeded":
		out := redact(st.Output, secret)
		upd := g.DB.Model(&model.FlowMCPCall{}).Where("id = ? AND status IN ('pending','reconciliation_required')", row.ID).
			Updates(map[string]any{"status": "succeeded", "output": out, "billed": true, "updated_at": g.now()})
		if upd.RowsAffected == 1 {
			g.usage(c, row.Tool, "reconcile", "succeeded", g.now(), len(out), v.CostRule.PerCallCredits)
		}
		return true, out, nil
	case "not_found":
		return false, "", nil
	}
	return false, "", ErrMCPUnknownOutcome
}

func mcpErrorKind(err error) string {
	switch {
	case errors.Is(err, ErrMCPSchemaDrift):
		return "schema_drift"
	case errors.Is(err, ErrMCPBudget):
		return "budget_blocked"
	case errors.Is(err, ErrMCPToolDenied):
		return "denied"
	default:
		return "call_failed"
	}
}
