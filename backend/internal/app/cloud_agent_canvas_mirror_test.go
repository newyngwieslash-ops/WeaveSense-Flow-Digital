package app

import (
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/flow"
	"yingce/backend/internal/model"
)

// A canvas conversation continued from the intelligence page is a full canvas
// run: canvas tools on the server-side document, plus directory recording.
func TestCanvasConversationContinuedFromIntelligenceUsesCanvasTools(t *testing.T) {
	s, db, _, _ := flowTestSetup(t)
	if err := db.AutoMigrate(&model.FlowCanvasConversationDoc{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.CanvasProject{ID: "cv_m", UserID: "user", PayloadJSON: `{"nodes":[]}`}).Error; err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"activeId":"c1","conversations":[{"id":"c1","title":"Board","messages":[{"id":"m1","role":"user","text":"make a poster"}]}]}`
	db.Create(&model.FlowCanvasConversationDoc{UserID: "user", CanvasID: "cv_m", Document: doc, Version: 1, UpdatedAt: time.Now()})
	if err := flow.MirrorCanvasConversations(db, "user", "cv_m", []flow.CanvasConv{{ID: "c1", Title: "Board", Messages: []flow.CanvasConvMessage{{ID: "m1", Role: "user", Text: "make a poster"}}}}); err != nil {
		t.Fatal(err)
	}
	convID := flow.CanvasConversationIndexID("user", "cv_m", "c1")
	req := CloudAgentRequest{Prompt: "Add a title node", Model: "text-test", ChannelID: "channel", ChannelModelKey: "text-test", PermissionMode: "request_approval",
		IdempotencyKey: "canvas-mirror-1", FlowConversationID: convID, CanvasID: "cv_m", Subject: &flow.AgentSubject{Kind: flow.SubjectCanvas, ID: "cv_m"}}
	req.Budget.MaxCredits = 1
	run, err := s.CreateCloudAgentRun("user", req, "")
	if err != nil {
		t.Fatal(err)
	}
	_, state := agentInterjectionState(t, s, run.ID)
	names := map[string]bool{}
	for _, tool := range state.Canonical.Tools {
		names[stringValue(tool["function"].(map[string]any)["name"])] = true
	}
	// Canvas tool set (canvas tools are loaded through the canvas read-tool
	// chain), never the intelligence-only tool set.
	if !names["plan_update"] {
		t.Fatalf("canvas tool set missing: %v", names)
	}
	for _, tool := range cloudAgentIntelTools(flowRunRequest(model.FlowAgentConversation{}, "x")) {
		if n := stringValue(tool["function"].(map[string]any)["name"]); n != "plan_update" && n != "ask_user" && n != "task_get" && names[n] {
			t.Fatalf("intel-only tool %s exposed on a canvas run", n)
		}
	}
	ctx := false
	for _, m := range state.Canonical.Messages {
		if m[cloudAgentContextSourceKey] == "flow_context" {
			ctx = true
		}
	}
	if !ctx {
		t.Fatal("directory context missing")
	}
	var d model.FlowCanvasConversationDoc
	db.First(&d, "canvas_id = ?", "cv_m")
	if !strings.Contains(d.Document, "Add a title node") {
		t.Fatalf("user turn not written back to the sidebar: %s", d.Document)
	}
	// Someone else's canvas / conversation is rejected.
	db.Create(&model.CanvasProject{ID: "cv_other", UserID: "other", PayloadJSON: `{"nodes":[]}`})
	bad := req
	bad.IdempotencyKey, bad.CanvasID, bad.Subject = "canvas-mirror-2", "cv_other", &flow.AgentSubject{Kind: flow.SubjectCanvas, ID: "cv_other"}
	if _, err := s.CreateCloudAgentRun("user", bad, ""); err == nil {
		t.Fatal("run on a foreign canvas/conversation pair accepted")
	}
}
