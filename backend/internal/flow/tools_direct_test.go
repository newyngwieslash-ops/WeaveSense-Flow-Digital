package flow

import (
	"context"
	"errors"
	"testing"
)

// Direct tests for the three tools the PRD §8.3 audit found untested.

func TestToolIntelPreviewMonitorDirect(t *testing.T) {
	tools, _, fe, conv, ss := setupAgent(t)
	ctx := context.Background()
	args := map[string]any{"keywords": []any{"锂电", "储能"}, "source_ids": []any{"src_1"}, "interval_seconds": 3600.0, "timezone": "Asia/Shanghai", "misfire_policy": "skip"}
	r, err := tools.Invoke(ctx, conv, ss, "u_alice", "pm1", "intel_preview_monitor", args, "")
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("preview: %v %+v", err, r)
	}
	body := fe.bodies["POST /v1/monitors/preview"]
	if body["interval_seconds"] != float64(3600) || body["timezone"] != "Asia/Shanghai" || body["misfire_policy"] != "skip" {
		t.Fatalf("args not forwarded: %v", body)
	}
	// A read tool: a viewer may preview, it never needs approval and creates nothing.
	viewer := ss
	viewer.Scope.Role = "viewer"
	if r, err := tools.Invoke(ctx, conv, viewer, "u_alice", "pm2", "intel_preview_monitor", args, ""); err != nil || r.Status != "succeeded" {
		t.Fatalf("viewer preview: %v %+v", err, r)
	}
	if fe.calls["POST /v1/monitors"] != 0 {
		t.Fatal("preview must not create a monitor")
	}
	for _, bad := range []map[string]any{
		{"source_ids": []any{"src_1"}, "interval_seconds": 3600.0},                       // keywords missing
		{"keywords": []any{"x"}, "source_ids": []any{"src_1"}, "interval_seconds": "1h"}, // wrong type
		{"keywords": []any{"x"}, "source_ids": []any{"src_1"}, "interval_seconds": 3600.0, "misfire_policy": "all"},
	} {
		if _, err := tools.Invoke(ctx, conv, ss, "u_alice", "pm_bad", "intel_preview_monitor", bad, ""); !errors.Is(err, ErrToolArgs) {
			t.Fatalf("bad args %v: %v", bad, err)
		}
	}
}

func TestToolIntelCancelOperationDirect(t *testing.T) {
	tools, _, fe, conv, ss := setupAgent(t)
	ctx := context.Background()
	r, err := tools.Invoke(ctx, conv, ss, "u_alice", "c1", "intel_cancel_operation", map[string]any{"operation_id": "op_1", "reason": "user asked"}, "")
	if err != nil || r.Status != "succeeded" || fe.calls["POST /v1/operations/op_1/cancel"] != 1 {
		t.Fatalf("cancel: %v %+v %v", err, r, fe.calls)
	}
	// Same call id replays the stored receipt; the engine is not asked twice.
	again, err := tools.Invoke(ctx, conv, ss, "u_alice", "c1", "intel_cancel_operation", map[string]any{"operation_id": "op_1", "reason": "user asked"}, "")
	if err != nil || !again.Replayed || fe.calls["POST /v1/operations/op_1/cancel"] != 1 {
		t.Fatalf("replay: %v %+v", err, again)
	}
	// The operation id is path-escaped: no traversal into other engine routes.
	_, _ = tools.Invoke(ctx, conv, ss, "u_alice", "c2", "intel_cancel_operation", map[string]any{"operation_id": "../sources"}, "")
	if fe.calls["POST /v1/sources"] != 0 {
		t.Fatal("operation id escaped its path segment")
	}
	// Cancelling is a write (intel.collect): viewers may not.
	viewer := ss
	viewer.Scope.Role = "viewer"
	if _, err := tools.Invoke(ctx, conv, viewer, "u_alice", "c3", "intel_cancel_operation", map[string]any{"operation_id": "op_1"}, ""); !errors.Is(err, ErrToolPermission) {
		t.Fatalf("viewer cancel: %v", err)
	}
	if _, err := tools.Invoke(ctx, conv, ss, "u_alice", "c4", "intel_cancel_operation", map[string]any{}, ""); !errors.Is(err, ErrToolArgs) {
		t.Fatalf("missing operation_id: %v", err)
	}
}

func TestToolIntelPreviewScheduleDirect(t *testing.T) {
	tools, _, fe, conv, ss := setupAgent(t)
	ctx := context.Background()
	args := map[string]any{"query": "储能招标", "source_ids": []any{"src_1"}, "cron": "0 9 * * 1", "timezone": "Asia/Shanghai", "window_hours": 168.0}
	r, err := tools.Invoke(ctx, conv, ss, "u_alice", "s1", "intel_preview_schedule", args, "")
	if err != nil || r.Status != "succeeded" {
		t.Fatalf("preview schedule: %v %+v", err, r)
	}
	body := fe.bodies["POST /v1/brief-schedules/preview"]
	if body["cron"] != "0 9 * * 1" || body["timezone"] != "Asia/Shanghai" || body["skill"] == nil {
		t.Fatalf("body: %v", body)
	}
	if _, ok := body["skill_id"]; ok {
		t.Fatal("skill_id must be resolved server-side into a frozen skill ref, not forwarded")
	}
	if fe.calls["POST /v1/brief-schedules"] != 0 {
		t.Fatal("preview must not save a schedule")
	}
	// Unknown skill → the call fails before reaching the engine.
	before := fe.calls["POST /v1/brief-schedules/preview"]
	bad := map[string]any{"query": "x", "source_ids": []any{"src_1"}, "cron": "0 9 * * *", "timezone": "UTC", "skill_id": "nope"}
	if r, err := tools.Invoke(ctx, conv, ss, "u_alice", "s2", "intel_preview_schedule", bad, ""); err == nil && r.Status == "succeeded" {
		t.Fatalf("unknown skill must fail: %+v", r)
	}
	if fe.calls["POST /v1/brief-schedules/preview"] != before {
		t.Fatal("engine called despite unknown skill")
	}
	if _, err := tools.Invoke(ctx, conv, ss, "u_alice", "s3", "intel_preview_schedule", map[string]any{"query": "x"}, ""); !errors.Is(err, ErrToolArgs) {
		t.Fatalf("missing cron/timezone: %v", err)
	}
}
