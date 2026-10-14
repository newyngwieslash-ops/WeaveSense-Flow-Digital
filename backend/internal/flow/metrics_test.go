package flow

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
