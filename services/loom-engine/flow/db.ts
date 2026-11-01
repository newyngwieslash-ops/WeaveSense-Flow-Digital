// Minimal DB abstraction over node-postgres (production) or PGlite (tests).
import { readFileSync, readdirSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { AsyncLocalStorage } from "node:async_hooks";

/** Trusted request or dispatcher tenant. Missing is never a system capability. */
const tenantStore = new AsyncLocalStorage<string>();
export function validTenant(tenantId: unknown): tenantId is string {
  return typeof tenantId === "string" && /^[A-Za-z0-9_-]{1,128}$/.test(tenantId);
}
export const withTenant = <T>(tenantId: string, fn: () => Promise<T>) => {
  if (!validTenant(tenantId)) throw new Error("tenant scope required");
  if (currentTenant() && currentTenant() !== tenantId) throw new Error("tenant scope switch denied");
  return tenantStore.run(tenantId, fn);
};
export const currentTenant = () => tenantStore.getStore() ?? "";

export interface Q {
  query<T = any>(sql: string, params?: unknown[]): Promise<{ rows: T[]; rowCount?: number | null; affectedRows?: number }>;
}
export interface Db extends Q {
  tx<T>(fn: (q: Q) => Promise<T>): Promise<T>;
  close(): Promise<void>;
}

export async function pgDb(url: string): Promise<Db> {
  return connectPg(url, false);
}

/** Operator-only connection. No implicit app fallback; its DSN must have DDL rights.
 * RLS still applies. Only migration/explicit offline import entrypoints use this.
 */
export async function pgMaintenanceDb(url: string): Promise<Db> {
  if (!url) throw new Error("explicit maintenance database URL required");
  return connectPg(url, true);
}

async function connectPg(url: string, maintenance: boolean): Promise<Db> {
  if (!url) throw new Error("explicit database URL required");
  const pg = (await import("pg")).default;
  const pool = new pg.Pool({ connectionString: url, max: 10, connectionTimeoutMillis: 10_000 });
  const admit = async (q: Q) => {
    const result = await q.query(`SELECT current_user = session_user
      AND NOT (r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication)
      AND NOT EXISTS (SELECT 1 FROM pg_database d WHERE d.datname=current_database() AND d.datdba=r.oid)
      AND NOT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relowner=r.oid)
      AND NOT EXISTS (SELECT 1 FROM pg_roles privileged
        WHERE (privileged.rolsuper OR privileged.rolbypassrls OR privileged.rolcreaterole
          OR privileged.rolname IN ('flow_intel_guard','flow_intel_migrate','flow_intel_system'))
          AND pg_has_role(r.oid,privileged.oid,'MEMBER')) AS allowed
      FROM pg_roles r WHERE r.rolname=current_user`);
    if (result.rows[0]?.allowed !== true) throw new Error("runtime database role not permitted");
  };
  if (!maintenance) {
    try { await admit(pool as unknown as Q); }
    catch (error) { await pool.end(); throw error; }
  }
  const db: Db = {
    query: (s, p) => db.tx((q) => q.query(s, p)),
    async tx(fn) {
      const tenant = currentTenant();
      if (!maintenance && !validTenant(tenant)) throw new Error("tenant scope required");
      const c = await pool.connect();
      let active = true;
      try {
        if (!maintenance) await admit(c as unknown as Q);
        await c.query("BEGIN");
        // Explicitly overwrite even an inherited session GUC; SET LOCAL unwinds on both outcomes.
        await c.query("SELECT set_config('app.tenant_id', $1, true)", [tenant]);
        const r = await fn({ query: (s, p) => {
          if (!active || currentTenant() !== tenant) return Promise.reject(new Error("transaction scope expired"));
          return c.query(s, p as any[]) as any;
        } });
        active = false;
        await c.query("COMMIT");
        return r;
      } catch (e) {
        await c.query("ROLLBACK").catch(() => {});
        throw e;
      } finally {
        active = false;
        c.release();
      }
    },
    close: () => pool.end(),
  };
  return db;
}

export async function pgliteDb(): Promise<Db> {
  // Isolated fixture/admin adapter, NOT evidence of runtime role/RLS enforcement.
  const { PGlite } = await import("@electric-sql/pglite");
  const pg = new PGlite();
  let chain: Promise<unknown> = Promise.resolve();
  const q: Q = { query: (s, p) => pg.query(s, p as any[]) as any };
  return {
    ...q,
    tx(fn) {
      // PGlite is single-connection: serialize transactions.
      const run = chain.then(() => pg.transaction((t) => fn({ query: (s, p) => t.query(s, p as any[]) as any })));
      chain = run.catch(() => {});
      return run as any;
    },
    close: () => pg.close(),
  };
}

export const affected = (r: { rowCount?: number | null; affectedRows?: number }) => r.rowCount ?? r.affectedRows ?? 0;

export async function migrate(db: Db): Promise<string[]> {
  const dir = join(dirname(fileURLToPath(import.meta.url)), "migrations");
  await db.query("CREATE TABLE IF NOT EXISTS flow_schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())");
  const applied: string[] = [];
  for (const f of readdirSync(dir).filter((n) => n.endsWith(".sql")).sort()) {
    const done = await db.query("SELECT 1 FROM flow_schema_migrations WHERE name=$1", [f]);
    if (done.rows.length) continue;
    const sql = readFileSync(join(dir, f), "utf8");
    await db.tx(async (t) => {
      for (const stmt of sql.split(/;\s*\n/).map((s) => s.trim()).filter((s) => s && !/^(--[^\n]*\n?)+$/.test(s))) {
        await t.query(stmt);
      }
      await t.query("INSERT INTO flow_schema_migrations(name) VALUES ($1)", [f]);
    });
    applied.push(f);
  }
  return applied;
}
