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

func TestCanvasTwoWayEditDeleteTombstones(t *testing.T) {
	db := testDB(t)
	initial := `{"version":1,"conversations":[{"id":"c1","title":"Plan","messages":[{"id":"m1","role":"user","text":"hi"},{"id":"m2","role":"assistant","text":"hello"}]}]}`
	db.Create(&model.FlowCanvasConversationDoc{UserID: "u_alice", CanvasID: "cv1", Document: initial, Version: 1, UpdatedAt: time.Now()})
	load := func() []CanvasConv {
		var row model.FlowCanvasConversationDoc
		db.First(&row, "user_id = ? AND canvas_id = ?", "u_alice", "cv1")
		var p struct{ Conversations []CanvasConv }
		_ = json.Unmarshal([]byte(row.Document), &p)
		return p.Conversations
	}
	if err := MirrorCanvasConversations(db, "u_alice", "cv1", load()); err != nil {
		t.Fatal(err)
	}
	convID := CanvasConversationIndexID("u_alice", "cv1", "c1")
	cs := Conversations{DB: db}
	conv, _, _ := cs.Get("u_alice", convID)
	texts := func() string {
		msgs, _, _ := cs.Messages(convID, 0, 50)
		var out []string
		for i := len(msgs) - 1; i >= 0; i-- {
			out = append(out, msgs[i].Content)
		}
		return strings.Join(out, "|")
	}
	intel, _ := cs.Append(conv, model.FlowAgentMessage{Role: "user", Content: "from intel"})
	var m1 model.FlowAgentMessage
	db.First(&m1, "conversation_id = ? AND external_id = ?", convID, "m1")

	// Intelligence → canvas: edit + delete, with revision conflict detection.
	if _, err := cs.EditMessage(conv, m1.ID, "hi (edited)", 5); err != ErrRevisionConflict {
		t.Fatalf("stale edit accepted: %v", err)
	}
	e, err := cs.EditMessage(conv, m1.ID, "hi (edited)", 0)
	if err != nil || e.Revision != 1 {
		t.Fatalf("edit: %v rev %d", err, e.Revision)
	}
	if again, err := cs.EditMessage(conv, m1.ID, "hi (edited)", 0); err != nil || again.Revision != 1 {
		t.Fatalf("edit not idempotent: %v", err)
	}
	if _, err := cs.DeleteMessage(conv, intel.ID, -1); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.DeleteMessage(conv, intel.ID, -1); err != nil {
		t.Fatalf("delete not idempotent: %v", err)
	}
	cv := load()[0]
	raw, _ := json.Marshal(cv)
	if !strings.Contains(string(raw), "hi (edited)") || strings.Contains(string(raw), "from intel") || len(cv.DeletedIDs) != 1 || cv.DeletedIDs[0] != "dir_"+intel.ID {
		t.Fatalf("write-back of edit/delete: %s", raw)
	}
	// Re-mirroring the canvas document does not resurrect or double-apply anything.
	if err := MirrorCanvasConversations(db, "u_alice", "cv1", load()); err != nil {
		t.Fatal(err)
	}
	if got := texts(); got != "hi (edited)|hello" {
		t.Fatalf("after echo: %s", got)
	}
	// A stale replica that still holds the deleted message cannot resurrect it.
	stale := load()
	stale[0].Messages = append(stale[0].Messages, CanvasConvMessage{ID: "dir_" + intel.ID, Role: "user", Text: "from intel"})
	_ = MirrorCanvasConversations(db, "u_alice", "cv1", stale)
	if got := texts(); got != "hi (edited)|hello" {
		t.Fatalf("stale replica resurrected: %s", got)
	}

	// Canvas → intelligence: edit m2, delete m1 (explicit tombstone).
	next := load()
	for i := range next[0].Messages {
		if next[0].Messages[i].ID == "m2" {
			next[0].Messages[i].Text = "hello (canvas edit)"
		}
	}
	next[0].Messages = next[0].Messages[1:]
	next[0].DeletedIDs = append(next[0].DeletedIDs, "m1")
	for i := 0; i < 2; i++ { // idempotent
		if err := MirrorCanvasConversations(db, "u_alice", "cv1", next); err != nil {
			t.Fatal(err)
		}
	}
	if got := texts(); got != "hello (canvas edit)" {
		t.Fatalf("canvas edit/delete not mirrored: %s", got)
	}
	var m2 model.FlowAgentMessage
	db.First(&m2, "conversation_id = ? AND external_id = ?", convID, "m2")
	if m2.Revision != 1 || m2.SyncedRevision != 1 || m2.EditedAt == nil {
		t.Fatalf("m2 revision %d/%d", m2.Revision, m2.SyncedRevision)
	}
	// Tool messages cannot be edited.
	tool, _ := cs.Append(conv, model.FlowAgentMessage{Role: "tool", Content: "{}"})
	if _, err := cs.EditMessage(conv, tool.ID, "x", -1); err != ErrInvalid {
		t.Fatalf("tool edit: %v", err)
	}
}
