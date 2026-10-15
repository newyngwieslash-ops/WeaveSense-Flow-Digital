package app

import (
	"context"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

// A15 / §9.4 cancel propagation: cancelling an intel run cancels its child
// research in the engine (queued and running) before cleanup completes.
func TestCancelIntelRunPropagatesToChildResearch(t *testing.T) {
	s, db, fe, conv := flowTestSetup(t)
	if err := db.AutoMigrate(&model.FlowUsageRecord{}); err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateCloudAgentRun("user", flowRunRequest(conv, "cancel-children-1"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CancelCloudAgent(context.Background(), "user", run.ID); err != nil {
		t.Fatal(err)
	}
	fe.mu.Lock()
	n := fe.calls["POST /v1/research-runs/cancel-children"]
	body := fe.bodies["/v1/research-runs/cancel-children"]
	fe.mu.Unlock()
	if n == 0 {
		// Cleanup may be finished by the scheduler path; drive it explicitly.
		exec, err := s.repo.CloudAgent("user", run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.finishCloudAgentCleanup(context.Background(), exec); err != nil {
			t.Fatal(err)
		}
		fe.mu.Lock()
		n = fe.calls["POST /v1/research-runs/cancel-children"]
		body = fe.bodies["/v1/research-runs/cancel-children"]
		fe.mu.Unlock()
	}
	if n == 0 || !strings.Contains(body, run.ID) {
		t.Fatalf("parent cancel must cancel child research in the engine: calls=%d body=%s", n, body)
	}
}
