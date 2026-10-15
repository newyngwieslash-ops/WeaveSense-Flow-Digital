package flow

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

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
	// Oldest queued platform job (async model tasks waiting to run).
	var oldest struct{ Age float64 }
	var first model.FlowModelTask
	if db.Model(&model.FlowModelTask{}).Where("status IN ?", []string{"queued", "retry_wait"}).Order("created_at").Limit(1).Find(&first).RowsAffected > 0 {
		oldest.Age = time.Since(first.CreatedAt).Seconds()
	}
	fmt.Fprintf(&b, "# HELP flow_model_task_oldest_queued_seconds Age of the oldest queued/retrying model task.\n# TYPE flow_model_task_oldest_queued_seconds gauge\nflow_model_task_oldest_queued_seconds %d\n", int64(oldest.Age))
	// Budget pressure: tenants whose month-to-date consumption reached 90% of their quota.
	var near int64
	db.Raw(`SELECT count(*) FROM flow_billing_accounts a WHERE a.monthly_quota_micro > 0 AND
		(SELECT coalesce(sum(CASE WHEN r.status = 'settled' THEN r.consumed_micro ELSE r.reserved_micro END),0) FROM flow_budget_reservations r
		 WHERE r.account_id = a.id AND r.parent_id = '' AND r.created_at >= ? AND r.status <> 'released') >= a.monthly_quota_micro * 9 / 10`, monthStart(time.Now().UTC())).Scan(&near)
	fmt.Fprintf(&b, "# HELP flow_budget_accounts_near_quota Billing accounts at >=90%% of their monthly quota.\n# TYPE flow_budget_accounts_near_quota gauge\nflow_budget_accounts_near_quota %d\n", near)
	writeCounters(&b)
	return b.String()
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}
