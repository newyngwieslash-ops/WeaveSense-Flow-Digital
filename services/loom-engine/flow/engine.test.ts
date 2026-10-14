import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate, type Db } from "./db.ts";
import { Engine, HttpError, type FeedItem } from "./engine.ts";
import { sign } from "./auth.ts";
import { handler } from "./server.ts";
import { createServer } from "node:http";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
const FEED = "https://example.com/feed.xml";
const KEY = "k".repeat(32);
let feed: FeedItem[] = [];
const fetcher = async () => feed;

async function setup() {
  const db: Db = await pgliteDb();
  await migrate(db);
  let now = new Date("2026-10-13T00:00:00Z");
  const eng = new Engine(db, fetcher, () => now);
  return { db, eng, setNow: (d: string) => { now = new Date(d); } };
}
const item = (id: string, body = "b", pub = "2026-10-12T00:00:00Z"): FeedItem => ({ external_id: id, title: "t" + id, body, url: "u" + id, published_at: pub });

test("migrations are idempotent", async () => {
  const { db } = await setup();
  assert.deepEqual(await migrate(db), []);
});

test("A01: two tenants, same feed — isolated documents", async () => {
  const { eng, db } = await setup();
  const sa = await eng.createSource(A, { url: FEED }), sb = await eng.createSource(B, { url: FEED });
  assert.notEqual(sa.id, sb.id);
  feed = [item("1"), item("2")];
  await eng.requestCollection(A, "k1", { source_ids: [sa.id] });
  await eng.requestCollection(B, "k1", { source_ids: [sb.id] }); // same key in other tenant is independent
  while (await eng.workOnce("w"));
  assert.equal((await eng.listDocuments(A, {})).length, 2);
  assert.equal((await eng.listDocuments(B, {})).length, 2);
  const ids = (await db.query(`SELECT DISTINCT tenant_id FROM flow_documents`)).rows.length;
  assert.equal(ids, 2);
  const docA = (await eng.listDocuments(A, {}))[0];
  assert.ok((await eng.listDocuments(B, {})).every((d: any) => d.id !== docA.id));
});

test("A02: guessed foreign IDs are rejected (404)", async () => {
  const { eng } = await setup();
  const sa = await eng.createSource(A, { url: FEED });
  feed = [item("1")];
  const op = await eng.requestCollection(A, "k", { source_ids: [sa.id] });
  await eng.workOnce("w");
  const doc = (await eng.listDocuments(A, {}))[0];
  await assert.rejects(eng.requestCollection(B, "x", { source_ids: [sa.id] }), (e: any) => e instanceof HttpError && e.status === 404);
  await assert.rejects(eng.createSchedule(B, "u", "p", { source_ids: [sa.id], interval_seconds: 3600 }), (e: any) => e.status === 404);
  await assert.rejects(eng.getOperation(B, op.operation_id), (e: any) => e.status === 404);
  await assert.rejects(eng.getRevision(B, doc.id, 1), (e: any) => e.status === 404);
  assert.equal((await eng.getRevision(A, doc.id, 1)).document_id, doc.id);
});

test("A03/A04: idempotent replay creates nothing new; same key different params → 409", async () => {
  const { eng, db } = await setup();
  const s1 = await eng.createSource(A, { url: FEED }), s2 = await eng.createSource(A, { url: FEED + "?2" });
  const r1 = await eng.requestCollection(A, "same", { source_ids: [s1.id] });
  const r2 = await eng.requestCollection(A, "same", { source_ids: [s1.id] });
  assert.equal(r2.operation_id, r1.operation_id); assert.equal(r2.replayed, true);
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_collection_runs`)).rows[0].n, 1);
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_outbox`)).rows[0].n, 1);
  await assert.rejects(eng.requestCollection(A, "same", { source_ids: [s2.id] }), (e: any) => e.status === 409);
});

test("A06: crash before commit does not advance cursor or write docs; retry after lease expiry succeeds", async () => {
  const { eng, db, setNow } = await setup();
  const s = await eng.createSource(A, { url: FEED });
  feed = [item("1", "b", "2026-10-12T05:00:00Z")];
  await eng.requestCollection(A, "k", { source_ids: [s.id] });
  const run = await eng.claim("w1", 60_000);
  await assert.rejects(eng.execute(run, { beforeCommit: () => { throw new Error("simulated crash"); } }));
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_documents`)).rows[0].n, 0);
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_source_cursors`)).rows[0].n, 0);
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_outbox WHERE type='collection.completed'`)).rows[0].n, 0);
  // A05: run still 'running' with live lease → not re-claimable; after expiry it is recovered.
  assert.equal(await eng.claim("w2"), null);
  setNow("2026-10-13T00:02:00Z");
  const again = await eng.claim("w2");
  assert.equal(again.id, run.id); assert.equal(again.attempts, 2);
  // A07: the old worker's late result is rejected by fencing.
  await assert.rejects(eng.execute(run), (e: any) => e.status === 409);
  const r = await eng.execute(again);
  assert.equal(r.status, "succeeded");
  const cur = (await db.query(`SELECT last_published_at FROM flow_source_cursors`)).rows[0];
  assert.equal(new Date(cur.last_published_at).toISOString(), "2026-10-12T05:00:00.000Z");
});

test("A05: queued job survives engine restart (new Engine instance) and completes", async () => {
  const { eng, db } = await setup();
  const s = await eng.createSource(A, { url: FEED });
  feed = [item("1")];
  const op = await eng.requestCollection(A, "k", { source_ids: [s.id] });
  const restarted = new Engine(db, fetcher);
  assert.ok(await restarted.workOnce("w-new"));
  assert.equal((await restarted.getOperation(A, op.operation_id)).status, "succeeded");
});

test("dedup and revisions: unchanged items skipped, changed items get a new revision", async () => {
  const { eng } = await setup();
  const s = await eng.createSource(A, { url: FEED });
  feed = [item("1"), item("2")];
  await eng.requestCollection(A, "k1", { source_ids: [s.id] }); await eng.workOnce("w");
  feed = [item("1"), item("2", "changed"), item("3")];
  await eng.requestCollection(A, "k2", { source_ids: [s.id] });
  const r: any = await eng.workOnce("w");
  assert.equal(r.items_new, 1); assert.equal(r.items_revised, 1);
  const d2 = (await eng.listDocuments(A, {})).find((d: any) => d.title === "t2");
  assert.equal(d2.current_revision, 2);
  assert.equal((await eng.getRevision(A, d2.id, 1)).body, "b"); // old revision still readable
});

test("scheduler: window key tenant+schedule+scheduled_at, ticks are idempotent, missed windows collapse", async () => {
  const { eng, db, setNow } = await setup();
  const s = await eng.createSource(A, { url: FEED });
  const sc = await eng.createSchedule(A, "user:u", "policy_v1", { source_ids: [s.id], interval_seconds: 3600, start_at: "2026-10-13T00:00:00Z" });
  assert.equal(await eng.schedulerTick(), 1);
  assert.equal(await eng.schedulerTick(), 0); // same window not duplicated
  const run = (await db.query(`SELECT * FROM flow_collection_runs`)).rows[0];
  assert.equal(run.idempotency_key, `tn_a:${sc.id}:2026-10-13T00:00:00.000Z`);
  // re-inserting the same window (e.g. a second scheduler racing) is a no-op
  await db.query(`UPDATE flow_schedules SET next_run_at='2026-10-13T00:00:00Z'`);
  assert.equal(await eng.schedulerTick(), 0);
  setNow("2026-10-13T05:30:00Z"); // 5 windows missed → one run for latest window
  assert.equal(await eng.schedulerTick(), 1);
  const last = (await db.query(`SELECT scheduled_at FROM flow_collection_runs ORDER BY scheduled_at DESC LIMIT 1`)).rows[0];
  assert.equal(new Date(last.scheduled_at).toISOString(), "2026-10-13T05:00:00.000Z");
  assert.equal(new Date((await eng.listSchedules(A))[0].next_run_at).toISOString(), "2026-10-13T06:00:00.000Z");
});

test("transient fetch failure: retry_wait → retried up to max_attempts → failed, cursor untouched", async () => {
  const { db } = await setup();
  let now = new Date("2026-10-13T00:00:00Z");
  const eng = new Engine(db, async () => { throw new Error("HTTP 503"); }, () => now);
  const s = await eng.createSource(A, { url: FEED });
  const op = await eng.requestCollection(A, "k", { source_ids: [s.id] });
  assert.equal((await eng.workOnce("w"))!.status, "retry_wait");
  assert.equal((await eng.getOperation(A, op.operation_id)).status, "retrying");
  assert.equal(await eng.claim("w"), null, "not claimable before retry_at");
  for (const attempt of [2, 3]) {
    now = new Date(now.getTime() + 31 * 60_000);
    const r = await eng.workOnce("w");
    assert.equal(r!.status, attempt < 3 ? "retry_wait" : "failed");
  }
  assert.equal((await db.query(`SELECT attempts FROM flow_collection_runs`)).rows[0].attempts, 3);
  assert.equal((await eng.getOperation(A, op.operation_id)).status, "failed");
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_outbox WHERE type='operation.retrying'`)).rows[0].n, 2);
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_source_cursors`)).rows[0].n, 0);
  assert.equal((await eng.listSources(A))[0].consecutive_failures, 3);
});

test("outbox dispatch is ordered and at-least-once; stops on failure", async () => {
  const { eng, db } = await setup();
  const s = await eng.createSource(A, { url: FEED });
  feed = [item("1")];
  await eng.requestCollection(A, "k", { source_ids: [s.id] }); await eng.workOnce("w");
  const seen: any[] = [];
  let fail = true;
  assert.equal(await eng.dispatchOutbox(async (e) => { if (fail) throw new Error("down"); seen.push(e); }), 0);
  fail = false;
  assert.equal(await eng.dispatchOutbox(async (e) => { seen.push(e); }), 2);
  assert.deepEqual(seen.map((e) => e.type), ["collection.accepted", "collection.completed"]);
  assert.ok(seen[1].sequence > seen[0].sequence && seen[1].tenant_id === "tn_a");
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_outbox WHERE delivered_at IS NULL`)).rows[0].n, 0);
});

test("HTTP API: context verification, scopes, cross-tenant 404, 202 accept", async () => {
  const { eng } = await setup();
  const srv = createServer(handler(eng, { signingKey: KEY, audience: "loom-engine" }));
  await new Promise<void>((r) => srv.listen(0, r));
  const base = `http://127.0.0.1:${(srv.address() as any).port}`;
  const tok = (s: typeof A, scopes: string[]) => sign(KEY, { iss: "flow-platform", aud: "loom-engine", ...s, actor: { type: "user", id: "u" }, scopes, policy_version: "policy_v1" });
  const all = ["intel.sources.read", "intel.sources.write", "intel.collect", "intel.operations.read", "intel.documents.read", "intel.schedules.write"];
  const call = (method: string, path: string, t: string, body?: unknown, idem?: string) =>
    fetch(base + path, { method, headers: { authorization: `Bearer ${t}`, "content-type": "application/json", ...(idem ? { "x-idempotency-key": idem } : {}) }, body: body ? JSON.stringify(body) : undefined });
  try {
    assert.equal((await call("GET", "/v1/sources", "garbage")).status, 401);
    assert.equal((await call("GET", "/v1/sources", sign("x".repeat(32), { iss: "i", aud: "loom-engine", ...A, actor: { type: "user", id: "u" }, scopes: all, policy_version: "p" }))).status, 401);
    const created = await (await call("POST", "/v1/sources", tok(A, all), { url: FEED })).json();
    assert.equal((await call("POST", "/v1/sources", tok(A, ["intel.sources.read"]), { url: FEED + "2" })).status, 403);
    const acc = await call("POST", "/v1/collection-runs", tok(A, all), { source_ids: [created.data.id] }, "idem-1");
    assert.equal(acc.status, 202);
    const op = (await acc.json()).data.operation_id;
    assert.equal((await call("GET", `/v1/operations/${op}`, tok(B, all))).status, 404);
    assert.equal((await call("POST", "/v1/collection-runs", tok(B, all), { source_ids: [created.data.id] }, "idem-1")).status, 404);
    assert.equal((await call("GET", `/v1/operations/${op}`, tok(A, all))).status, 200);
  } finally { srv.close(); }
});
