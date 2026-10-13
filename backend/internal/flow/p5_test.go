package flow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

type fakeGW struct {
	text string
	cost int64
	err  error
}

func (f fakeGW) Complete(context.Context, string, string, int) (string, int64, error) {
	return f.text, f.cost, f.err
}

func fund(t *testing.T, b Billing, user string, amt int64) {
	t.Helper()
	if err := b.DB.Save(&model.CreditAccount{UserID: user, AvailableMicrocredits: amt}).Error; err != nil {
		t.Fatal(err)
	}
}

func credit(b Billing, user string) model.CreditAccount {
	var a model.CreditAccount
	b.DB.First(&a, "user_id = ?", user)
	return a
}

func TestBudgetReserveAllocateSettleRelease(t *testing.T) {
	db := testDB(t)
	b := Billing{DB: db}
	tenant := model.PersonalTenantID("u_alice")
	fund(t, b, "u_alice", 1_000_000)
	root, err := b.Reserve(tenant, "run:1", "agent_run", "1", 600_000)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := b.Reserve(tenant, "run:1", "agent_run", "1", 600_000); again.ID != root.ID {
		t.Fatal("reserve must be idempotent")
	}
	if _, err := b.Reserve(tenant, "run:2", "agent_run", "2", 600_000); !errors.Is(err, ErrBudgetInsufficient) {
		t.Fatalf("over-reserve must fail, got %v", err)
	}
	c1, err := b.Allocate(root.ID, "c1", "research", "q", 400_000)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Allocate(root.ID, "c2", "research", "q", 300_000); !errors.Is(err, ErrBudgetParent) {
		t.Fatalf("child over-allocation must fail, got %v", err)
	}
	if _, err := b.Settle(c1.ID, 100_000); err != nil {
		t.Fatal(err)
	}
	c2, err := b.Allocate(root.ID, "c2", "research", "q", 300_000)
	if err != nil {
		t.Fatalf("freed budget must be allocatable: %v", err)
	}
	c3, _ := b.Allocate(root.ID, "c3", "research", "q", 100_000)
	if err := b.MarkUnknown(c3.ID, "x", "timeout"); err != nil {
		t.Fatal(err)
	}
	n, err := b.Reconcile(0, func(model.FlowBudgetReservation) (Outcome, error) { return Outcome{Known: true, Succeeded: false}, nil })
	if err != nil || n != 1 {
		t.Fatalf("reconcile %d %v", n, err)
	}
	if _, err := b.Settle(root.ID, 0); !errors.Is(err, ErrBudgetState) {
		t.Fatalf("root with open children must not settle: %v", err)
	}
	b.Release(c2.ID)
	if _, err := b.Settle(root.ID, 0); err != nil {
		t.Fatalf("settle root: %v", err)
	}
	a := credit(b, "u_alice")
	if a.ReservedMicrocredits != 0 || a.AvailableMicrocredits != 900_000 {
		t.Fatalf("ledger: %+v", a)
	}
	b.Settle(root.ID, 0) // idempotent: no second ledger effect
	if a2 := credit(b, "u_alice"); a2.AvailableMicrocredits != 900_000 {
		t.Fatalf("double settle changed ledger: %+v", a2)
	}
}

func TestBudgetQuota(t *testing.T) {
	db := testDB(t)
	b := Billing{DB: db}
	tenant := model.PersonalTenantID("u_alice")
	fund(t, b, "u_alice", 10_000_000)
	acc, _ := b.Account(db, tenant)
	db.Model(&acc).Update("monthly_quota_micro", 500_000)
	r, err := b.Reserve(tenant, "a", "agent_run", "a", 400_000)
	if err != nil {
		t.Fatal(err)
	}
	b.Settle(r.ID, 400_000)
	if _, err := b.Reserve(tenant, "b", "agent_run", "b", 200_000); !errors.Is(err, ErrBudgetQuota) {
		t.Fatalf("quota must block, got %v", err)
	}
}

func TestSynthesizeDropsInvalidCitationsAndUnknownOutcome(t *testing.T) {
	db := testDB(t)
	b := Billing{DB: db}
	tenant := model.PersonalTenantID("u_alice")
	fund(t, b, "u_alice", 5_000_000)
	ev := []ResolvedEvidence{{EvidenceID: "ev1", DocumentID: "d1", SourceID: "s1", Revision: 1}}
	gw := fakeGW{cost: 1234, text: `{"body":"b","claims":[
	 {"text":"ok","kind":"source_statement","citations":[{"evidence_id":"ev1","revision":1}]},
	 {"text":"bad","kind":"source_statement","citations":[{"evidence_id":"ev_fake","revision":1}]}]}`}
	out, err := Synthesize(context.Background(), gw, b, SynthesisRequest{TenantID: tenant, Key: "k1", Purpose: "brief", Question: "q",
		Evidence: ev, Scope: MaterialScope{SourceIDs: []string{"s1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Claims) != 1 || len(out.Dropped) != 1 || out.CostMicro != 1234 {
		t.Fatalf("synthesis %+v", out)
	}
	_, err = Synthesize(context.Background(), fakeGW{err: ErrModelUnknownOutcome}, b, SynthesisRequest{TenantID: tenant, Key: "k2", Purpose: "brief", Question: "q", Evidence: ev, Scope: MaterialScope{SourceIDs: []string{"s1"}}})
	if !errors.Is(err, ErrModelUnknownOutcome) {
		t.Fatal(err)
	}
	var r model.FlowBudgetReservation
	db.First(&r, "idempotency_key = ?", "k2")
	if r.Status != "unknown" {
		t.Fatalf("want unknown, got %s", r.Status)
	}
}

func TestOrgInviteSingleUseAndGrantRevocation(t *testing.T) {
	db := testDB(t)
	tn, ws, err := CreateOrganization(db, "u_alice", "Acme")
	if err != nil {
		t.Fatal(err)
	}
	var acc model.FlowBillingAccount
	if db.First(&acc, "tenant_id = ?", tn.ID).Error != nil || acc.Kind != "shared" || acc.PayerUserID != "u_alice" {
		t.Fatalf("shared billing account %+v", acc)
	}
	admin, err := ResolveScope(db, "u_alice", ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, tok, err := CreateInvite(db, admin, "u_alice", "bob@x", "editor", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var inv model.FlowInvite
	db.First(&inv)
	if inv.TokenHash == tok {
		t.Fatal("token stored in clear")
	}
	if _, err := AcceptInvite(db, "u_bob", tok); err != nil {
		t.Fatal(err)
	}
	if _, err := AcceptInvite(db, "u_bob", tok); err == nil {
		t.Fatal("invite must be single use")
	}
	if err := AuthorizeGrant(db, "user:u_bob", tn.ID, ws.ID, "intel.reports.write"); err != nil {
		t.Fatalf("bob should be authorized: %v", err)
	}
	if err := RemoveMember(db, admin, "u_bob"); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeGrant(db, "user:u_bob", tn.ID, ws.ID, "intel.reports.write"); err == nil {
		t.Fatal("removed member's grant must be denied")
	}
}

func TestEgressBlocksPrivateAddresses(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "::1", "fe80::1", "fc00::1", "::ffff:127.0.0.1", "0.0.0.0"} {
		if PublicAddr(netip.MustParseAddr(a)) {
			t.Fatalf("%s must be blocked", a)
		}
	}
	if !PublicAddr(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public address must pass")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	_, err := (&http.Client{Transport: PublicTransport()}).Get(srv.URL)
	if err == nil || !errors.Is(err, ErrEgressBlocked) {
		t.Fatalf("loopback dial must be blocked, got %v", err)
	}
}

func TestRunLeaseOwnershipAndTakeover(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	l := RunLeases{DB: db, TTL: time.Minute, Now: func() time.Time { return now }}
	tok, err := l.Acquire("run1", "a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Acquire("run1", "b"); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second owner must be refused: %v", err)
	}
	if again, _ := l.Acquire("run1", "a"); again != tok {
		t.Fatal("re-acquire by owner keeps token")
	}
	now = now.Add(2 * time.Minute) // owner a crashed; lease expired
	tb, err := l.Acquire("run1", "b")
	if err != nil || tb != tok+1 {
		t.Fatalf("takeover: %d %v", tb, err)
	}
	if err := l.Renew("run1", "a", tok); !errors.Is(err, ErrLeaseHeld) {
		t.Fatal("stale owner renew must fail")
	}
	l.Release("run1", "a", tok) // stale release is a no-op
	if err := l.Renew("run1", "b", tb); err != nil {
		t.Fatal(err)
	}
	l.Release("run1", "b", tb)
	if _, err := l.Acquire("run1", "c"); err != nil {
		t.Fatal(err)
	}
}

func TestCanvasConversationDocOptimisticConcurrency(t *testing.T) {
	db := testDB(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	who := "u_alice"
	RegisterCanvasConversationRoutes(r.Group("/api"), db, func(*gin.Context) (*model.User, error) { return &model.User{ID: who}, nil })
	do := func(method, body string) (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/api/agent/canvas-conversations/cv1", strings.NewReader(body))
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}
	if code, _ := do("PUT", `{"baseVersion":0,"document":{"version":1,"activeId":null,"conversations":[]}}`); code != 200 {
		t.Fatal(code)
	}
	if code, _ := do("PUT", `{"baseVersion":0,"document":{"x":1}}`); code != 409 {
		t.Fatalf("stale create must conflict: %d", code)
	}
	if code, _ := do("PUT", `{"baseVersion":1,"document":{"v":2}}`); code != 200 {
		t.Fatal(code)
	}
	if code, body := do("PUT", `{"baseVersion":1,"document":{"v":3}}`); code != 409 || !strings.Contains(body, `"v":2`) {
		t.Fatalf("stale write must conflict and return server copy: %d %s", code, body)
	}
	who = "u_bob"
	if _, body := do("GET", ""); !strings.Contains(body, `"document":null`) {
		t.Fatalf("other user must not see doc: %s", body)
	}
}
