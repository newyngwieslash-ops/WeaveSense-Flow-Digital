package database

import "gorm.io/gorm"

// flowRLSTables are platform tables holding tenant data (tenant_id column).
var flowRLSTables = []string{
	"flow_agent_conversations", "flow_agent_messages", "flow_agent_tool_calls", "flow_notifications",
	"flow_mcp_connections", "flow_mcp_usages", "flow_mcp_calls", "flow_budget_reservations",
	"flow_invites", "flow_external_waits", "flow_skill_installs",
	"flow_model_tasks", "flow_mcp_oauth_grants", "flow_oauth_states",
	"flow_usage_records", "flow_notification_targets", "flow_notification_deliveries",
	"flow_mcp_secrets", "flow_capability_snapshots", "flow_run_capabilities",
	"flow_topics", "flow_event_inbox", "flow_operation_projections", "flow_billing_accounts", "flow_skill_run_pins",
	"flow_command_outbox",
	// Education math workbench (schema v68).
	"edu_org_policies", "edu_role_bindings", "edu_classes", "edu_teaching_assignments", "edu_enrollments", "edu_problems",
	"edu_problem_revisions", "edu_math_jobs", "edu_stage_runs", "edu_artifacts", "edu_solution_revisions", "edu_lesson_revisions",
	"edu_review_records", "edu_publications", "edu_assignments", "edu_audit_events",
	"edu_class_invites", "edu_consent_records",
	"edu_attempts", "edu_hint_unlocks", "edu_self_tests", "edu_review_items", "edu_help_requests", "edu_help_replies",
	"edu_predictions", "edu_figure_interactions", "edu_variant_sets", "edu_variant_reviews", "edu_assignment_extensions", "edu_lesson_edits",
	"edu_ledger_anomalies",
}

// flowSystemOnlyTables are platform-wide tables with no tenant_id (managed
// provider accounts shared by every tenant). Under FORCE RLS they are visible
// and writable only in system context; tenant-scoped transactions see no rows
// and cannot insert. Billing switches to system context for these statements
// (flow.withSystemContext).
var flowSystemOnlyTables = []string{"flow_provider_accounts", "flow_provider_account_periods"}

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
	system := "coalesce(current_setting('app.tenant_ids', true), '') = ''"
	pols := map[string]string{}
	for _, t := range flowRLSTables {
		pols[t] = system + " OR tenant_id = ANY(string_to_array(current_setting('app.tenant_ids', true), ','))"
	}
	for _, t := range flowSystemOnlyTables {
		pols[t] = system
	}
	for _, t := range append(append([]string{}, flowRLSTables...), flowSystemOnlyTables...) {
		if !tx.Migrator().HasTable(t) {
			continue
		}
		pol := pols[t]
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
