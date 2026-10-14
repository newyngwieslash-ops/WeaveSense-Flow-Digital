package flow

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"yingce/backend/internal/model"
)

// RegisterMetricsRoute exposes Prometheus gauges for the Flow platform (§14).
// It is off unless FLOW_METRICS_TOKEN is set, and then requires that bearer
// token. Only aggregate counts by status are exported, no tenant ids.
func RegisterMetricsRoute(r gin.IRouter, db *gorm.DB) {
	r.GET("/metrics/flow", func(c *gin.Context) {
		tok := os.Getenv("FLOW_METRICS_TOKEN")
		if tok == "" {
			c.Status(http.StatusNotFound)
			return
		}
		if subtle.ConstantTimeCompare([]byte(c.GetHeader("Authorization")), []byte("Bearer "+tok)) != 1 {
			c.Status(http.StatusUnauthorized)
			return
		}
		c.Data(http.StatusOK, "text/plain; version=0.0.4", []byte(MetricsText(db)))
	})
}

// MetricsText renders the platform gauges in Prometheus text format.
func MetricsText(db *gorm.DB) string {
	var b strings.Builder
	byStatus := func(name, help string, m any) {
		type row struct {
			Status string
			N      int64
		}
		var rows []row
		db.Model(m).Select("status, count(*) as n").Group("status").Scan(&rows)
		sort.Slice(rows, func(i, j int) bool { return rows[i].Status < rows[j].Status })
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
		for _, r := range rows {
			fmt.Fprintf(&b, "%s{status=%q} %d\n", name, r.Status, r.N)
		}
	}
	fmt.Fprintf(&b, "# HELP flow_platform_up Platform is serving.\n# TYPE flow_platform_up gauge\nflow_platform_up 1\n")
	byStatus("flow_notification_deliveries", "Email/webhook deliveries by status.", &model.FlowNotificationDelivery{})
	byStatus("flow_notification_targets", "Notification targets by status.", &model.FlowNotificationTarget{})
	byStatus("flow_model_tasks", "Async model tasks by status.", &model.FlowModelTask{})
	byStatus("flow_mcp_calls", "MCP calls by status (reconciliation_required needs an operator).", &model.FlowMCPCall{})
	byStatus("flow_budget_reservations", "Budget reservations by status.", &model.FlowBudgetReservation{})
	byStatus("flow_usage_records", "Usage records by billing status.", &model.FlowUsageRecord{})
	return b.String()
}
