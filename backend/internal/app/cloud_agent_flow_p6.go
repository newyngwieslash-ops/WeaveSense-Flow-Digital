package app

import (
	"context"
	"encoding/json"
	"fmt"
	"gorm.io/gorm"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

func envLimit(name string) int {
	n, _ := strconv.Atoi(os.Getenv(name))
	return n
}

// piRunSlots: per-user concurrency for agent runs (A15). 0 = unlimited.
func (s *Service) piRunSlots() flow.RunSlots {
	if s.repo == nil || s.repo.DB() == nil {
		return flow.RunSlots{}
	}
	return flow.RunSlots{DB: s.repo.DB(), Limit: envLimit("FLOW_MAX_CONCURRENT_RUNS_PER_USER")}
}

// releasePiRunSlot frees the run's slot and starts the runs now at the head of the queue.
func (s *Service) releasePiRunSlot(runID string) {
	for _, next := range s.piRunSlots().Release(runID) {
		if next.UserID != "" {
			s.startCloudAgentPi(next.UserID, next.ID)
		}
	}
}

// parkFlowOperation records waiting_external for an async engine operation
// returned by an intel tool, with the run request as resume checkpoint (A21).
func (s *Service) parkFlowOperation(db *gorm.DB, userID, runID string, req CloudAgentRequest, ss flow.SubjectScope, callID string, r flow.ToolResult) {
	opID, ok := asyncOperationID(r.Output)
	if !ok {
		return
	}
	out := struct{ OperationID string }{opID}
	cp, _ := json.Marshal(req)
	if _, err := (flow.Waits{DB: db}).Park(model.FlowExternalWait{TenantID: ss.Scope.TenantID, WorkspaceID: ss.Scope.WorkspaceID, UserID: userID,
		RunID: runID, ToolCallID: callID, OperationID: out.OperationID, Reservation: "run:" + runID, Checkpoint: string(cp)}); err != nil {
		slog.Warn("flow: park operation", "run", runID, "error", err)
	}
}

// parksFlowOperation: every intel tool that can start background work
// (collect, research, extract, brief, …) parks; operation inspection/cancel
// tools never do.
func parksFlowOperation(tool string) bool {
	return strings.HasPrefix(tool, "intel_") && tool != "intel_get_operation" && tool != "intel_cancel_operation"
}

// asyncOperationID returns the operation id of a tool output that started (or
// is still running) a background operation; terminal outputs are not parked.
func asyncOperationID(raw json.RawMessage) (string, bool) {
	var out struct {
		OperationID string `json:"operation_id"`
		Status      string `json:"status"`
		Operation   *struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"operation"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return "", false
	}
	// Research whose LLM synthesis is still a queued model task parks on the task.
	var syn struct {
		Synthesis *struct {
			Operation *struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"operation"`
		} `json:"synthesis"`
	}
	if json.Unmarshal(raw, &syn) == nil && syn.Synthesis != nil && syn.Synthesis.Operation != nil && syn.Synthesis.Operation.ID != "" {
		switch syn.Synthesis.Operation.Status {
		case "succeeded", "failed":
		default:
			return syn.Synthesis.Operation.ID, true
		}
	}
	id, st := out.OperationID, out.Status
	if id == "" && out.Operation != nil {
		id, st = out.Operation.ID, out.Operation.Status
	}
	if id == "" {
		return "", false
	}
	switch st {
	case "succeeded", "completed", "failed", "cancelled", "canceled", "partial":
		return "", false
	}
	return id, true
}

// resumeFlowWait auto-continues a parked run exactly once: a continuation run
// (idempotency key per wait) on the same conversation, restored from the checkpoint.
func (s *Service) resumeFlowWait(w model.FlowExternalWait) (string, error) {
	var req CloudAgentRequest
	if err := json.Unmarshal([]byte(w.Checkpoint), &req); err != nil {
		return "", err
	}
	req.Prompt = fmt.Sprintf("[auto-continue] Background operation %s finished with status %s. Read its results (intel_get_operation) and continue the original task.", w.OperationID, w.Outcome)
	req.IdempotencyKey = "autocontinue:" + w.ID
	req.FlowMCPTools = nil
	run, err := s.CreateCloudAgentRun(w.UserID, req, w.RunID)
	if err != nil {
		return "", err
	}
	return run.ID, nil
}

// RunFlowAutoContinue resumes ready waits until ctx ends.
func (s *Service) RunFlowAutoContinue(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if s.repo != nil && s.repo.DB() != nil {
			if _, err := (flow.Waits{DB: s.repo.DB()}).ResumeReady(s.resumeFlowWait); err != nil {
				slog.Warn("flow: auto-continue", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
