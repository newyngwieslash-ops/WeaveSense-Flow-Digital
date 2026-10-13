package flow

import (
	"context"
	"errors"
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
