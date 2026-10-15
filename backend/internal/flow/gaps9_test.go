package flow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

// §5.2 saved topics: create/update/list, optimistic version, cross-tenant denial, tool.
func TestSavedTopicsCRUDAndTool(t *testing.T) {
	db := testDB(t)
	a, _ := ResolveScope(db, "u_alice", "")
	b, _ := ResolveScope(db, "u_bob", "")
	title, q := "锂电池价格", "碳酸锂价格走势"
	kw := []string{"碳酸锂", " 碳酸锂 ", "锂价"}
	v, err := CreateTopic(db, a, "u_alice", TopicInput{Title: &title, Question: &q, Keywords: &kw}, time.Now())
	if err != nil || v.Version != 1 || len(v.Keywords) != 2 {
		t.Fatalf("create: %v %+v", err, v)
	}
	nt := "锂电池价格周报"
	v2, err := UpdateTopic(db, a, v.ID, 1, TopicInput{Title: &nt}, time.Now())
	if err != nil || v2.Version != 2 || v2.Title != nt || v2.Question != q {
		t.Fatalf("update: %v %+v", err, v2)
	}
	if _, err := UpdateTopic(db, a, v.ID, 1, TopicInput{Title: &nt}, time.Now()); !errors.Is(err, ErrTopicConflict) {
		t.Fatalf("stale version must conflict: %v", err)
	}
	if _, err := GetTopic(db, b, v.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant get must be denied: %v", err)
	}
	if _, err := UpdateTopic(db, b, v.ID, 0, TopicInput{Title: &nt}, time.Now()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-tenant update must be denied: %v", err)
	}
	if l, _ := ListTopics(db, b); len(l) != 0 {
		t.Fatalf("bob sees %d topics", len(l))
	}
	ro := a
	ro.Role = "viewer"
	if _, err := CreateTopic(db, ro, "u_alice", TopicInput{Title: &title}, time.Now()); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer must not create topics: %v", err)
	}
	// SubjectTopic resolves the saved topic.
	if ss, err := ResolveSubject(db, "u_alice", AgentSubject{Kind: SubjectTopic, ID: v.ID}); err != nil || ss.TopicID != v.ID {
		t.Fatalf("resolve topic subject: %v", err)
	}
	spec, ok := FindTool("intel_save_topic")
	if !ok || spec.local == nil || spec.Class != "write" {
		t.Fatal("intel_save_topic missing")
	}
	out, err := spec.local(Tools{DB: db}, context.Background(), model.FlowAgentConversation{}, SubjectScope{Scope: a}, "u_alice",
		map[string]any{"title": "储能", "keywords": []any{"储能"}, "source_ids": []any{"src_1"}})
	if err != nil || out.(TopicView).SourceIDs[0] != "src_1" {
		t.Fatalf("tool create: %v %+v", err, out)
	}
	if l, _ := ListTopics(db, a); len(l) != 2 {
		t.Fatalf("alice topics %d", len(l))
	}
}

// §11.1 projection follows cancel / retry events.
func TestProjectionCancelAndRetryEvents(t *testing.T) {
	db := testDB(t)
	sc, _ := ResolveScope(db, "u_alice", "")
	ev := func(id, typ string, seq int64) Event {
		return Event{EventID: id, OperationID: "op_1", JobID: "j1", TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, Sequence: seq, Type: typ,
			OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: json.RawMessage(`{}`)}
	}
	for i, step := range []struct{ typ, want string }{
		{"collection.accepted", "running"}, {"operation.retrying", "retry_wait"},
		{"operation.cancel_requested", "cancel_requested"}, {"operation.cancelled", "cancelled"},
	} {
		if _, err := ConsumeEvent(db, "platform", ev("e"+step.typ, step.typ, int64(i+1))); err != nil {
			t.Fatal(err)
		}
		var p model.FlowOperationProjection
		db.First(&p, "operation_id = ?", "op_1")
		if p.Status != step.want {
			t.Fatalf("after %s: %s", step.typ, p.Status)
		}
	}
}

// §8.3 every tool definition declares its cost basis.
func TestEveryToolHasCostBasis(t *testing.T) {
	for _, s := range Catalog {
		if s.CostBasis == "" {
			t.Errorf("%s has no cost basis", s.Name)
		}
	}
}
