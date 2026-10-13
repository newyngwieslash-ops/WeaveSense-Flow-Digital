package flow

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

func TestCanvasTwoWaySyncWithConflicts(t *testing.T) {
	db := testDB(t)
	doc := func(msgs string) string {
		return `{"version":1,"activeId":"c1","conversations":[{"id":"c1","title":"Plan","updatedAt":"2026-10-13T00:00:00Z","messages":[` + msgs + `],"permissionMode":"read_only"}]}`
	}
	initial := doc(`{"id":"m1","role":"user","text":"hi"},{"id":"m2","role":"assistant","text":"hello"}`)
	if err := db.Create(&model.FlowCanvasConversationDoc{UserID: "u_alice", CanvasID: "cv1", Document: initial, Version: 1, UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	var parsed struct{ Conversations []CanvasConv }
	_ = json.Unmarshal([]byte(initial), &parsed)
	if err := MirrorCanvasConversations(db, "u_alice", "cv1", parsed.Conversations); err != nil {
		t.Fatal(err)
	}
	convID := CanvasConversationIndexID("u_alice", "cv1", "c1")
	cs := Conversations{DB: db}
	conv, ss, err := cs.Get("u_alice", convID)
	if err != nil || ss.Subject.Kind != SubjectCanvas {
		t.Fatalf("canvas conversation not resolvable from intelligence: %v", err)
	}
	// Continue from the intelligence page: user + assistant turn.
	if _, err := cs.Append(conv, model.FlowAgentMessage{Role: "user", Content: "from intel"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Append(conv, model.FlowAgentMessage{Role: "assistant", Content: "intel answer"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.Append(conv, model.FlowAgentMessage{Role: "tool", Content: "{}"}); err != nil {
		t.Fatal(err)
	}
	var row model.FlowCanvasConversationDoc
	db.First(&row, "user_id = ? AND canvas_id = ?", "u_alice", "cv1")
	if row.Version != 3 || !strings.Contains(row.Document, "from intel") || !strings.Contains(row.Document, "intel answer") || !strings.Contains(row.Document, `"permissionMode":"read_only"`) {
		t.Fatalf("write-back v%d %s", row.Version, row.Document)
	}
	if strings.Count(row.Document, `"role":"tool"`) != 0 {
		t.Fatal("tool message leaked into sidebar")
	}
	// The canvas client re-saves the merged document (contains dir_* ids) and adds its own turn:
	_ = json.Unmarshal([]byte(row.Document), &parsed)
	parsed.Conversations[0].Messages = append(parsed.Conversations[0].Messages, CanvasConvMessage{ID: "m3", Role: "user", Text: "back on canvas"})
	if err := MirrorCanvasConversations(db, "u_alice", "cv1", parsed.Conversations); err != nil {
		t.Fatal(err)
	}
	msgs, _, _ := cs.Messages(convID, 0, 50)
	var texts []string
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "tool" {
			texts = append(texts, msgs[i].Content)
		}
	}
	if strings.Join(texts, "|") != "hi|hello|from intel|intel answer|back on canvas" {
		t.Fatalf("directory after round trip: %v", texts)
	}
	// A concurrent canvas save wins the CAS first: write-back retries on the new version.
	db.Model(&model.FlowCanvasConversationDoc{}).Where("canvas_id = ?", "cv1").Updates(map[string]any{"version": 9})
	if _, err := cs.Append(conv, model.FlowAgentMessage{Role: "user", Content: "after conflict"}); err != nil {
		t.Fatal(err)
	}
	row = model.FlowCanvasConversationDoc{}
	db.First(&row, "user_id = ? AND canvas_id = ?", "u_alice", "cv1")
	if row.Version != 10 || strings.Count(row.Document, "from intel") != 1 || !strings.Contains(row.Document, "after conflict") {
		t.Fatalf("after conflict v%d", row.Version)
	}
	// Another user cannot reach it.
	if _, _, err := cs.Get("u_bob", convID); err == nil {
		t.Fatal("bob read alice's canvas conversation")
	}
}
