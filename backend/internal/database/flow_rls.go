package database

import "gorm.io/gorm"

// flowRLSTables are platform tables holding tenant data (tenant_id column).
var flowRLSTables = []string{
	"flow_agent_conversations", "flow_agent_messages", "flow_agent_tool_calls", "flow_notifications",
	"flow_mcp_connections", "flow_mcp_usages", "flow_mcp_calls", "flow_budget_reservations",
	"flow_invites", "flow_external_waits", "flow_skill_installs",
}

// applyFlowRLS enables Postgres row-level security on platform flow tables as
// a second tenant-isolation layer. Requests that set app.tenant_id (see
// flow.TenantTx) only see their tenant's rows; an unset/empty value is system
// context (workers, migrations, admin) and sees everything. No-op on SQLite.
func applyFlowRLS(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	for _, t := range flowRLSTables {
		if !tx.Migrator().HasTable(t) {
			continue
		}
		pol := "coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)"
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
