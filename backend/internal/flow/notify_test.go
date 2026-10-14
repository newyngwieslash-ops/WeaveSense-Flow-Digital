package flow

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"yingce/backend/internal/model"
)

func notifyFixture(t *testing.T) (Billing, Scope) {
	db := testDB(t)
	ws := model.PersonalWorkspaceID("u_alice")
	s, err := ResolveScope(db, "u_alice", ws)
	if err != nil {
		t.Fatal(err)
	}
	return Billing{DB: db}, s
}

func reportEvent(s Scope, id string) Event {
	return Event{EventID: id, OperationID: "op_" + id, JobID: "j", TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, Sequence: 1,
		Type: "report.completed", OccurredAt: time.Now(), Payload: json.RawMessage(`{"report_id":"rep_1","title":"储能周报"}`)}
}

func deliveries(t *testing.T, b Billing, s Scope) []model.FlowNotificationDelivery {
	out, err := ListNotificationDeliveries(b.DB, s, "")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWebhookDeliveryIdempotentFanOutRetryAndSignature(t *testing.T) {
	b, s := notifyFixture(t)
	AllowInsecureWebhooks = true
	t.Cleanup(func() { AllowInsecureWebhooks = false })
	t.Setenv("FLOW_HOOK_SECRET", "hook-secret-1")
	var mu sync.Mutex
	var ids []string
	fail := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m := hmac.New(sha256.New, []byte("hook-secret-1"))
		m.Write(body)
		if r.Header.Get("X-Flow-Signature") != "sha256="+hex.EncodeToString(m.Sum(nil)) {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		ids = append(ids, r.Header.Get("X-Flow-Delivery-Id"))
		f := fail
		mu.Unlock()
		if f {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	if _, err := CreateNotificationTarget(b.DB, s, "u_alice", TargetInput{Channel: "webhook", Address: srv.URL + "/hook", SecretRef: "env:FLOW_HOOK_SECRET"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // duplicate event delivery
		if _, err := ConsumeEvent(b.DB, "platform-projection", reportEvent(s, "evt_1")); err != nil {
			t.Fatal(err)
		}
	}
	if d := deliveries(t, b, s); len(d) != 1 || d[0].Status != "pending" || d[0].Title != "储能周报" {
		t.Fatalf("fan-out must be once per event: %+v", d)
	}
	clock := time.Now().UTC()
	n := Notifier{DB: b.DB, HTTP: srv.Client(), Now: func() time.Time { return clock }, Backoff: time.Second}
	if _, err := n.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := deliveries(t, b, s)[0]
	if d.Status != StatusRetryWait || d.Attempts != 1 {
		t.Fatalf("503 must wait for retry: %+v", d)
	}
	mu.Lock()
	fail = false
	mu.Unlock()
	clock = clock.Add(2 * time.Second)
	_, _ = n.RunDue(context.Background())
	d = deliveries(t, b, s)[0]
	if d.Status != "delivered" || d.Receipt != "http 204" || d.Attempts != 2 {
		t.Fatalf("delivered: %+v", d)
	}
	if len(ids) != 2 || ids[0] != d.ID || ids[1] != d.ID {
		t.Fatalf("stable delivery id across attempts: %v", ids)
	}
	sum, _ := SummarizeUsage(b.DB, s.TenantID, time.Time{})
	if len(sum) != 1 || sum[0].UsageType != UsageNotifications || sum[0].Quantity != 1 {
		t.Fatalf("notification usage: %+v", sum)
	}
}

func TestWebhookGoneDisablesTargetWithoutRerouting(t *testing.T) {
	b, s := notifyFixture(t)
	AllowInsecureWebhooks = true
	t.Cleanup(func() { AllowInsecureWebhooks = false })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(410) }))
	defer srv.Close()
	tg, _ := CreateNotificationTarget(b.DB, s, "u_alice", TargetInput{Channel: "webhook", Address: srv.URL}, time.Now())
	_, _ = ConsumeEvent(b.DB, "c", reportEvent(s, "evt_a"))
	_, _ = ConsumeEvent(b.DB, "c", reportEvent(s, "evt_b"))
	n := Notifier{DB: b.DB, HTTP: srv.Client()}
	// Claim only the first by making the second not yet due.
	b.DB.Model(&model.FlowNotificationDelivery{}).Where("event_id = ?", "evt_b").Update("next_attempt_at", time.Now().Add(time.Hour))
	_, _ = n.RunDue(context.Background())
	var got model.FlowNotificationTarget
	b.DB.First(&got, "id = ?", tg.ID)
	if got.Status != "disabled" {
		t.Fatalf("target must be disabled: %+v", got)
	}
	st := map[string]string{}
	for _, d := range deliveries(t, b, s) {
		st[d.EventID] = d.Status
	}
	if st["evt_a"] != "failed" || st["evt_b"] != "cancelled" {
		t.Fatalf("statuses %v", st)
	}
	_, _ = ConsumeEvent(b.DB, "c", reportEvent(s, "evt_c"))
	if len(deliveries(t, b, s)) != 2 {
		t.Fatal("a disabled target receives nothing new")
	}
}

func TestEmailAmbiguousGoesToReconciliationAndIsNotResentBlindly(t *testing.T) {
	b, s := notifyFixture(t)
	sent := 0
	mode := "ambiguous"
	mailer := func(_ context.Context, to, subject, body string, h map[string]string) (string, error) {
		sent++
		if mode == "ambiguous" {
			return "", fmt.Errorf("%w: lost reply", errAmbiguous)
		}
		if h["X-Flow-Delivery-Id"] == "" || to != "ops@example.com" {
			return "", errors.New("bad message")
		}
		return "queued as 1", nil
	}
	tg, err := CreateNotificationTarget(b.DB, s, "u_alice", TargetInput{Channel: "email", Address: "ops@example.com"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ConsumeEvent(b.DB, "c", reportEvent(s, "evt_m"))
	n := Notifier{DB: b.DB, Mail: mailer}
	_, _ = n.RunDue(context.Background())
	_, _ = n.RunDue(context.Background())
	d := deliveries(t, b, s)[0]
	if d.Status != StatusReconciliation || sent != 1 {
		t.Fatalf("ambiguous send must not be retried blindly: %+v sent=%d", d, sent)
	}
	if _, err := ResolveDelivery(b.DB, s, d.ID, false, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	mode = "ok"
	_, _ = n.RunDue(context.Background())
	if d = deliveries(t, b, s)[0]; d.Status != "delivered" || sent != 2 {
		t.Fatalf("after reconcile: %+v", d)
	}
	if _, err := ResolveDelivery(b.DB, s, d.ID, true, time.Now()); !errors.Is(err, ErrBudgetState) {
		t.Fatal("only reconciliation_required deliveries can be resolved")
	}
	if err := RevokeNotificationTarget(b.DB, s, tg.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, _ = ConsumeEvent(b.DB, "c", reportEvent(s, "evt_n"))
	if len(deliveries(t, b, s)) != 1 {
		t.Fatal("revoked target gets no deliveries")
	}
}

func TestNotificationTargetValidation(t *testing.T) {
	for _, in := range []TargetInput{{Channel: "webhook", Address: "http://example.com/h"}, {Channel: "webhook", Address: "https://u:p@example.com/h"},
		{Channel: "email", Address: "not-an-email"}, {Channel: "sms", Address: "1"}, {Channel: "webhook", Address: "https://e.com", SecretRef: "plain"},
		{Channel: "email", Address: "a@b.co", EventTypes: []string{"anything"}}} {
		if _, err := validateTarget(in); !errors.Is(err, ErrNotifyTarget) {
			t.Fatalf("%+v must be rejected", in)
		}
	}
}

func TestUsageEventsPricedWithFrozenPriceVersionAndChargedOnce(t *testing.T) {
	b, s := notifyFixture(t)
	fund(t, b, "u_alice", 1_000_000)
	t.Setenv("FLOW_PRICE_BOOK", `{"version":"price_2026_10","unit_micro":{"ocr_pages":3000}}`)
	ev := Event{EventID: "evt_u1", OperationID: "ocr:doc_1", JobID: "doc_1", TenantID: s.TenantID, WorkspaceID: s.WorkspaceID, Sequence: 1,
		Type: "usage.recorded", OccurredAt: time.Now(), Payload: json.RawMessage(`{"usage_type":"ocr_pages","quantity":2,"document_id":"doc_1"}`)}
	for i := 0; i < 2; i++ {
		if _, err := ConsumeEvent(b.DB, "platform-projection", ev); err != nil {
			t.Fatal(err)
		}
	}
	var rows []model.FlowUsageRecord
	b.DB.Find(&rows)
	if len(rows) != 1 || rows[0].PriceVersion != "price_2026_10" || rows[0].CostMicro != 6000 || rows[0].Status != "charged" {
		t.Fatalf("usage: %+v", rows)
	}
	if a := credit(b, "u_alice"); a.AvailableMicrocredits != 1_000_000-6000 {
		t.Fatalf("charged once: %+v", a)
	}
	r, err := b.Reserve(s.TenantID, "k_pv", "agent_run", "x", 100)
	if err != nil || r.PriceVersion != "price_2026_10" {
		t.Fatalf("reservation must freeze price version: %+v %v", r, err)
	}
	c, _ := b.Allocate(r.ID, "k_pv_c", "research", "x", 50)
	if c.PriceVersion != r.PriceVersion {
		t.Fatal("child inherits the frozen price version")
	}
	t.Setenv("FLOW_PRICE_BOOK", `{"version":"bad","unit_micro":{"nope":1}}`)
	if CurrentPriceBook().Version != DefaultPriceBook.Version {
		t.Fatal("invalid price book falls back to default")
	}
	if _, err := RecordUsage(b.DB, UsageInput{TenantID: s.TenantID, UsageType: "bogus", IdemKey: "x"}, time.Now()); !errors.Is(err, ErrUsageInvalid) {
		t.Fatal("unknown usage type rejected")
	}
}
