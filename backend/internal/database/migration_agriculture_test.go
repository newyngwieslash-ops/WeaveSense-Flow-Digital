package database

import (
	"errors"
	"strings"
	"testing"

	"yingce/backend/internal/model"
)

func TestAgricultureMigrationSQLitePreservesLegacyAndUniqueRoles(t *testing.T) {
	db, err := Open(Config{Driver: "sqlite", DSN: "file:" + t.Name() + "?mode=memory&cache=shared"})
	if err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{
		"CREATE TABLE flow_workspaces(id text PRIMARY KEY, tenant_id text, slug text, name text, created_at datetime)",
		"INSERT INTO flow_workspaces(id,tenant_id,slug,name) VALUES ('old-w','old-t','default','Existing')",
		"CREATE TABLE flow_invites(id text PRIMARY KEY, tenant_id text, workspace_id text, email text, role text, token_hash text, invited_by text, expires_at datetime, accepted_by text, accepted_at datetime, revoked_at datetime, created_at datetime)",
		"INSERT INTO flow_invites(id,tenant_id,workspace_id,email,role,token_hash) VALUES ('old-i','old-t','old-w','fixture@example.invalid','viewer','old-hash')",
	} {
		if err := db.Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := migrateAgriculture(db); err != nil {
			t.Fatal(err)
		}
	}
	var workspace model.Workspace
	var invite model.FlowInvite
	if err := db.First(&workspace, "id = ?", "old-w").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&invite, "id = ?", "old-i").Error; err != nil {
		t.Fatal(err)
	}
	if workspace.Product != "" || invite.Product != "" || workspace.Name != "Existing" || invite.TokenHash != "old-hash" {
		t.Fatal("legacy records changed")
	}
	first := model.AgricultureRoleBinding{ID: "r1", TenantID: "ta", WorkspaceID: "wa", UserID: "ua", Role: "viewer"}
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	first.ID = "r2"
	if err := db.Create(&first).Error; err == nil {
		t.Fatal("duplicate capability binding accepted")
	}
	first.Role = "analyst"
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	first.ID, first.WorkspaceID, first.Role = "r3", "", "tenant_admin"
	if err := db.Create(&first).Error; err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable(&model.AgricultureAuditEvent{}) {
		t.Fatal("missing audit table")
	}
	if !errors.Is(RequireAgricultureAdmission(db), ErrAgricultureAdmissionUnavailable) {
		t.Fatal("SQLite must not claim PostgreSQL admission readiness")
	}
}

func TestAgricultureAdmissionSQLContract(t *testing.T) {
	statements := agricultureAdmissionStatements()
	positions := make(map[string]int)
	for index, statement := range statements {
		positions[statement] = index
	}
	previous := -1
	for _, statement := range []string{
		agricultureAdmissionFunction,
		"REVOKE ALL ON FUNCTION agri_admission.resolve_invite(text, text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION agri_admission.resolve_invite(text, text) TO flow_platform_app",
		"ALTER FUNCTION agri_admission.resolve_invite(text, text) OWNER TO flow_platform_admission",
		"REVOKE CREATE ON SCHEMA agri_admission FROM flow_platform_admission",
	} {
		index, exists := positions[statement]
		if !exists || index <= previous {
			t.Fatal("function privileges must be constrained and granted before non-inherited ownership transfer")
		}
		previous = index
	}
	for _, required := range []string{"runtime.rolcanlogin", "runtime.rolinherit", "runtime.rolsuper", "runtime.rolbypassrls", "runtime.rolcreatedb", "runtime.rolcreaterole", "runtime.rolreplication", "pg_catalog.pg_has_role(current_user, c.relowner, 'MEMBER')"} {
		if !strings.Contains(agricultureAdmissionReadinessSQL, required) {
			t.Errorf("missing runtime privilege gate: %s", required)
		}
	}
	for _, required := range []string{
		"SECURITY DEFINER", "SET search_path = pg_catalog", "i.token_hash = $1", "u.id = $2", "$1 ~ '^[0-9a-f]{64}$'",
		"public.flow_invites", "public.flow_workspaces", "public.flow_tenants", "public.users",
		"i.product = 'agriculture'", "w.product = 'agriculture'", "w.tenant_id = i.tenant_id",
		"i.accepted_at IS NULL", "i.revoked_at IS NULL", "i.expires_at > CURRENT_TIMESTAMP",
		"u.email_verified_at IS NOT NULL", "t.status = 'active'", "u.status = 'active'",
		"pg_catalog.lower(pg_catalog.btrim(u.email)) = pg_catalog.lower(pg_catalog.btrim(i.email))",
	} {
		if !strings.Contains(agricultureAdmissionFunction, required) {
			t.Errorf("missing security predicate: %s", required)
		}
	}
	for _, prohibited := range []string{"EXECUTE ", "INSERT ", "UPDATE ", "DELETE ", "set_config", "i.token_hash = token_hash"} {
		if strings.Contains(agricultureAdmissionFunction, prohibited) {
			t.Errorf("unsafe admission SQL: %s", prohibited)
		}
	}
	if strings.Contains(agricultureTenantPolicy, " OR ") || !strings.Contains(agricultureTenantPolicy, "ANY(string_to_array") {
		t.Fatal("new table policy must have exact tenant membership and no system fallback")
	}
}
