package app

import (
	"encoding/json"
	"testing"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// PRD §8.1/§9.1: a run is admitted with one frozen capability snapshot (tools,
// model route, price book, skills), and the canvas path meters its settled
// billing orders into the flow usage ledger through the chain's UsageRecorder,
// once per order and priced with the run's frozen price version.
func TestRunCapabilitySnapshotAndCanvasUsageLedger(t *testing.T) {
	s, db, _, conv := flowTestSetup(t)
	if err := db.AutoMigrate(&model.FlowUsageRecord{}, &model.FlowCapabilitySnapshot{}, &model.FlowRunCapability{}, &model.FlowSkillInstall{}, &model.FlowSkillRunPin{}); err != nil {
		t.Fatal(err)
	}
	run, err := s.CreateCloudAgentRun("user", flowRunRequest(conv, "cap-run-1"), "")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := flow.RunCapability(db, run.ID)
	if err != nil {
		t.Fatalf("run must be bound to a capability snapshot: %v", err)
	}
	var c struct {
		ToolNames  []string       `json:"tool_names"`
		ModelRoute map[string]any `json:"model_route"`
		PriceBook  flow.PriceBook `json:"price_book"`
	}
	if err := json.Unmarshal([]byte(snap.Content), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.ToolNames) == 0 || c.ModelRoute["channel_model_key"] != "text-test" || c.PriceBook.Version == "" || snap.ToolSchemaHash == "" {
		t.Fatalf("snapshot content incomplete: %+v", c)
	}
	if err := flow.CheckRunTool(db, run.ID, "intel_search_documents"); err != nil {
		t.Fatalf("frozen tool must pass: %v (tools %v)", err, c.ToolNames)
	}
	if err := flow.CheckRunTool(db, run.ID, "intel_not_frozen_tool"); err == nil {
		t.Fatal("a tool outside the frozen table must be rejected")
	}
	again, _ := flow.FreezeCapabilitySnapshot(db, flow.CapabilityInput{TenantID: snap.TenantID, RunID: run.ID, Tools: nil}, snap.CreatedAt)
	if again.ID != snap.ID {
		t.Fatal("a run's snapshot binding is set once")
	}

	if err := db.Create(&model.BillingOrder{ID: "bo_media", UserID: "user", Status: model.BillingStatusSettled, AmountMicrocredits: 5000, ActualAmountMicrocredits: 4000}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Task{ID: "task_media", UserID: "user", AgentRunID: run.ID, Type: "image", BillingOrderID: "bo_media"}).Error; err != nil {
		t.Fatal(err)
	}
	exec, err := s.repo.CloudAgent("user", run.ID)
	if err != nil {
		t.Fatal(err)
	}
	s.recordCanvasRunUsage(exec)
	s.recordCanvasRunUsage(exec)
	var lines []model.FlowUsageRecord
	db.Where("idem_key LIKE ?", "canvas:order:%").Find(&lines)
	if len(lines) != 1 {
		t.Fatalf("one usage line per settled order, got %d", len(lines))
	}
	l := lines[0]
	if l.UsageType != flow.UsageCanvasMedia || l.CostMicro != 4000 || l.Status != "recorded" || l.PriceVersion != snap.PriceVersion {
		t.Fatalf("usage line: %+v", l)
	}
}
