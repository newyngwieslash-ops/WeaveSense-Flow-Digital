package flow

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"yingce/backend/internal/model"
)

func TestMetricsRouteTokenAndContent(t *testing.T) {
	db := testDB(t)
	db.Create(&model.FlowMCPCall{ID: "m1", TenantID: "tn_hidden", IdemKey: "k", ConnectionID: "c", Tool: "t", Status: StatusReconciliation})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterMetricsRoute(r, db)
	get := func(auth string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/metrics/flow", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		r.ServeHTTP(w, req)
		return w
	}
	if w := get(""); w.Code != http.StatusNotFound {
		t.Fatalf("disabled metrics served: %d", w.Code)
	}
	t.Setenv("FLOW_METRICS_TOKEN", "tok")
	if w := get("Bearer nope"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", w.Code)
	}
	w := get("Bearer tok")
	body := w.Body.String()
	if w.Code != 200 || !strings.Contains(body, `flow_mcp_calls{status="reconciliation_required"} 1`) || strings.Contains(body, "tn_hidden") {
		t.Fatalf("%d %s", w.Code, body)
	}
}

// §14: the counters and gauges behind the budget / MCP / citation / duplicate
// delivery / oldest-queued alerts are actually emitted.
func TestMetricsEmitAlertSignals(t *testing.T) {
	db := testDB(t)
	CountBudgetDenied(ErrBudgetQuota)
	CountMCPError("call_failed")
	CountCitationRejected("report_save")
	e := Event{EventID: "evt_dup", TenantID: "tn_x", OperationID: "op1", Type: "collection.completed", Payload: []byte(`{}`)}
	if _, err := ConsumeEvent(db, "c1", e); err != nil {
		t.Fatal(err)
	}
	if applied, _ := ConsumeEvent(db, "c1", e); applied {
		t.Fatal("duplicate applied")
	}
	db.Create(&model.FlowModelTask{ID: "mt_old", TenantID: "tn_x", Status: "queued", CreatedAt: time.Now().Add(-2 * time.Hour)})
	body := MetricsText(db)
	for _, want := range []string{`flow_budget_denied_total{reason="quota"}`, `flow_mcp_errors_total{kind="call_failed"}`,
		`flow_citation_rejected_total{stage="report_save"}`, `flow_event_duplicate_deliveries_total{source="inbox"}`,
		"flow_budget_accounts_near_quota 0", "flow_model_task_oldest_queued_seconds 7"} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}
