import { currentTenant, validTenant, withTenant } from "./db.ts";

export interface TenantScanner {
  scan(after: string, limit: number): Promise<string[]>;
  close(): Promise<void>;
}

/** Separate system connection: fixed metadata-only operation, never exposed as Db. */
export async function pgTenantScanner(url: string): Promise<TenantScanner> {
  if (!url) throw new Error("LOOM_SYSTEM_DATABASE_URL required for background dispatch");
  const pg = (await import("pg")).default;
  const pool = new pg.Pool({ connectionString: url, max: 2, connectionTimeoutMillis: 10_000 });
  try {
    const role = await pool.query(`SELECT current_user = 'flow_intel_system' AND current_user=session_user
      AND NOT (r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication)
      AND NOT pg_has_role(current_user,'flow_intel_guard','MEMBER')
      AND NOT pg_has_role(current_user,'flow_intel_migrate','MEMBER')
      AND NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='public' AND c.relkind IN ('r','p') AND
          (c.relowner=r.oid OR has_table_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE')
           OR has_any_column_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))
      AS allowed FROM pg_roles r WHERE r.rolname=current_user`);
    if (role.rows[0]?.allowed !== true) throw new Error("system database role not permitted");
  } catch (error) { await pool.end(); throw error; }
  return {
    async scan(after, limit) {
      if (currentTenant()) throw new Error("system scan forbidden within tenant context");
      if ((after !== "" && !validTenant(after)) || !Number.isInteger(limit) || limit < 1 || limit > 100) {
        throw new Error("invalid tenant scan bounds");
      }
      const result = await pool.query("SELECT tenant_id FROM public.flow_dispatch_tenants($1, $2)", [after, limit]);
      const tenants = result.rows.map((row) => row.tenant_id);
      if (tenants.length > limit || tenants.some((t, i) => !validTenant(t) || t <= (i ? tenants[i - 1] : after))) {
        throw new Error("invalid tenant scan result");
      }
      return tenants;
    },
    close: () => pool.end(),
  };
}

/** A separate cursor per loop preserves bounded round-robin progress. The callback
 * uses ONLY the ordinary tenant pool, including claim/fencing and atomic outbox writes.
 * A failed tenant advances the cursor before propagation, so it cannot starve others.
 */
export function tenantDispatch(scanner: TenantScanner, pageSize = 50) {
  if (!Number.isInteger(pageSize) || pageSize < 1 || pageSize > 100) throw new Error("invalid tenant page size");
  let cursor = "";
  let running = false;
  return async (fn: () => Promise<unknown>) => {
    if (running || currentTenant()) throw new Error("tenant dispatch context invalid");
    running = true;
    try {
      const tenants = await scanner.scan(cursor, pageSize);
      if (!tenants.length) { cursor = ""; return; }
      for (const tenant of tenants) {
        cursor = tenant;
        await withTenant(tenant, fn);
      }
      if (tenants.length < pageSize) cursor = "";
    } finally { running = false; }
  };
}
