package flow

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

func TestP7MembersRoleEditing(t *testing.T) {
	db := testDB(t)
	tn, ws, err := CreateOrganization(db, "u_alice", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := ResolveScope(db, "u_alice", ws.ID)
	_, tok, _ := CreateInvite(db, admin, "u_alice", "bob@x", "editor", time.Hour)
	if _, err := AcceptInvite(db, "u_bob", tok); err != nil {
		t.Fatal(err)
	}
	ms, err := ListMembers(db, admin)
	if err != nil || len(ms) != 2 {
		t.Fatalf("members %v %v", ms, err)
	}
	if err := SetMemberRole(db, admin, "u_bob", "viewer"); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeGrant(db, "user:u_bob", tn.ID, ws.ID, "intel.reports.write"); err == nil {
		t.Fatal("viewer must lose write scope")
	}
	if err := SetMemberRole(db, admin, "u_bob", "admin"); err != nil {
		t.Fatal(err)
	}
	bob, _ := ResolveScope(db, "u_bob", ws.ID)
	if bob.Role != "admin" {
		t.Fatalf("bob role %s", bob.Role)
	}
	if err := SetMemberRole(db, bob, "u_alice", "member"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("owner role must be immutable: %v", err)
	}
	if err := SetMemberRole(db, admin, "u_bob", "root"); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid role accepted")
	}
	_ = SetMemberRole(db, admin, "u_bob", "member")
	bob, _ = ResolveScope(db, "u_bob", ws.ID)
	if err := SetMemberRole(db, bob, "u_alice", "admin"); !errors.Is(err, ErrForbidden) {
		t.Fatal("non-admin edited roles")
	}
}

func TestP7NotificationReadStateIsPerUser(t *testing.T) {
	db := testDB(t)
	_, ws, _ := CreateOrganization(db, "u_alice", "Acme")
	admin, _ := ResolveScope(db, "u_alice", ws.ID)
	_, tok, _ := CreateInvite(db, admin, "u_alice", "bob@x", "editor", time.Hour)
	_, _ = AcceptInvite(db, "u_bob", tok)
	bob, _ := ResolveScope(db, "u_bob", ws.ID)
	for _, id := range []string{"n1", "n2"} {
		db.Create(&model.FlowNotification{ID: id, TenantID: admin.TenantID, WorkspaceID: ws.ID, EventID: "ev_" + id, Type: "x", CreatedAt: time.Now()})
	}
	if n, err := MarkNotificationsRead(db, admin, "u_alice", "n1"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n, _ := MarkNotificationsRead(db, admin, "u_alice", "n1"); n != 0 {
		t.Fatal("double read counted")
	}
	a, _ := ListNotificationsForUser(db, admin, "u_alice")
	b, _ := ListNotificationsForUser(db, bob, "u_bob")
	unread := func(vs []NotificationView) int {
		n := 0
		for _, v := range vs {
			if v.ReadAt == nil {
				n++
			}
		}
		return n
	}
	if unread(a) != 1 || unread(b) != 2 {
		t.Fatalf("alice unread %d bob unread %d", unread(a), unread(b))
	}
	_, _ = MarkNotificationsRead(db, bob, "u_bob", "all")
	b, _ = ListNotificationsForUser(db, bob, "u_bob")
	a, _ = ListNotificationsForUser(db, admin, "u_alice")
	if unread(b) != 0 || unread(a) != 1 {
		t.Fatal("read-all leaked across users")
	}
}

func TestP7TopicToolSet(t *testing.T) {
	for _, n := range []string{"intel_apply_monitor", "intel_collect_once", "intel_apply_schedule", "mcp_x_y"} {
		if ToolAllowedForSubject(SubjectTopic, n) {
			t.Fatalf("%s allowed for topic", n)
		}
		if !ToolAllowedForSubject(SubjectWorkspace, n) {
			t.Fatalf("%s denied for workspace", n)
		}
	}
	for _, n := range []string{"intel_search_documents", "intel_start_research", "intel_save_report"} {
		if !ToolAllowedForSubject(SubjectTopic, n) {
			t.Fatalf("%s denied for topic", n)
		}
	}
	db := testDB(t)
	ss, _ := ResolveSubject(db, "u_alice", AgentSubject{Kind: SubjectWorkspace})
	topic := model.FlowTopic{ID: "tp1", TenantID: ss.Scope.TenantID, WorkspaceID: ss.Scope.WorkspaceID, Title: "t"}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatal(err)
	}
	ts, err := ResolveSubject(db, "u_alice", AgentSubject{Kind: SubjectTopic, ID: "tp1"})
	if err != nil {
		t.Fatal(err)
	}
	conv, _ := Conversations{DB: db}.Create("u_alice", ts, "t")
	_, err = Tools{DB: db}.Invoke(context.Background(), conv, ts, "u_alice", "c1", "intel_apply_monitor", map[string]any{}, "")
	if !errors.Is(err, ErrToolNotInSubjectSet) {
		t.Fatalf("want subject-set denial, got %v", err)
	}
}

func TestP7ModelStatusReconcileAndOperatorResolve(t *testing.T) {
	db := testDB(t)
	b := Billing{DB: db}
	tenant := model.PersonalTenantID("u_alice")
	fund(t, b, "u_alice", 5_000_000)
	var gotKey atomic.Value
	gwSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey.Store(r.Header.Get("Idempotency-Key"))
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer gwSrv.Close()
	ev := []ResolvedEvidence{{EvidenceID: "ev1", DocumentID: "d1", SourceID: "s1", Revision: 1}}
	for _, k := range []string{"ka", "kb", "kc"} {
		_, err := Synthesize(context.Background(), &OpenAICompatGateway{URL: gwSrv.URL, MicroPer1KTok: 1000}, b, SynthesisRequest{TenantID: tenant, Key: k, Purpose: "brief", Question: "q", Evidence: ev, Scope: MaterialScope{SourceIDs: []string{"s1"}}})
		if !errors.Is(err, ErrModelUnknownOutcome) {
			t.Fatal(err)
		}
	}
	if gotKey.Load() != "kc" {
		t.Fatalf("idempotency header %v", gotKey.Load())
	}
	stSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("idempotency_key") {
		case "ka":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "succeeded", "total_tokens": 100})
		case "kb":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "not_found"})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
		}
	}))
	defer stSrv.Close()
	ms := ModelStatus{URL: stSrv.URL, MicroPer1KTok: 1000}
	n, err := b.Reconcile(0, func(r model.FlowBudgetReservation) (Outcome, error) { return ms.Query(context.Background(), r) })
	if err != nil || n != 2 {
		t.Fatalf("reconciled %d %v", n, err)
	}
	get := func(k string) model.FlowBudgetReservation {
		var r model.FlowBudgetReservation
		db.First(&r, "idempotency_key = ?", k)
		return r
	}
	if r := get("ka"); r.Status != "settled" || r.ConsumedMicro != 100 {
		t.Fatalf("ka %+v", r)
	}
	if r := get("kb"); r.Status != "released" {
		t.Fatalf("kb %+v", r)
	}
	kc := get("kc")
	if kc.Status != StatusReconciliation {
		t.Fatal("pending must stay unknown")
	}
	scope, _ := ResolveScope(db, "u_alice", "")
	if _, err := ResolveReservation(db, Scope{TenantID: scope.TenantID, Role: "viewer"}, kc.ID, "not_executed", 0); !errors.Is(err, ErrForbidden) {
		t.Fatal("non-admin resolved")
	}
	if r, err := ResolveReservation(db, scope, kc.ID, "not_executed", 0); err != nil || r.Status != "released" {
		t.Fatalf("resolve %+v %v", r, err)
	}
	if _, err := ResolveReservation(db, scope, kc.ID, "succeeded", 0); !errors.Is(err, ErrBudgetState) {
		t.Fatal("double resolve allowed")
	}
}

func TestP7MCPOperatorResolve(t *testing.T) {
	db := testDB(t)
	scope, _ := ResolveScope(db, "u_alice", "")
	gw := MCPGateway{DB: db}
	row := model.FlowMCPCall{ID: "mc1", TenantID: scope.TenantID, IdemKey: "k", ConnectionID: "none", Tool: "t", Status: StatusReconciliation}
	db.Create(&row)
	if _, err := gw.ResolveMCPCall(Scope{TenantID: scope.TenantID, Role: "editor"}, "mc1", "succeeded", "x"); !errors.Is(err, ErrForbidden) {
		t.Fatal("editor resolved")
	}
	calls, _ := ListMCPCalls(db, scope, StatusReconciliation)
	if len(calls) != 1 {
		t.Fatal("unknown call not listed")
	}
	r, err := gw.ResolveMCPCall(scope, "mc1", "succeeded", "booked")
	if err != nil || r.Status != "succeeded" || !r.Billed || r.Output != "booked" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := gw.ResolveMCPCall(scope, "mc1", "not_executed", ""); !errors.Is(err, ErrBudgetState) {
		t.Fatal("resolved twice")
	}
	other := Scope{TenantID: "t_other", Role: "admin"}
	db.Create(&model.FlowMCPCall{ID: "mc2", TenantID: scope.TenantID, IdemKey: "k2", ConnectionID: "none", Tool: "t", Status: StatusReconciliation})
	if _, err := gw.ResolveMCPCall(other, "mc2", "not_executed", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-tenant resolve")
	}
}

func TestP7MCPOAuthClientCredentialsCached(t *testing.T) {
	db := testDB(t)
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		id, sec, ok := r.BasicAuth()
		_ = r.ParseForm()
		if !ok || id != "cid" || sec != "csec" || r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("scope") != "tools" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at-1", "token_type": "Bearer", "expires_in": 3600})
	}))
	defer srv.Close()
	t.Setenv("FLOW_MCP_OAUTH_DEMO_TOKEN_URL", srv.URL)
	t.Setenv("FLOW_MCP_OAUTH_DEMO_CLIENT_ID", "cid")
	t.Setenv("FLOW_MCP_OAUTH_DEMO_CLIENT_SECRET", "csec")
	t.Setenv("FLOW_MCP_OAUTH_DEMO_SCOPE", "tools")
	gw := MCPGateway{DB: db}
	for i := 0; i < 3; i++ {
		tok, err := gw.credential("oauth:FLOW_MCP_OAUTH_DEMO")
		if err != nil || tok != "at-1" {
			t.Fatal(tok, err)
		}
	}
	if hits != 1 {
		t.Fatalf("token endpoint hit %d times", hits)
	}
	if _, err := gw.credential("oauth:OTHER"); err == nil {
		t.Fatal("bad prefix accepted")
	}
	t.Setenv("FLOW_MCP_OAUTH_DEMO_CLIENT_SECRET", "wrong")
	if _, err := (OAuthClientCredentials{}).Token(context.Background(), "oauth:FLOW_MCP_OAUTH_DEMO"); err == nil {
		t.Fatal("bad secret accepted")
	}
}

func TestP7CanvasConversationsMirroredIntoDirectory(t *testing.T) {
	db := testDB(t)
	convs := []CanvasConv{{ID: "c1", Title: "Plan", Messages: []CanvasConvMessage{{Role: "user", Text: "hi"}, {Role: "tool", Text: "x"}, {Role: "assistant", Text: "hello"}}}}
	if err := MirrorCanvasConversations(db, "u_alice", "cv1", convs); err != nil {
		t.Fatal(err)
	}
	convs[0].Messages = append(convs[0].Messages, CanvasConvMessage{Role: "user", Text: "more"})
	if err := MirrorCanvasConversations(db, "u_alice", "cv1", convs); err != nil {
		t.Fatal(err)
	}
	list, _ := Conversations{DB: db}.List("u_alice", 10, time.Time{})
	if len(list) != 1 || list[0].SubjectKind != SubjectCanvas || list[0].SubjectID != "cv1" || list[0].Title != "Plan" || list[0].LastSeq != 3 {
		t.Fatalf("directory %+v", list)
	}
	if l, _ := (Conversations{DB: db}).List("u_bob", 10, time.Time{}); len(l) != 0 {
		t.Fatal("leaked to other user")
	}
}
