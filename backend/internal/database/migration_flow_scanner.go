package database

import "gorm.io/gorm"

func migrateFlowSystemScanner(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	var schema string
	if err := tx.Raw("SELECT current_schema()").Scan(&schema).Error; err != nil {
		return err
	}
	if schema != "public" {
		return nil
	}
	for _, table := range []string{"flow_budget_reservations", "flow_notification_deliveries", "flow_model_tasks", "flow_command_outbox", "flow_external_waits", "edu_math_jobs"} {
		columns := "tenant_id,status"
		if table == "flow_model_tasks" {
			columns += ",notified_at"
		}
		if table == "edu_math_jobs" {
			columns += ",settlement"
		}
		for _, sql := range []string{"GRANT SELECT (" + columns + ") ON public." + table + " TO flow_platform_guard", "CREATE POLICY flow_background_metadata ON public." + table + " FOR SELECT TO flow_platform_guard USING (true)"} {
			if err := tx.Exec(sql).Error; err != nil {
				return err
			}
		}
	}
	for _, sql := range []string{
		"GRANT CREATE ON SCHEMA flow_system TO flow_platform_guard",
		"GRANT SELECT (id,tenant_id,status) ON public.edu_classes TO flow_platform_guard",
		"CREATE POLICY flow_edu_admission ON public.edu_classes FOR SELECT TO flow_platform_guard USING (true)",
		"GRANT SELECT (id,tenant_id,class_id,secret_hash,expires_at,max_uses,uses,status) ON public.edu_class_invites TO flow_platform_guard",
		"CREATE POLICY flow_edu_admission ON public.edu_class_invites FOR SELECT TO flow_platform_guard USING (true)",
		"GRANT SELECT (id,tenant_id,class_id,secret_hash,expires_at,state) ON public.edu_roster_entries TO flow_platform_guard",
		"CREATE POLICY flow_edu_admission ON public.edu_roster_entries FOR SELECT TO flow_platform_guard USING (true)",
		flowEduInviteResolverSQL,
		"REVOKE ALL ON FUNCTION flow_system.resolve_edu_class_invite(text,text,text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION flow_system.resolve_edu_class_invite(text,text,text) TO flow_platform_app",
		"ALTER FUNCTION flow_system.resolve_edu_class_invite(text,text,text) OWNER TO flow_platform_guard",
		flowEduRosterResolverSQL,
		"REVOKE ALL ON FUNCTION flow_system.resolve_edu_roster_claim(text,text,text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION flow_system.resolve_edu_roster_claim(text,text,text) TO flow_platform_app",
		"ALTER FUNCTION flow_system.resolve_edu_roster_claim(text,text,text) OWNER TO flow_platform_guard",
		"GRANT SELECT (tenant_id,user_id,status,expires_at) ON public.edu_role_bindings TO flow_platform_guard",
		"CREATE POLICY flow_edu_admission ON public.edu_role_bindings FOR SELECT TO flow_platform_guard USING (true)",
		flowEduUserTenantsSQL,
		"REVOKE ALL ON FUNCTION flow_system.resolve_edu_user_tenants(text) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION flow_system.resolve_edu_user_tenants(text) TO flow_platform_app",
		"ALTER FUNCTION flow_system.resolve_edu_user_tenants(text) OWNER TO flow_platform_guard",
		flowPendingTenantsSQL,
		"REVOKE ALL ON FUNCTION flow_system.pending_tenants(text,text,integer) FROM PUBLIC,flow_platform_app",
		"GRANT EXECUTE ON FUNCTION flow_system.pending_tenants(text,text,integer) TO flow_platform_system",
		"ALTER FUNCTION flow_system.pending_tenants(text,text,integer) OWNER TO flow_platform_guard",
		"REVOKE CREATE ON SCHEMA flow_system FROM flow_platform_guard",
	} {
		if err := tx.Exec(sql).Error; err != nil {
			return err
		}
	}
	return nil
}

// actor comes only from a trusted platform session or the verified artifact
// signature. This returns no roster/role payload and grants no business access.
const flowEduUserTenantsSQL = `CREATE FUNCTION flow_system.resolve_edu_user_tenants(actor text)
 RETURNS TABLE(tenant_id text) LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $edu$
 SELECT DISTINCT b.tenant_id::text FROM public.edu_role_bindings b
 JOIN public.users u ON u.id=b.user_id AND u.status='active'
 JOIN public.flow_tenants t ON t.id=b.tenant_id AND t.status='active'
 WHERE b.user_id=actor AND actor<>'' AND b.status='active'
 AND (b.expires_at IS NULL OR b.expires_at>now()); $edu$`

const flowEduInviteResolverSQL = `CREATE FUNCTION flow_system.resolve_edu_class_invite(invite_id text,provided_hash text,actor text)
 RETURNS TABLE(tenant_id text) LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $invite$
 SELECT i.tenant_id::text FROM public.edu_class_invites i
 JOIN public.flow_tenants t ON t.id=i.tenant_id AND t.status='active'
 JOIN public.edu_classes c ON c.id=i.class_id AND c.tenant_id=i.tenant_id AND c.status='active'
 WHERE i.id=invite_id AND i.secret_hash=provided_hash AND provided_hash<>''
 AND i.status='active' AND i.expires_at>now()
 AND EXISTS(SELECT 1 FROM public.users u WHERE u.id=actor AND u.status='active'); $invite$`

const flowEduRosterResolverSQL = `CREATE FUNCTION flow_system.resolve_edu_roster_claim(entry_id text,provided_hash text,actor text)
 RETURNS TABLE(tenant_id text) LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $roster$
 SELECT r.tenant_id::text FROM public.edu_roster_entries r
 JOIN public.flow_tenants t ON t.id=r.tenant_id AND t.status='active'
 JOIN public.edu_classes c ON c.id=r.class_id AND c.tenant_id=r.tenant_id AND c.status='active'
 WHERE r.id=entry_id AND r.secret_hash=provided_hash AND provided_hash<>''
 AND r.state='pending' AND r.expires_at>now()
 AND EXISTS(SELECT 1 FROM public.users u WHERE u.id=actor AND u.status='active'); $roster$`

// Only tenant IDs, no content, callbacks, recipients, prompts or credentials.
// The calling role has no base-table access. Pagination never pins execution to
// the first 100 tenants; each worker maintains its own bounded keyset cursor.
const flowPendingTenantsSQL = `CREATE FUNCTION flow_system.pending_tenants(scan_kind text,after_tenant text,batch_size integer)
 RETURNS TABLE(tenant_id text) LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $scan$
 BEGIN
  IF scan_kind NOT IN ('billing','notifications','model_tasks','commands','external_waits','edu_jobs','edu_ledger')
   OR scan_kind IS NULL OR after_tenant IS NULL OR length(after_tenant)>160 OR batch_size IS NULL OR batch_size<1 OR batch_size>100 THEN
   RAISE EXCEPTION 'invalid fixed background scan' USING ERRCODE='22023';
  END IF;
  RETURN QUERY SELECT DISTINCT source.tenant_id::text FROM (
   SELECT r.tenant_id FROM public.flow_budget_reservations r WHERE scan_kind='billing' AND r.status IN ('reserved','reconciliation_required')
   UNION ALL SELECT n.tenant_id FROM public.flow_notification_deliveries n WHERE scan_kind='notifications' AND n.status IN ('pending','retry_wait','sending')
   UNION ALL SELECT m.tenant_id FROM public.flow_model_tasks m WHERE scan_kind='model_tasks' AND (m.status IN ('queued','retry_wait','running','reconciliation_required') OR (m.status IN ('succeeded','failed') AND m.notified_at IS NULL))
   UNION ALL SELECT c.tenant_id FROM public.flow_command_outbox c WHERE scan_kind='commands' AND c.status IN ('pending','retry_wait','dispatching')
   UNION ALL SELECT w.tenant_id FROM public.flow_external_waits w WHERE scan_kind='external_waits' AND w.status IN ('waiting','ready')
   UNION ALL SELECT j.tenant_id FROM public.edu_math_jobs j WHERE scan_kind='edu_ledger' OR (scan_kind='edu_jobs' AND (j.status IN ('queued','running','reconciliation_required') OR j.settlement='open'))
  ) source WHERE source.tenant_id<>'' AND source.tenant_id>after_tenant ORDER BY source.tenant_id::text LIMIT batch_size;
 END; $scan$`
