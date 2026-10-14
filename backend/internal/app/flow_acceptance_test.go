package app

import (
	"context"
	"testing"

	"yingce/backend/internal/model"
)

// Direct acceptance tests A08 and A10 (PRD §15) on the real Pi run chain.

func TestA10ApprovalRejectAndExpiryNeverExecute(t *testing.T) {
	// Reject: the monitor is never written.
	s, db, fe, conv := flowTestSetup(t)
	run, err := s.CreateCloudAgentRun("user", flowRunRequest(conv, "flow-a10-reject"), "")
	if err != nil {
		t.Fatal(err)
	}
	flowModelStep(t, s, db, run.ID, []map[string]any{{"id": "call_r", "type": "function", "function": map[string]any{"name": "intel_apply_monitor", "arguments": `{"preview_id":"mpv_1"}`}}})
	out, err := s.cloudAgentPiTool(context.Background(), "user", run.ID, piToolPayload("call_r", "intel_apply_monitor", `{"preview_id":"mpv_1"}`))
	if err != nil {
		t.Fatal(err)
	}
	approvalID, _ := out.(map[string]any)["approvalId"].(string)
	if err := s.DecideCloudAgentApproval("user", run.ID, approvalID, "reject", "not now"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if fe.calls["POST /v1/monitors"] != 0 {
		t.Fatalf("rejected approval executed: %v", fe.calls)
	}
	var ok model.FlowAgentToolCall
	if db.First(&ok, "call_id = ? AND status = ?", "call_r", "succeeded").Error == nil {
		t.Fatal("a rejected call must not have a succeeded receipt")
	}
	// A rejected approval cannot be flipped to approve afterwards.
	if err := s.DecideCloudAgentApproval("user", run.ID, approvalID, "approve", ""); err == nil || fe.calls["POST /v1/monitors"] != 0 {
		t.Fatalf("decision flip must be refused: %v %v", err, fe.calls)
	}

	// Expired: the preview expired before approval → the engine refuses (410), nothing is created.
	s2, db2, fe2, conv2 := flowTestSetup(t)
	fe2.monitorStatus = 410
	run2, err := s2.CreateCloudAgentRun("user", flowRunRequest(conv2, "flow-a10-expired"), "")
	if err != nil {
		t.Fatal(err)
	}
	flowModelStep(t, s2, db2, run2.ID, []map[string]any{{"id": "call_e", "type": "function", "function": map[string]any{"name": "intel_apply_monitor", "arguments": `{"preview_id":"mpv_1"}`}}})
	out, err = s2.cloudAgentPiTool(context.Background(), "user", run2.ID, piToolPayload("call_e", "intel_apply_monitor", `{"preview_id":"mpv_1"}`))
	if err != nil {
		t.Fatal(err)
	}
	approvalID, _ = out.(map[string]any)["approvalId"].(string)
	_ = s2.DecideCloudAgentApproval("user", run2.ID, approvalID, "approve", "")
	var rec model.FlowAgentToolCall
	if db2.First(&rec, "call_id = ?", "call_e").Error == nil && rec.Status == "succeeded" {
		t.Fatalf("expired preview must not yield a succeeded monitor: %+v", rec)
	}
	// Approved exactly once even when the decision is repeated.
	_ = s2.DecideCloudAgentApproval("user", run2.ID, approvalID, "approve", "")
	if fe2.calls["POST /v1/monitors"] != 1 {
		t.Fatalf("approved action executed more than once: %v", fe2.calls)
	}
}

func TestA08RunContinuesWithoutClientAndEventsReplay(t *testing.T) {
	s, db, fe, conv := flowTestSetup(t)
	run, err := s.CreateCloudAgentRun("user", flowRunRequest(conv, "flow-a08-key"), "")
	if err != nil {
		t.Fatal(err)
	}
	// No SSE/web client is attached at any point: the model step and the tool
	// call are driven by the background runtime only.
	flowModelStep(t, s, db, run.ID, []map[string]any{{"id": "call_q", "type": "function", "function": map[string]any{"name": "intel_search_documents", "arguments": `{"query":"lithium"}`}}})
	if _, err := s.cloudAgentPiTool(context.Background(), "user", run.ID, piToolPayload("call_q", "intel_search_documents", `{"query":"lithium"}`)); err != nil {
		t.Fatal(err)
	}
	if fe.calls["POST /v1/documents/search"] != 1 {
		t.Fatalf("background work did not run: %v", fe.calls)
	}
	// A client that connects later replays everything from the start…
	full, err := s.CloudAgentRun("user", run.ID, CloudAgentRunViewOptions{})
	if err != nil || len(full.Events) == 0 {
		t.Fatalf("replay: %v %d", err, len(full.Events))
	}
	for i := 1; i < len(full.Events); i++ {
		if full.Events[i].Seq != full.Events[i-1].Seq+1 {
			t.Fatalf("replayed events are not contiguous: %d then %d", full.Events[i-1].Seq, full.Events[i].Seq)
		}
	}
	last := full.Events[len(full.Events)-1].Seq
	// …and a client resuming from its last seen seq (Last-Event-ID) gets only newer events.
	flowModelStep(t, s, db, run.ID, []map[string]any{{"id": "call_q2", "type": "function", "function": map[string]any{"name": "intel_search_documents", "arguments": `{"query":"storage"}`}}})
	delta, err := s.CloudAgentRun("user", run.ID, CloudAgentRunViewOptions{SinceSeq: last})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range delta.Events {
		if e.Seq <= last {
			t.Fatalf("resume replayed an already-seen event %d (last %d)", e.Seq, last)
		}
	}
	// Results persisted in the conversation regardless of any client.
	var n int64
	db.Model(&model.FlowAgentMessage{}).Where("conversation_id = ? AND tool_name = ?", conv.ID, "intel_search_documents").Count(&n)
	if n != 1 {
		t.Fatalf("tool result not persisted for later viewing: %d", n)
	}
	// Another user cannot replay this run.
	if _, err := s.CloudAgentRun("intruder", run.ID, CloudAgentRunViewOptions{}); err == nil {
		t.Fatal("foreign user replayed the run")
	}
}
