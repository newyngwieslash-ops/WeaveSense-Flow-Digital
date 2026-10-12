// Minimal DB abstraction over node-postgres (production) or PGlite (tests).
import { readFileSync, readdirSync } from "node:fs";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";

export interface Q {
  query<T = any>(sql: string, params?: unknown[]): Promise<{ rows: T[]; rowCount?: number | null; affectedRows?: number }>;
}
export interface Db extends Q {
  tx<T>(fn: (q: Q) => Promise<T>): Promise<T>;
  close(): Promise<void>;
}

export async function pgDb(url: string): Promise<Db> {
  const pg = (await import("pg")).default;
  const pool = new pg.Pool({ connectionString: url, max: 10 });
  return {
    query: (s, p) => pool.query(s, p as any[]) as any,
    async tx(fn) {
      const c = await pool.connect();
      try {
        await c.query("BEGIN");
        const r = await fn({ query: (s, p) => c.query(s, p as any[]) as any });
        await c.query("COMMIT");
        return r;
      } catch (e) {
        await c.query("ROLLBACK").catch(() => {});
        throw e;
      } finally {
        c.release();
      }
    },
    close: () => pool.end(),
  };
}

export async function pgliteDb(): Promise<Db> {
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
