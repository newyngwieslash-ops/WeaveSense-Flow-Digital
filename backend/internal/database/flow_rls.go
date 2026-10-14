package database

import "gorm.io/gorm"

// flowRLSTables are platform tables holding tenant data (tenant_id column).
var flowRLSTables = []string{
	"flow_agent_conversations", "flow_agent_messages", "flow_agent_tool_calls", "flow_notifications",
	"flow_mcp_connections", "flow_mcp_usages", "flow_mcp_calls", "flow_budget_reservations",
	"flow_invites", "flow_external_waits", "flow_skill_installs",
	"flow_model_tasks", "flow_mcp_oauth_grants", "flow_oauth_states",
	"flow_usage_records", "flow_notification_targets", "flow_notification_deliveries",
}

// applyFlowRLS enables Postgres row-level security on platform flow tables as
// a second tenant-isolation layer. Every authenticated platform request runs in
// a transaction with app.tenant_ids = the caller's active tenants (see
// flow.TenantMiddleware); it only sees/writes those tenants' rows. An unset or
// empty value is system context (workers, migrations, engine callbacks) and
// sees everything. No-op on SQLite.
func applyFlowRLS(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	for _, t := range flowRLSTables {
		if !tx.Migrator().HasTable(t) {
			continue
		}
		pol := "coalesce(current_setting('app.tenant_ids', true), '') = '' OR tenant_id = ANY(string_to_array(current_setting('app.tenant_ids', true), ','))"
		for _, stmt := range []string{
			"ALTER TABLE " + t + " ENABLE ROW LEVEL SECURITY",
			"ALTER TABLE " + t + " FORCE ROW LEVEL SECURITY",
			"DROP POLICY IF EXISTS tenant_isolation ON " + t,
			"CREATE POLICY tenant_isolation ON " + t + " USING (" + pol + ") WITH CHECK (" + pol + ")",
		} {
			if err := tx.Exec(stmt).Error; err != nil {
				return err
			}
		}
	}
	return nil
}
