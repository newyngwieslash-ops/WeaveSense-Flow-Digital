package database

import (
	"errors"
	"fmt"

	"yingce/backend/internal/model"

	"gorm.io/gorm"
)

var ErrAgricultureAdmissionUnavailable = errors.New("agriculture admission resolver unavailable or unsafe; configure the dedicated PostgreSQL role and migration before enabling agriculture")

const agricultureTenantPolicy = "tenant_id <> '' AND tenant_id = ANY(string_to_array(current_setting('app.tenant_ids', true), ','))"

// The function resolves only the invitation's routing identity. Acceptance must
// still lock/revalidate the invitation and current authorizations in a tenant
// transaction. In particular this function never changes or consumes a token.
const agricultureAdmissionFunction = `CREATE OR REPLACE FUNCTION agri_admission.resolve_invite(token_hash text, actor_user_id text)
RETURNS TABLE(invite_id text, tenant_id text, workspace_id text, recipient_user_id text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog
AS $agri$
  SELECT i.id::text, i.tenant_id::text, i.workspace_id::text, u.id::text
  FROM public.flow_invites AS i
  JOIN public.flow_workspaces AS w ON w.id = i.workspace_id AND w.tenant_id = i.tenant_id
  JOIN public.flow_tenants AS t ON t.id = i.tenant_id
  JOIN public.users AS u ON u.id = $2
  WHERE $1 ~ '^[0-9a-f]{64}$' AND i.token_hash = $1
    AND $2 <> ''
    AND i.product = 'agriculture' AND w.product = 'agriculture'
    AND i.accepted_at IS NULL AND i.revoked_at IS NULL AND i.expires_at > CURRENT_TIMESTAMP
    AND t.status = 'active' AND u.status = 'active'
    AND u.email_verified_at IS NOT NULL
    AND pg_catalog.btrim(i.email) <> ''
    AND pg_catalog.lower(pg_catalog.btrim(u.email)) = pg_catalog.lower(pg_catalog.btrim(i.email))
$agri$`

func migrateAgriculture(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&model.Workspace{}, &model.FlowInvite{}, &model.AgricultureRoleBinding{}, &model.AgricultureAuditEvent{}); err != nil {
		return err
	}
	if tx.Dialector.Name() != "postgres" {
		return nil // SQLite verifies models only, never PostgreSQL admission/isolation.
	}
	for _, table := range []string{"flow_agriculture_role_bindings", "flow_agriculture_audit_events"} {
		for _, statement := range []string{
			"ALTER TABLE " + table + " ENABLE ROW LEVEL SECURITY",
			"ALTER TABLE " + table + " FORCE ROW LEVEL SECURITY",
			"DROP POLICY IF EXISTS agriculture_tenant_isolation ON " + table,
			"CREATE POLICY agriculture_tenant_isolation ON " + table + " USING (" + agricultureTenantPolicy + ") WITH CHECK (" + agricultureTenantPolicy + ")",
		} {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
	}
	var available bool
	if err := tx.Raw("SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'flow_platform_admission')").Scan(&available).Error; err != nil {
		return err
	}
	if !available {
		// Ordinary upstream deployments still migrate. Their agriculture startup
		// must call RequireAgricultureAdmission and fail closed, not use system scope.
		return nil
	}
	var schema string
	if err := tx.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		return err
	}
	if schema != "public" {
		return nil
	} // Isolated upstream test schemas do not alter the public resolver.
	var safe bool
	if err := tx.Raw("SELECT NOT (rolcanlogin OR rolinherit OR rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole OR rolreplication) FROM pg_catalog.pg_roles WHERE rolname = 'flow_platform_admission'").Scan(&safe).Error; err != nil {
		return err
	}
	if !safe {
		return ErrAgricultureAdmissionUnavailable
	}
	statements := []string{
		"CREATE SCHEMA IF NOT EXISTS agri_admission",
		"REVOKE ALL ON SCHEMA agri_admission FROM PUBLIC",
		"GRANT USAGE, CREATE ON SCHEMA agri_admission TO flow_platform_admission",
		"GRANT USAGE ON SCHEMA public TO flow_platform_admission",
		"GRANT SELECT (id, tenant_id, workspace_id, token_hash, product, accepted_at, revoked_at, expires_at, email) ON public.flow_invites TO flow_platform_admission",
		"GRANT SELECT (id, tenant_id, product) ON public.flow_workspaces TO flow_platform_admission",
		"GRANT SELECT (id, status) ON public.flow_tenants TO flow_platform_admission",
		"GRANT SELECT (id, email, email_verified_at, status) ON public.users TO flow_platform_admission",
		"DROP POLICY IF EXISTS agriculture_admission_read ON public.flow_invites",
		"CREATE POLICY agriculture_admission_read ON public.flow_invites FOR SELECT TO flow_platform_admission USING (product = 'agriculture')",
		// Restrictive policy prevents the old empty-scope permissive policy from
		// broadening this dedicated role. Existing application roles are unchanged.
		"DROP POLICY IF EXISTS agriculture_admission_limit ON public.flow_invites",
		"CREATE POLICY agriculture_admission_limit ON public.flow_invites AS RESTRICTIVE FOR SELECT TO flow_platform_admission USING (product = 'agriculture')",
		agricultureAdmissionFunction,
		"REVOKE ALL ON FUNCTION agri_admission.resolve_invite(text, text) FROM PUBLIC",
		"ALTER FUNCTION agri_admission.resolve_invite(text, text) OWNER TO flow_platform_admission",
		"REVOKE CREATE ON SCHEMA agri_admission FROM flow_platform_admission",
		"GRANT USAGE ON SCHEMA agri_admission TO flow_platform_app",
		"GRANT EXECUTE ON FUNCTION agri_admission.resolve_invite(text, text) TO flow_platform_app",
	}
	for _, statement := range statements {
		if err := tx.Exec(statement).Error; err != nil {
			return fmt.Errorf("install agriculture admission: %w", err)
		}
	}
	return nil
}

const agricultureAdmissionReadinessSQL = `SELECT EXISTS (
 SELECT 1 FROM pg_catalog.pg_proc p
 JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
 JOIN pg_catalog.pg_roles r ON r.oid = p.proowner
 JOIN pg_catalog.pg_roles runtime ON runtime.rolname = current_user
 WHERE n.nspname = 'agri_admission' AND p.proname = 'resolve_invite'
 AND pg_catalog.oidvectortypes(p.proargtypes) = 'text, text'
 AND p.prosecdef AND 'search_path=pg_catalog' = ANY(p.proconfig)
 AND r.rolname = 'flow_platform_admission'
 AND NOT (r.rolcanlogin OR r.rolinherit OR r.rolsuper OR r.rolbypassrls OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.aclexplode(coalesce(p.proacl, pg_catalog.acldefault('f', p.proowner))) a WHERE a.grantee = 0 AND a.privilege_type = 'EXECUTE')
 AND pg_catalog.has_function_privilege(current_user, p.oid, 'EXECUTE')
 AND NOT pg_catalog.pg_has_role(current_user, r.oid, 'MEMBER')
 AND runtime.rolcanlogin
 AND NOT (runtime.rolinherit OR runtime.rolsuper OR runtime.rolbypassrls OR runtime.rolcreatedb OR runtime.rolcreaterole OR runtime.rolreplication)
 AND NOT EXISTS (
   SELECT 1 FROM pg_catalog.pg_class c
   JOIN pg_catalog.pg_namespace ns ON ns.oid = c.relnamespace
   WHERE ns.nspname = 'public' AND c.relkind IN ('r', 'p')
   AND pg_catalog.pg_has_role(current_user, c.relowner, 'MEMBER')
 )
)`

// RequireAgricultureAdmission is a production startup gate, not a substitute for
// live tenant/recipient/expiry checks at each request. No driver details or DSNs
// are exposed on failure. Runtime cannot own tables or inherit/SET ROLE to owners.
func RequireAgricultureAdmission(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return ErrAgricultureAdmissionUnavailable
	}
	var safe bool
	err := db.Raw(agricultureAdmissionReadinessSQL).Scan(&safe).Error
	if err != nil || !safe {
		return ErrAgricultureAdmissionUnavailable
	}
	return nil
}
