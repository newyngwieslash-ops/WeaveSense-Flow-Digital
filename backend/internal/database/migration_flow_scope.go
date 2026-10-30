package database

import (
	"errors"
	"fmt"
	"gorm.io/gorm"
)

var ErrFlowScopeUnavailable = errors.New("Flow strict scope roles or fixed provider functions unavailable; apply bootstrap and migration 84")

// Runtime admission is separate from the migration-owner connection. A missing
// installation cannot silently select the historical empty-scope implementation.
func RequireFlowScope(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	var safe bool
	err := db.Raw(`SELECT current_user=session_user AND r.rolname='flow_platform_app' AND r.rolcanlogin
 AND NOT (r.rolsuper OR r.rolbypassrls OR r.rolinherit OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication)
 AND NOT pg_catalog.pg_has_role(current_user,'flow_platform_guard','MEMBER')
 AND NOT pg_catalog.pg_has_role(current_user,'flow_platform_system','MEMBER')
 AND NOT pg_catalog.pg_has_role(current_user,'flow_platform_migrate','MEMBER')
 AND NOT pg_catalog.pg_has_role(current_user,'flow_platform_admission','MEMBER')
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_database d WHERE d.datname=current_database() AND d.datdba=r.oid)
 AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
 WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f') AND c.relowner=r.oid)
 AND NOT has_table_privilege(current_user,'public.flow_provider_accounts','SELECT,INSERT,UPDATE,DELETE')
 AND NOT has_table_privilege(current_user,'public.flow_provider_account_periods','SELECT,INSERT,UPDATE,DELETE')
 AND NOT has_any_column_privilege(current_user,'public.flow_provider_accounts','SELECT,INSERT,UPDATE,REFERENCES')
 AND NOT has_any_column_privilege(current_user,'public.flow_provider_account_periods','SELECT,INSERT,UPDATE,REFERENCES')
 AND EXISTS(SELECT 1 FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_roles g ON g.oid=p.proowner
  WHERE p.oid=to_regprocedure('flow_system.provider_exists(text)') AND p.prosecdef
  AND p.proconfig @> ARRAY['search_path=pg_catalog']::text[] AND g.rolname='flow_platform_guard'
  AND NOT(g.rolcanlogin OR g.rolsuper OR g.rolbypassrls OR g.rolinherit)
  AND has_function_privilege(current_user,p.oid,'EXECUTE')
  AND NOT EXISTS(SELECT 1 FROM pg_catalog.aclexplode(p.proacl) a WHERE a.grantee=0 AND a.privilege_type='EXECUTE'))
 FROM pg_catalog.pg_roles r WHERE r.rolname=current_user`).Scan(&safe).Error
	if err != nil || !safe {
		return ErrFlowScopeUnavailable
	}
	return nil
}

// v84 is an explicit maintenance-window cutover. Historical migrations retain
// their original policy/backfill semantics; no runtime flag restores empty scope.
func migrateFlowStrictScope(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	var schema string
	if err := tx.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		return err
	}
	if schema == "public" {
		for _, role := range []string{"flow_platform_guard", "flow_platform_system"} {
			var safe bool
			if err := tx.Raw(`SELECT NOT (rolsuper OR rolbypassrls OR rolinherit OR rolcreatedb OR rolcreaterole OR rolreplication)
    AND rolcanlogin = ? FROM pg_catalog.pg_roles WHERE rolname=?`, role == "flow_platform_system", role).Scan(&safe).Error; err != nil || !safe {
				return fmt.Errorf("strict Flow scope requires pre-provisioned least-privilege role %s", role)
			}
		}
	}
	for _, table := range flowRLSTables {
		if !tx.Migrator().HasTable(table) {
			continue
		}
		for _, sql := range []string{
			"DROP POLICY IF EXISTS tenant_isolation ON " + table,
			"CREATE POLICY tenant_isolation ON " + table + " USING (" + agricultureTenantPolicy + ") WITH CHECK (" + agricultureTenantPolicy + ")",
		} {
			if err := tx.Exec(sql).Error; err != nil {
				return err
			}
		}
		if schema == "public" {
			for _, sql := range []string{"DROP POLICY IF EXISTS flow_migration_maintenance ON " + table, "CREATE POLICY flow_migration_maintenance ON " + table + " TO flow_platform_migrate USING (true) WITH CHECK (true)"} {
				if err := tx.Exec(sql).Error; err != nil {
					return err
				}
			}
		}
	}
	for _, table := range flowSystemOnlyTables {
		if !tx.Migrator().HasTable(table) {
			continue
		}
		for _, sql := range []string{"DROP POLICY IF EXISTS tenant_isolation ON " + table, "CREATE POLICY tenant_isolation ON " + table + " USING (false) WITH CHECK (false)"} {
			if err := tx.Exec(sql).Error; err != nil {
				return err
			}
		}
		if schema == "public" {
			for _, sql := range []string{"CREATE POLICY flow_provider_guard ON " + table + " TO flow_platform_guard,flow_platform_migrate USING (true) WITH CHECK (true)", "REVOKE ALL ON " + table + " FROM flow_platform_app,flow_platform_system"} {
				if err := tx.Exec(sql).Error; err != nil {
					return err
				}
			}
		}
	}
	if schema != "public" {
		return nil
	}
	return installFlowProviderGuard(tx)
}

func installFlowProviderGuard(tx *gorm.DB) error {
	for _, sql := range []string{
		"CREATE SCHEMA IF NOT EXISTS flow_system",
		"REVOKE ALL ON SCHEMA flow_system FROM PUBLIC",
		"GRANT USAGE,CREATE ON SCHEMA flow_system TO flow_platform_guard",
		"GRANT USAGE ON SCHEMA public TO flow_platform_guard",
		"GRANT USAGE ON SCHEMA flow_system TO flow_platform_app,flow_platform_system",
		"GRANT SELECT ON public.flow_provider_accounts TO flow_platform_guard",
		"GRANT UPDATE(id) ON public.flow_provider_accounts TO flow_platform_guard",
		"GRANT SELECT,INSERT,UPDATE ON public.flow_provider_account_periods TO flow_platform_guard",
		flowProviderExistsSQL,
		"REVOKE ALL ON FUNCTION flow_system.provider_exists(text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION flow_system.provider_exists(text) TO flow_platform_app",
		"ALTER FUNCTION flow_system.provider_exists(text) OWNER TO flow_platform_guard",
		flowProviderTriggerSQL,
		"REVOKE ALL ON FUNCTION flow_system.reservation_provider_transition() FROM PUBLIC",
		"CREATE TRIGGER flow_reservation_provider_transition BEFORE INSERT OR UPDATE OR DELETE ON public.flow_budget_reservations FOR EACH ROW EXECUTE FUNCTION flow_system.reservation_provider_transition()",
		"ALTER FUNCTION flow_system.reservation_provider_transition() OWNER TO flow_platform_guard",
		"GRANT SELECT ON public.flow_tenants,public.flow_memberships,public.flow_workspaces,public.users TO flow_platform_guard",
		"GRANT SELECT ON public.flow_invites TO flow_platform_guard",
		"CREATE POLICY flow_legacy_admission_guard ON public.flow_invites FOR SELECT TO flow_platform_guard USING (product='')",
		flowUserTenantResolverSQL,
		"REVOKE ALL ON FUNCTION flow_system.resolve_user_tenants(text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION flow_system.resolve_user_tenants(text) TO flow_platform_app",
		"ALTER FUNCTION flow_system.resolve_user_tenants(text) OWNER TO flow_platform_guard",
		flowLegacyInviteResolverSQL,
		"REVOKE ALL ON FUNCTION flow_system.resolve_legacy_invite(text,text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION flow_system.resolve_legacy_invite(text,text) TO flow_platform_app",
		"ALTER FUNCTION flow_system.resolve_legacy_invite(text,text) OWNER TO flow_platform_guard",
		"REVOKE CREATE ON SCHEMA flow_system FROM flow_platform_guard",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			return fmt.Errorf("install fixed provider boundary: %w", err)
		}
	}
	return nil
}

const flowProviderExistsSQL = `CREATE OR REPLACE FUNCTION flow_system.provider_exists(account text) RETURNS boolean
 LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $provider$
 BEGIN
  IF coalesce(current_setting('app.tenant_ids',true),'')='' THEN
   RAISE EXCEPTION 'provider lookup requires tenant context' USING ERRCODE='42501';
  END IF;
  RETURN EXISTS(SELECT 1 FROM public.flow_provider_accounts p WHERE p.id=account);
 END; $provider$`

// Only a real reservation transition can mutate global provider totals. There
// is no callable amount/period mutation API and no second settlement ledger.
const flowProviderTriggerSQL = `CREATE OR REPLACE FUNCTION flow_system.reservation_provider_transition() RETURNS trigger
 LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $provider$
 DECLARE budget bigint; state text; target_period text; changed integer;
 BEGIN
  IF TG_OP='DELETE' THEN
   IF session_user<>'flow_platform_migrate' THEN RAISE EXCEPTION 'reservation deletion requires migration maintenance' USING ERRCODE='42501'; END IF;
   RETURN OLD;
  END IF;
  IF NEW.tenant_id='' OR NOT coalesce(NEW.tenant_id=ANY(string_to_array(current_setting('app.tenant_ids',true),',')),false) THEN
   RAISE EXCEPTION 'reservation requires matching tenant context' USING ERRCODE='42501';
  END IF;
  IF TG_OP='UPDATE' THEN
   IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.account_id IS DISTINCT FROM OLD.account_id
    OR NEW.provider_account_id IS DISTINCT FROM OLD.provider_account_id OR NEW.reserved_micro IS DISTINCT FROM OLD.reserved_micro
    OR NEW.created_at IS DISTINCT FROM OLD.created_at OR NEW.parent_id IS DISTINCT FROM OLD.parent_id
    OR NEW.price_version IS DISTINCT FROM OLD.price_version THEN
    RAISE EXCEPTION 'reservation financial identity is immutable' USING ERRCODE='23514';
   END IF;
  END IF;
  IF coalesce(NEW.provider_account_id,'')='' THEN RETURN NEW; END IF;
  target_period:=to_char(NEW.created_at AT TIME ZONE 'UTC','YYYY-MM');
  IF TG_OP='INSERT' THEN
   IF NEW.reserved_micro IS NULL OR NEW.reserved_micro<=0 OR NEW.status IS DISTINCT FROM 'reserved' OR NEW.created_at IS NULL THEN
    RAISE EXCEPTION 'invalid provider reservation' USING ERRCODE='23514';
   END IF;
   SELECT p.monthly_budget_micro,p.status INTO budget,state FROM public.flow_provider_accounts p WHERE p.id=NEW.provider_account_id FOR SHARE;
   IF NOT FOUND THEN
    RAISE EXCEPTION 'provider disappeared after lookup' USING ERRCODE='23514';
   END IF;
   IF state='suspended' THEN RAISE EXCEPTION 'flow_provider_budget_exhausted' USING ERRCODE='P0001'; END IF;
   INSERT INTO public.flow_provider_account_periods(account_id,period,reserved_micro,consumed_micro,updated_at)
    VALUES(NEW.provider_account_id,target_period,0,0,NEW.created_at) ON CONFLICT DO NOTHING;
   UPDATE public.flow_provider_account_periods p SET reserved_micro=p.reserved_micro+NEW.reserved_micro,updated_at=NEW.updated_at
    WHERE p.account_id=NEW.provider_account_id AND p.period=target_period
     AND (coalesce(budget,0)<=0 OR p.reserved_micro+p.consumed_micro+NEW.reserved_micro<=budget);
   GET DIAGNOSTICS changed=ROW_COUNT;
   IF changed<>1 THEN RAISE EXCEPTION 'flow_provider_budget_exhausted' USING ERRCODE='P0001'; END IF;
   RETURN NEW;
  END IF;
  IF OLD.status IN ('settled','released') THEN
   IF NEW.status IS DISTINCT FROM OLD.status OR NEW.consumed_micro IS DISTINCT FROM OLD.consumed_micro THEN
    RAISE EXCEPTION 'closed provider reservation cannot reopen' USING ERRCODE='23514';
   END IF;
   RETURN NEW;
  END IF;
  IF NEW.status IN ('settled','released') THEN
   IF OLD.status NOT IN ('reserved','reconciliation_required') OR NEW.consumed_micro IS NULL OR NEW.consumed_micro<0 OR NEW.consumed_micro>NEW.reserved_micro THEN
    RAISE EXCEPTION 'invalid provider settlement' USING ERRCODE='23514';
   END IF;
   UPDATE public.flow_provider_account_periods p
    SET reserved_micro=CASE WHEN p.reserved_micro>NEW.reserved_micro THEN p.reserved_micro-NEW.reserved_micro ELSE 0 END,
        consumed_micro=p.consumed_micro+NEW.consumed_micro,updated_at=NEW.updated_at
    WHERE p.account_id=NEW.provider_account_id AND p.period=target_period;
   GET DIAGNOSTICS changed=ROW_COUNT;
   IF changed<>1 THEN RAISE EXCEPTION 'provider period missing' USING ERRCODE='23514'; END IF;
  END IF;
  RETURN NEW;
 END; $provider$`

const flowUserTenantResolverSQL = `CREATE OR REPLACE FUNCTION flow_system.resolve_user_tenants(actor text)
 RETURNS TABLE(tenant_id text) LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $scope$
 SELECT m.tenant_id::text FROM public.flow_memberships m
 JOIN public.flow_tenants t ON t.id=m.tenant_id AND t.status='active'
 JOIN public.users u ON u.id=m.user_id AND u.status='active'
 WHERE m.user_id=$1 AND m.status='active' AND $1<>''
 UNION
 SELECT 'tn_u_'||u.id FROM public.users u WHERE u.id=$1 AND u.status='active' AND $1<>''
 AND NOT EXISTS(SELECT 1 FROM public.flow_tenants t WHERE t.id='tn_u_'||u.id)
 $scope$`

const flowLegacyInviteResolverSQL = `CREATE OR REPLACE FUNCTION flow_system.resolve_legacy_invite(token_hash text,actor text)
 RETURNS TABLE(tenant_id text,workspace_id text) LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $scope$
 SELECT i.tenant_id::text,i.workspace_id::text FROM public.flow_invites i
 JOIN public.flow_tenants t ON t.id=i.tenant_id AND t.status='active'
 JOIN public.flow_workspaces w ON w.id=i.workspace_id AND w.tenant_id=i.tenant_id AND w.product=''
 JOIN public.users u ON u.id=$2 AND u.status='active' AND u.email_verified_at IS NOT NULL
 WHERE $1~'^[0-9a-f]{64}$' AND i.token_hash=$1 AND i.product='' AND i.accepted_at IS NULL
 AND i.revoked_at IS NULL AND i.expires_at>CURRENT_TIMESTAMP AND btrim(i.email)<>''
 AND lower(btrim(u.email))=lower(btrim(i.email))
 $scope$`
