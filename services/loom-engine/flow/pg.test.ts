// Real-Postgres tests (RLS as non-superuser, backup/restore consistency).
// Run with FLOW_TEST_PG_ADMIN_URL=postgres://<superuser>@host/db and FLOW_TEST_PG_APP_ROLE=<non-superuser role:password>.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { pgDb, migrate, withTenant } from "./db.ts";
import { Engine } from "./engine.ts";

const admin = process.env.FLOW_TEST_PG_ADMIN_URL ?? "";
const [appUser, appPass] = (process.env.FLOW_TEST_PG_APP_ROLE ?? "").split(":");
const skip = !admin || !appUser ? "FLOW_TEST_PG_ADMIN_URL / FLOW_TEST_PG_APP_ROLE not set" : false;
const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
const backupDir = join(dirname(fileURLToPath(import.meta.url)), "../../../deploy/flow/backup");

const sql = (url: string, s: string) => execFileSync("psql", [url, "-v", "ON_ERROR_STOP=1", "-Atc", s], { encoding: "utf8" });
function freshDb(name: string) {
  sql(admin, `DROP DATABASE IF EXISTS ${name}`);
  sql(admin, `CREATE DATABASE ${name} OWNER ${appUser}`);
  const u = new URL(admin); u.pathname = `/${name}`;
  const app = new URL(u); app.username = appUser; app.password = appPass;
  return { adminUrl: u.toString(), appUrl: app.toString() };
}

test("RLS: tenant context hides and blocks other tenants' rows (non-superuser role)", { skip }, async () => {
  const { appUrl } = freshDb("flow_rls_test");
  const db = await pgDb(appUrl);
  try {
    await migrate(db);
    const eng = new Engine(db, async () => []);
    const sa = await eng.createSource(A, { url: "https://a.example/f" });
    await eng.createSource(B, { url: "https://b.example/f" });
    // System context (workers) sees all tenants.
    assert.equal((await db.query(`SELECT count(*)::int AS n FROM flow_sources`)).rows[0].n, 2);
    await withTenant("tn_a", async () => {
      assert.equal((await db.query(`SELECT count(*)::int AS n FROM flow_sources`)).rows[0].n, 1);
      // Even an application query that asks for tenant B gets nothing under tenant A's context.
      assert.equal((await eng.listSources(B)).length, 0);
      assert.equal((await eng.listSources(A))[0].id, sa.id);
      await assert.rejects(db.query(`INSERT INTO flow_sources (id, tenant_id, workspace_id, kind, url, title, config) VALUES ('x','tn_b','ws_b','rss','https://x','x','{}')`), /row-level security/);
      const u = await db.query(`UPDATE flow_sources SET status='revoked' WHERE tenant_id='tn_b'`);
      assert.equal(u.rowCount, 0);
    });
  } finally { await db.close(); }
});

test("A18: backup/restore dump → restore into an empty database → manifest and app reads match", { skip }, async () => {
  const src = freshDb("flow_backup_src");
  const dst = freshDb("flow_backup_dst");
  const db = await pgDb(src.appUrl);
  try {
    await migrate(db);
    const eng = new Engine(db, async () => [{ external_id: "1", title: "t", body: "battery storage tender awarded", url: "https://a/1", published_at: "2026-10-12T00:00:00Z" }]);
    const s = await eng.createSource(A, { url: "https://a.example/f" });
    await eng.requestCollection(A, "k1", { source_ids: [s.id] });
    while (await eng.workOnce("w"));
  } finally { await db.close(); }
  const out = mkdtempSync(join(tmpdir(), "flowbak-"));
  const dump = execFileSync(join(backupDir, "backup.sh"), [src.appUrl, out], { encoding: "utf8" }).trim();
  assert.ok(readdirSync(out).some((f) => f.endsWith(".manifest")));
  const r = execFileSync(join(backupDir, "restore.sh"), [dump, dst.appUrl], { encoding: "utf8" });
  assert.match(r, /restore verified/);
  const db2 = await pgDb(dst.appUrl);
  try {
    const eng2 = new Engine(db2, async () => []);
    const docs = await withTenant("tn_a", () => eng2.listDocuments(A, {}));
    assert.ok(Array.isArray(docs) ? docs.length >= 1 : (docs as any).items?.length >= 1);
    // Migrations are recorded, so a restored database is not re-migrated.
    assert.deepEqual(await migrate(db2), []);
  } finally { await db2.close(); }
});

test("RLS: HTTP API end-to-end on real Postgres as non-superuser", { skip }, async () => {
  const { createServer } = await import("node:http");
  const { sign } = await import("./auth.ts");
  const { handler } = await import("./server.ts");
  const { appUrl } = freshDb("flow_rls_api");
  const db = await pgDb(appUrl);
  const KEY = "k".repeat(32);
  const eng = new Engine(db, async () => [{ external_id: "1", title: "t", body: "grid storage", url: "https://a/1", published_at: "2026-10-12T00:00:00Z" }]);
  await migrate(db);
  const srv = createServer(handler(eng, { signingKey: KEY, audience: "loom-engine" }));
  await new Promise<void>((r) => srv.listen(0, r));
  const base = `http://127.0.0.1:${(srv.address() as any).port}`;
  const all = ["intel.sources.read", "intel.sources.write", "intel.collect", "intel.operations.read", "intel.documents.read"];
  const tok = (s: typeof A) => sign(KEY, { iss: "flow-platform", aud: "loom-engine", ...s, actor: { type: "user", id: "u" }, scopes: all, policy_version: "policy_v1" });
  const call = (m: string, p: string, s: typeof A, body?: unknown, idem?: string) =>
    fetch(base + p, { method: m, headers: { authorization: `Bearer ${tok(s)}`, "content-type": "application/json", ...(idem ? { "x-idempotency-key": idem } : {}) }, body: body ? JSON.stringify(body) : undefined });
  try {
    const src = (await (await call("POST", "/v1/sources", A, { url: "https://a.example/f" })).json()).data;
    const acc = await call("POST", "/v1/collection-runs", A, { source_ids: [src.id] }, "i1");
    assert.equal(acc.status, 202);
    while (await eng.workOnce("w"));
    const docsA = (await (await call("GET", "/v1/documents", A)).json()).data;
    assert.ok((docsA.items ?? docsA).length >= 1);
    const docsB = (await (await call("GET", "/v1/documents", B)).json()).data;
    assert.equal((docsB.items ?? docsB).length, 0);
    assert.equal((await call("POST", "/v1/collection-runs", B, { source_ids: [src.id] }, "i1")).status, 404);
  } finally { srv.close(); await db.close(); }
});
