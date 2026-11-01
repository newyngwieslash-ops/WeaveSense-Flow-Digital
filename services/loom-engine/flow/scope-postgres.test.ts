import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import pg from "pg";
import { pgDb, pgMaintenanceDb, migrate, withTenant, type Q } from "./db.ts";
import { pgTenantScanner, tenantDispatch } from "./tenant-dispatch.ts";
import { Engine } from "./engine.ts";
import { Reports } from "./reports.ts";
import { derivedTick } from "./gaps9.ts";
import { createApiServer } from "./server.ts";
import { sign } from "./auth.ts";
import { metricsSnapshot } from "./metrics-snapshot.ts";

const migrationURL = process.env.LOOM_SCOPE_TEST_MIGRATION_URL;
const appURL = process.env.LOOM_SCOPE_TEST_APP_URL;
const systemURL = process.env.LOOM_SCOPE_TEST_SYSTEM_URL;
const skip = !migrationURL || !appURL || !systemURL ? "explicit isolated PostgreSQL fixture required" : false;

test("DT018 real PostgreSQL ordinary scope and restricted background dispatch", { skip }, async (t) => {
  const migration = await pgMaintenanceDb(migrationURL!);
  const db = await pgDb(appURL!);
  const raw = new pg.Pool({ connectionString: appURL, max: 1 });
  const system = new pg.Pool({ connectionString: systemURL, max: 1 });
  const scanner = await pgTenantScanner(systemURL!);
  const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
  const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
  let clock = new Date("2026-10-30T00:00:00Z");
  const engine = new Engine(db, async () => [{ external_id: "1", title: "fixture", body: "scope migration fixture evidence", url: "https://fixture.invalid/1", published_at: clock.toISOString() }], () => clock);
  let sourceA: any;
  try {
    await t.test("migration applies and repeats without changing ledger", async () => {
      assert.ok((await migrate(migration)).includes("0019_scope_required.sql"));
      assert.deepEqual(await migrate(migration), []);
      const install = readFileSync(new URL("./install-system-role.sql", import.meta.url), "utf8");
      await migration.query(install);
      await migration.query(install);
      await assert.rejects(pgDb(migrationURL!), /runtime database role not permitted/);
      await assert.rejects(pgDb(systemURL!), /runtime database role not permitted/);
      await assert.rejects(pgTenantScanner(migrationURL!), /system database role not permitted/);
      await assert.rejects(pgTenantScanner(appURL!), /system database role not permitted/);
      const roles = await raw.query("SELECT rolsuper,rolbypassrls,rolcreaterole FROM pg_roles WHERE rolname=current_user");
      assert.deepEqual(roles.rows[0], { rolsuper: false, rolbypassrls: false, rolcreaterole: false });
      assert.equal((await raw.query("SELECT pg_has_role(current_user,'flow_intel_guard','MEMBER') AS member")).rows[0].member, false);
      assert.equal((await system.query("SELECT pg_has_role(current_user,'flow_intel_guard','MEMBER') AS member")).rows[0].member, false);
    });
    await t.test("two scoped tenants create sources and ordinary unscoped access denies", async () => {
      sourceA = await withTenant(A.tenant_id, () => engine.createSource(A, { url: "https://fixture.invalid/feed" }));
      await withTenant(B.tenant_id, () => engine.createSource(B, { url: "https://fixture.invalid/feed" }));
      await assert.rejects(db.query("SELECT * FROM flow_sources"), /tenant scope required/);
      assert.equal((await raw.query("SELECT count(*)::int AS n FROM flow_sources")).rows[0].n, 0);
      await assert.rejects(raw.query("INSERT INTO flow_sources(id,tenant_id,workspace_id,kind,url,title) VALUES('bad','tn_a','ws_a','rss','x','x')"), /row-level security/);
      await withTenant(A.tenant_id, async () => {
        assert.equal((await engine.listSources(A)).length, 1);
        assert.equal((await engine.listSources(B)).length, 0);
        await assert.rejects(engine.createSource(B, { url: "https://fixture.invalid/forbidden" }), /row-level security/);
      });
    });
    await t.test("system scanner has only bounded tenant metadata and no table writes or content", async () => {
      assert.deepEqual(await scanner.scan("", 1), ["tn_a"]);
      assert.deepEqual(await scanner.scan("tn_a", 1), ["tn_b"]);
      await assert.rejects(scanner.scan("", 101), /bounds/);
      await assert.rejects(raw.query("SELECT * FROM public.flow_dispatch_tenants('', 100)"), /permission denied/);
      for (const sql of ["SELECT tenant_id FROM flow_sources", "SELECT title FROM flow_sources", "UPDATE flow_sources SET title='bad'", "SET ROLE flow_intel_guard", "SET ROLE flow_intel_migrate"]) {
        await assert.rejects(system.query(sql), /permission denied/);
      }
      await assert.rejects(withTenant("tn_a", () => scanner.scan("", 1)), /forbidden/);
      const tables = (await migration.query("SELECT tablename FROM pg_policies WHERE policyname='tenant_isolation' AND schemaname='public' ORDER BY tablename")).rows;
      assert.equal(tables.length, 27);
      for (const { tablename } of tables) {
        assert.match(tablename, /^flow_[a-z_]+$/);
        assert.equal((await raw.query(`SELECT count(*)::int AS n FROM public.${tablename}`)).rows[0].n, 0);
        await assert.rejects(system.query(`SELECT tenant_id FROM public.${tablename}`), /permission denied/);
      }
    });
    await t.test("pooled scope reset rollback and escaped transaction handles fail closed", async () => {
      const connection = await raw.connect();
      try {
        await connection.query("BEGIN");
        await connection.query("SELECT set_config('app.tenant_id','tn_a',true)");
        assert.equal((await connection.query("SELECT tenant_id FROM flow_sources")).rows[0].tenant_id, "tn_a");
        await connection.query("ROLLBACK");
      } finally { connection.release(); }
      assert.equal((await raw.query("SELECT * FROM flow_sources")).rows.length, 0);
      let escaped!: Q;
      await withTenant("tn_a", async () => {
        await db.tx(async q => { escaped = q; });
        await assert.rejects(escaped.query("SELECT * FROM flow_sources"), /expired/);
        await assert.rejects(db.tx(async q => { await q.query("SELECT 1/0"); }), /division by zero/);
      });
      await withTenant("tn_b", async () => {
        assert.equal((await db.query("SELECT tenant_id FROM flow_sources")).rows[0].tenant_id, "tn_b");
      });
      await assert.rejects(db.query("SELECT * FROM flow_sources"), /scope required/);
    });
    await t.test("real HTTP and Agent parameters cannot select system scope or pool", async () => {
      const key = "fixture-only-signing-key-".repeat(2);
      const metrics = metricsSnapshot(db, scanner);
      const server = createApiServer(engine, { signingKey: key, audience: "loom-engine", metricsToken: "fixture-ops-only", metrics: metrics.read });
      await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
      const base = `http://127.0.0.1:${(server.address() as any).port}`;
      const token = sign(key, { iss: "flow-platform", aud: "loom-engine", ...A,
        actor: { type: "user", id: "fixture-user" }, scopes: ["intel.sources.read", "intel.sources.write"], policy_version: "policy_v1" });
      try {
        const headers = { authorization: `Bearer ${token}`, "content-type": "application/json", "x-db-role": "flow_intel_system", "x-tenant-id": "tn_b" };
        const response = await fetch(base + "/v1/sources?tenant_id=tn_b&role=system&pool=system", { headers });
        assert.equal(response.status, 200);
        const body = await response.json() as any;
        assert.deepEqual(body.data.map((s: any) => s.id), [sourceA.id]);
        const created = await fetch(base + "/v1/sources", { method: "POST", headers,
          body: JSON.stringify({ url: "https://fixture.invalid/http", tenant_id: "tn_b", role: "system", pool: "system" }) });
        assert.equal(created.status, 201);
        await withTenant("tn_b", async () => assert.equal((await engine.listSources(B)).length, 1));
        assert.equal((await fetch(base + "/v1/system/scan", { headers })).status, 404);
        assert.equal((await fetch(base + "/v1/sources?role=system")).status, 401);
        assert.equal((await fetch(base + "/metrics", { headers })).status, 401, "tenant/Agent token cannot read global metrics");
        const opsHeaders = { authorization: "Bearer fixture-ops-only" };
        assert.equal((await fetch(base + "/metrics", { headers: opsHeaders })).status, 503);
        await metrics.refresh();
        const responseMetrics = await fetch(base + "/metrics?role=system&tenant_id=tn_b", { headers: opsHeaders });
        assert.equal(responseMetrics.status, 200);
        const metricText = await responseMetrics.text();
        assert.match(metricText, /flow_sources\{kind="rss",status="active"\} 3/);
        assert.ok(!metricText.includes("tn_a") && !metricText.includes("tn_b") && !metricText.includes("fixture.invalid"));
      } finally { server.closeAllConnections(); await new Promise<void>(resolve => server.close(() => resolve())); }
    });
    await t.test("scoped collection claim commit cursor and ordered at least once outbox", async () => {
      await withTenant("tn_a", () => engine.requestCollection(A, "fixture-collection", { source_ids: [sourceA.id] }));
      const dispatch = tenantDispatch(scanner, 100);
      await dispatch(() => engine.workOnce("fixture-worker"));
      await withTenant("tn_a", async () => {
        assert.equal((await engine.listDocuments(A, {})).length, 1);
        assert.equal((await db.query("SELECT count(*)::int AS n FROM flow_outbox")).rows[0].n > 0, true);
        await engine.dispatchOutbox(async () => { throw new Error("fixture delivery failed"); });
        assert.ok((await db.query("SELECT attempts FROM flow_outbox ORDER BY sequence LIMIT 1")).rows[0].attempts > 0);
        const sent: number[] = [];
        await engine.dispatchOutbox(async e => { assert.equal(e.tenant_id, "tn_a"); sent.push(e.sequence); });
        assert.ok(sent.length > 0);
        assert.deepEqual(sent, [...sent].sort((a, b) => a - b));
        assert.equal(await engine.dispatchOutbox(async () => assert.fail("delivered event repeated")), 0);
      });
      await withTenant("tn_b", async () => assert.equal((await engine.listDocuments(B, {})).length, 0));
    });
    await t.test("scheduler report and enrichment loops use explicit tenant scopes", async () => {
      const reports = new Reports(engine);
      await withTenant("tn_a", async () => {
        await engine.createSchedule(A, "fixture-actor", "policy_v1", { source_ids: [sourceA.id], interval_seconds: 60, start_at: clock.toISOString() });
        const preview = await reports.previewBrief(A, { query: "fixture", source_ids: [sourceA.id], cron: "* * * * *", timezone: "UTC" });
        await reports.applyBrief(A, "fixture-actor", "policy_v1", { preview_id: preview.preview_id, approval_id: "fixture-approved", idempotency_key: "brief-fixture" });
      });
      const dispatch = tenantDispatch(scanner, 100);
      await dispatch(() => engine.schedulerTick());
      await dispatch(() => engine.workOnce("fixture-scheduled-worker"));
      clock = new Date("2026-10-30T00:01:01Z");
      await dispatch(async () => {
        await engine.reconcile();
        await engine.closeBudgets();
        await engine.retryMedia();
        await engine.asrTick();
        await engine.ocrTick();
        await reports.briefTick();
        await derivedTick(db, null);
      });
      await withTenant("tn_a", async () => {
        assert.ok((await db.query("SELECT count(*)::int AS n FROM flow_collection_runs WHERE schedule_id IS NOT NULL")).rows[0].n > 0);
        assert.equal((await reports.listReports(A)).length, 1);
        assert.equal(await reports.briefTick(), 0);
        assert.ok((await db.query("SELECT count(*)::int AS n FROM flow_derived_jobs")).rows[0].n > 0);
      });
      await withTenant("tn_b", async () => assert.equal((await reports.listReports(B)).length, 0));
    });
    await t.test("media retry writes real scoped rows and transactional outbox with fixture transport", async () => {
      const mediaEngine = new Engine(db, async () => ({ items: [{ external_id: "11", title: "fixture photo", body: "fixture", url: "https://fixture.invalid/11", published_at: null,
        media: [{ url: "telegram://fixture_channel/11", ref: "telegram://fixture_channel/11", type: "image", status: "failed", reason: "fixture", attempts: 1 }] }], collector: "telethon", mode: "fixture" }), () => clock);
      let calls = 0;
      mediaEngine.connectorEnv = { telegramWorkerUrl: "http://fixture.invalid", telegramMediaToken: "fixture-only", fetchImpl: (async (_url: any, init: any) => {
        calls++;
        const body = JSON.parse(init.body);
        assert.equal(body.tenant_id, "tn_a");
        assert.equal(body.workspace_id, "ws_a");
        return new Response(JSON.stringify({ object_key: `tenants/tn_a/workspaces/ws_a/telegram/${body.channel}/${body.message_id}`, content_type: "image/png" }));
      }) as any };
      await withTenant("tn_a", async () => {
        const source = await mediaEngine.createSource(A, { kind: "telegram", config: { handle: "fixture_channel" } });
        await mediaEngine.requestCollection(A, "media-fixture", { source_ids: [source.id] });
        await mediaEngine.workOnce("fixture-media-worker");
      });
      const dispatch = tenantDispatch(scanner, 100);
      await dispatch(() => mediaEngine.retryMedia());
      assert.equal(calls, 1);
      await dispatch(() => mediaEngine.retryMedia());
      assert.equal(calls, 1);
      await withTenant("tn_a", async () => assert.equal((await db.query("SELECT count(*)::int AS n FROM flow_outbox WHERE type='media.stored'")).rows[0].n, 1));
      await withTenant("tn_b", async () => assert.equal((await db.query("SELECT count(*)::int AS n FROM flow_outbox WHERE type='media.stored'")).rows[0].n, 0));
    });
  } finally { await Promise.all([migration.close(), db.close(), raw.end(), system.end(), scanner.close()]); }
});
