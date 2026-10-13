package flow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

// intel_start_research no longer blocks on the model: it returns the queued
// model task as an operation, the run parks on it, the worker completes it,
// NotifyDone releases the parked run, and intel_get_operation(mt_…) returns
// the citation-validated synthesis.
func TestResearchSynthesisIsAsync(t *testing.T) {
	tools, _, fe, conv, ss := setupAgent(t)
	fund(t, Billing{DB: tools.DB}, "u_alice", 10_000_000)
	gw := fakeGW{text: `{"body":"Deal summary","claims":[{"text":"Acme signed","kind":"source_statement","citations":[{"evidence_id":"ev1","revision":1}]}]}`, cost: 1000}
	tools.Model = gw
	r, err := tools.Invoke(context.Background(), conv, ss, "u_alice", "c1", "intel_start_research", map[string]any{"question": "Acme lithium?"}, "")
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("research: %v %+v", err, r)
	}
	var out struct {
		Synthesis struct {
			Status    string `json:"status"`
			Operation struct {
				ID, Status string
			} `json:"operation"`
		} `json:"synthesis"`
	}
	_ = json.Unmarshal(r.Output, &out)
	taskID := out.Synthesis.Operation.ID
	if !strings.HasPrefix(taskID, "mt_") || out.Synthesis.Status != "queued" {
		t.Fatalf("research blocked or returned no operation: %s", r.Output)
	}
	// The agent run parks on the task.
	tools.DB.Create(&model.FlowExternalWait{ID: "w1", TenantID: ss.Scope.TenantID, WorkspaceID: ss.Scope.WorkspaceID, UserID: "u_alice", RunID: "run1", OperationID: taskID, Status: "waiting", Checkpoint: "{}"})
	op, _ := tools.Invoke(context.Background(), conv, ss, "u_alice", "c2", "intel_get_operation", map[string]any{"operation_id": taskID}, "")
	if !strings.Contains(string(op.Output), `"status":"queued"`) {
		t.Fatalf("pending op: %s", op.Output)
	}
	// Async pipeline completes it and pushes completion once.
	notified := 0
	tasks := ModelTasks{DB: tools.DB, GW: gw, Notify: func(model.FlowModelTask) error { notified++; return nil }}
	if n, _ := tasks.RunDue(context.Background()); n != 1 {
		t.Fatalf("worker ran %d", n)
	}
	if n := tasks.NotifyDone(context.Background()); n != 1 || notified != 1 {
		t.Fatalf("notify %d/%d", n, notified)
	}
	if n := tasks.NotifyDone(context.Background()); n != 0 || notified != 1 {
		t.Fatal("completion pushed twice")
	}
	var w model.FlowExternalWait
	tools.DB.First(&w, "id = ?", "w1")
	if w.Status != "ready" || w.Outcome != "succeeded" {
		t.Fatalf("parked run not released: %+v", w)
	}
	op, _ = tools.Invoke(context.Background(), conv, ss, "u_alice", "c3", "intel_get_operation", map[string]any{"operation_id": taskID}, "")
	if !strings.Contains(string(op.Output), "Deal summary") || !strings.Contains(string(op.Output), "Acme signed") {
		t.Fatalf("completed op: %s", op.Output)
	}
	// Another tenant cannot read the task.
	if _, err := ModelTaskOperation(tools.DB, "t_other", taskID); err == nil {
		t.Fatal("cross-tenant model task read")
	}
	// Brief wake-up push hits the engine with the window key.
	notify := BriefWakeNotifier(tools.Engine)
	if err := notify(model.FlowModelTask{ID: "mt_b", TenantID: ss.Scope.TenantID, IdemKey: "brief:brief:s1:2026", Purpose: "brief", Status: "succeeded", Context: `{"workspace_id":"` + ss.Scope.WorkspaceID + `"}`}); err != nil {
		t.Fatal(err)
	}
	if fe.calls["POST /v1/briefs/wake"] != 1 {
		t.Fatalf("engine not woken: %v", fe.calls)
	}
}
