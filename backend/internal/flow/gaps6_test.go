package flow

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"yingce/backend/internal/model"
)

// PRD §9.2 fourth budget layer: a managed provider account caps the sum of all
// tenants' holds per month; settle moves the hold to consumed, release frees it.
func TestManagedProviderAccountBudgetLayer(t *testing.T) {
	db := testDB(t)
	fund(t, Billing{DB: db}, "u_alice", 10_000_000)
	tenant := model.PersonalTenantID("u_alice")
	if err := db.Create(&model.FlowProviderAccount{ID: "pa_llm", Provider: "openai-compat", MonthlyBudgetMicro: 1_000_000, Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	b := Billing{DB: db, ProviderAccountID: "pa_llm"}
	r1, err := b.Reserve(tenant, "m1", "model", "q", 700_000)
	if err != nil || r1.ProviderAccountID != "pa_llm" {
		t.Fatalf("reserve: %v %+v", err, r1)
	}
	if _, err := b.Reserve(tenant, "m2", "model", "q", 400_000); !errors.Is(err, ErrBudgetProvider) {
		t.Fatalf("provider cap must deny even with tenant credits left, got %v", err)
	}
	if _, err := b.Settle(r1.ID, 200_000); err != nil {
		t.Fatal(err)
	}
	var per model.FlowProviderAccountPeriod
	db.First(&per, "account_id = ?", "pa_llm")
	if per.ReservedMicro != 0 || per.ConsumedMicro != 200_000 {
		t.Fatalf("period after settle: %+v", per)
	}
	r2, err := b.Reserve(tenant, "m2", "model", "q", 400_000)
	if err != nil {
		t.Fatalf("freed provider budget must be reservable: %v", err)
	}
	if _, err := b.Release(r2.ID); err != nil {
		t.Fatal(err)
	}
	db.First(&per, "account_id = ?", "pa_llm")
	if per.ReservedMicro != 0 || per.ConsumedMicro != 200_000 {
		t.Fatalf("period after release: %+v", per)
	}
	db.Model(&model.FlowProviderAccount{}).Where("id = ?", "pa_llm").Update("status", "suspended")
	if _, err := b.Reserve(tenant, "m3", "model", "q", 1); !errors.Is(err, ErrBudgetProvider) {
		t.Fatalf("suspended provider account must deny, got %v", err)
	}
	// Unconfigured account id: layer inactive, nothing tagged.
	r4, err := Billing{DB: db, ProviderAccountID: "pa_none"}.Reserve(tenant, "m4", "model", "q", 1000)
	if err != nil || r4.ProviderAccountID != "" {
		t.Fatalf("unknown provider account must not block: %v %+v", err, r4)
	}
}

func TestScheduleWindowBudgetEndpoints(t *testing.T) {
	db := testDB(t)
	sc, err := ResolveScope(db, "u_alice", "")
	if err != nil {
		t.Fatal(err)
	}
	fund(t, Billing{DB: db}, "u_alice", 10_000)
	cfg := Config{SigningKey: []byte("0123456789abcdef0123456789abcdef"), Issuer: "flow-platform"}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterP5Routes(r.Group("/api"), db, cfg, func(*gin.Context) (*model.User, error) { return nil, nil }, nil)
	tok, _ := Sign(cfg.SigningKey, ExecutionContext{Issuer: "loom-engine", Audience: "flow-platform", TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID,
		Actor: Actor{"service_principal", "loom-engine"}, PolicyVersion: "policy_v1"}, time.Now(), time.Minute)
	call := func(path, body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	reserve := `{"grant_actor":"user:u_alice","workspace_id":"` + sc.WorkspaceID + `","key":"sched:w1","amount_micro":6000,"ref_id":"sch_1"}`
	code, out := call("/api/internal/flow/budget/reserve", reserve)
	if code != http.StatusOK {
		t.Fatalf("reserve: %d %v", code, out)
	}
	id := out["data"].(map[string]any)["reservation_id"].(string)
	if code, out = call("/api/internal/flow/budget/reserve", reserve); code != 200 || out["data"].(map[string]any)["reservation_id"] != id {
		t.Fatalf("same key must replay: %d %v", code, out)
	}
	if code, _ = call("/api/internal/flow/budget/reserve", strings.Replace(reserve, "sched:w1", "sched:w2", 1)); code != http.StatusPaymentRequired {
		t.Fatalf("insufficient credits must be 402, got %d", code)
	}
	if code, _ = call("/api/internal/flow/budget/reserve", strings.Replace(reserve, "user:u_alice", "user:u_bob", 1)); code != http.StatusForbidden {
		t.Fatalf("non-member grant actor must be 403, got %d", code)
	}
	if code, out = call("/api/internal/flow/budget/close", `{"reservation_id":"`+id+`","outcome":"release"}`); code != 200 || out["data"].(map[string]any)["status"] != "released" {
		t.Fatalf("release: %d %v", code, out)
	}
	if credit(Billing{DB: db}, "u_alice").AvailableMicrocredits != 10_000 {
		t.Fatal("released window must refund the hold")
	}
}

func TestEngineEventsResearchBlockedRevisedAndProjectionSync(t *testing.T) {
	db := testDB(t)
	sc, _ := ResolveScope(db, "u_alice", "")
	ev := func(id, typ, op string, seq int64, payload string) Event {
		return Event{EventID: id, OperationID: op, JobID: op, TenantID: sc.TenantID, WorkspaceID: sc.WorkspaceID, Sequence: seq, Type: typ,
			OccurredAt: time.Now().UTC(), PayloadVersion: 1, Payload: json.RawMessage(payload)}
	}
	if _, err := ConsumeEvent(db, "platform", ev("e1", "research.completed", "research:rr_1", 1, `{"research_run_id":"rr_1","status":"succeeded"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ConsumeEvent(db, "platform", ev("e2", "schedule.blocked", "sch:s1", 2, `{"schedule_id":"s1","reason":"budget_insufficient"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ConsumeEvent(db, "platform", ev("e3", "document.revised", "doc:d1", 3, `{"document_id":"d_uncited","revision":2}`)); err != nil {
		t.Fatal(err)
	}
	var notes []model.FlowNotification
	db.Order("created_at").Find(&notes)
	types := []string{}
	for _, n := range notes {
		types = append(types, n.Type)
	}
	if strings.Join(types, ",") != "research.completed,schedule.blocked" {
		t.Fatalf("notifications: %v (uncited revision must not notify)", types)
	}
	var p model.FlowOperationProjection
	db.First(&p, "operation_id = ?", "research:rr_1")
	if p.Status != "succeeded" || p.SourceVersion != 1 || p.SyncStatus != "synced" {
		t.Fatalf("projection: %+v", p)
	}
	p = model.FlowOperationProjection{}
	db.First(&p, "operation_id = ?", "sch:s1")
	if p.Status != "blocked" {
		t.Fatalf("blocked projection: %+v", p)
	}
	if ProjectionSyncStatus(p, p.AsOf.Add(time.Hour)) != "stale" || ProjectionSyncStatus(p, p.AsOf) != "synced" {
		t.Fatal("non-terminal projection must turn stale after ProjectionStaleAfter")
	}
	if _, err := ConsumeEvent(db, "platform", ev("e4", "research.completed", "research:rr_2", 4, `{"research_run_id":"rr_2","status":"failed"}`)); err != nil {
		t.Fatal(err)
	}
	p = model.FlowOperationProjection{}
	db.First(&p, "operation_id = ?", "research:rr_2")
	if p.Status != "failed" {
		t.Fatalf("failed research projection: %+v", p)
	}
}
