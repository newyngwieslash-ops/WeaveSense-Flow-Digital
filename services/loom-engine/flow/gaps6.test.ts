// PRD §9.2/§9.3/§12.2/§11.4/§7.2: window budget reservation, blocked schedules, tenant fair queue +
// concurrency caps, document.revised / research.completed / schedule.blocked events, event-occurrence time.
import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, eventOccurredAt } from "./engine.ts";
import { Reports } from "./reports.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
let body = "v1";
const feed = async () => [{ external_id: "1", title: "储能招标 2026-10-10 开标", body, url: "https://a/1", published_at: "2026-10-12T00:00:00Z" }];

async function setup() {
  const db = await pgliteDb();
  await migrate(db);
  const clock = { now: new Date("2026-10-13T00:00:00Z") };
  const eng = new Engine(db, feed as any, () => clock.now);
  return { db, eng, clock };
}
const events = async (db: any, type: string) => (await db.query(`SELECT * FROM flow_outbox WHERE type=$1 ORDER BY sequence`, [type])).rows;

test("schedule budget: each due window reserves before jobs exist; settled when the operation ends", async () => {
  const { db, eng, clock } = await setup();
  const s = await eng.createSource(A, { url: "https://a.example/f" });
  const sc = await eng.createSchedule(A, "user:u", "policy_v1", { source_ids: [s.id], interval_seconds: 60, start_at: "2026-10-13T00:00:00Z", budget_micro_per_window: 5000 });
  assert.equal(Number(sc.budget_micro_per_window), 5000);
  await assert.rejects(eng.createSchedule(A, "user:u", "p", { source_ids: [s.id], interval_seconds: 60, budget_micro_per_window: -1 }), /budget_micro_per_window/);
  const calls: any[] = [];
  eng.budget = { reserve: async (_sc, key, amt) => { calls.push(["reserve", key, amt]); return { reservation_id: "rsv_1" }; },
    close: async (_sc, id, outcome) => { calls.push(["close", id, outcome]); } };
  assert.equal(await eng.schedulerTick(), 1);
  assert.deepEqual(calls[0], ["reserve", "sched:tn_a:" + sc.id + ":2026-10-13T00:00:00.000Z", 5000]);
  const run = (await db.query(`SELECT * FROM flow_collection_runs`)).rows[0];
  assert.equal(run.budget_reservation_id, "rsv_1");
  assert.equal(await eng.closeBudgets(), 0, "not closed while the job is active");
  while (await eng.workOnce("w"));
  assert.equal(await eng.closeBudgets(), 1);
  assert.deepEqual(calls.at(-1), ["close", "rsv_1", "settle"]);
  assert.equal(await eng.closeBudgets(), 0, "closed once");
  clock.now = new Date("2026-10-13T00:01:00Z");
});

test("budget denied → schedule blocked (reason shown, schedule.blocked event), next window admitted again", async () => {
  const { db, eng, clock } = await setup();
  const s = await eng.createSource(A, { url: "https://a.example/f" });
  const sc = await eng.createSchedule(A, "user:u", "policy_v1", { source_ids: [s.id], interval_seconds: 60, start_at: "2026-10-13T00:00:00Z", budget_micro_per_window: 5000 });
  let deny = true;
  eng.budget = { reserve: async () => { if (deny) throw Object.assign(new Error("quota"), { status: 402 }); return { reservation_id: "rsv_2" }; }, close: async () => {} };
  assert.equal(await eng.schedulerTick(), 0);
  const row = (await db.query(`SELECT * FROM flow_schedules WHERE id=$1`, [sc.id])).rows[0];
  assert.equal(row.state, "blocked"); assert.equal(row.blocked_reason, "budget_insufficient"); assert.equal(row.enabled, true);
  const ev = await events(db, "schedule.blocked");
  assert.equal(ev.length, 1); assert.equal(JSON.parse(JSON.stringify(ev[0].payload)).reason ?? ev[0].payload.reason, "budget_insufficient");
  deny = false; clock.now = new Date("2026-10-13T00:01:00Z");
  assert.equal(await eng.schedulerTick(), 1);
  assert.equal((await db.query(`SELECT state FROM flow_schedules WHERE id=$1`, [sc.id])).rows[0].state, "active");
});

test("grant revoked → schedule blocked + disabled, schedule.blocked emitted once", async () => {
  const { db, eng } = await setup();
  const s = await eng.createSource(A, { url: "https://a.example/f" });
  const sc = await eng.createSchedule(A, "user:gone", "policy_v1", { source_ids: [s.id], interval_seconds: 60, start_at: "2026-10-13T00:00:00Z" });
  eng.authorize = async () => false;
  assert.equal(await eng.schedulerTick(), 0);
  assert.equal(await eng.schedulerTick(), 0);
  const row = (await db.query(`SELECT * FROM flow_schedules WHERE id=$1`, [sc.id])).rows[0];
  assert.deepEqual([row.state, row.blocked_reason, row.enabled], ["blocked", "grant_revoked", false]);
  assert.equal((await events(db, "schedule.blocked")).length, 1);
});

test("fair queue: a tenant with live jobs yields to one with none; per-tenant concurrency cap", async () => {
  const { eng } = await setup();
  const a1 = await eng.createSource(A, { url: "https://a.example/1" });
  const a2 = await eng.createSource(A, { url: "https://a.example/2" });
  const a3 = await eng.createSource(A, { url: "https://a.example/3" });
  const b1 = await eng.createSource(B, { url: "https://b.example/1" });
  await eng.requestCollection(A, "a", { source_ids: [a1.id, a2.id, a3.id] });
  await eng.requestCollection(B, "b", { source_ids: [b1.id] });
  const first = await eng.claim("w1");
  assert.equal(first.tenant_id, "tn_a", "oldest first when nobody is running");
  const second = await eng.claim("w2");
  assert.equal(second.tenant_id, "tn_b", "tenant B is not starved behind A's backlog");
  await eng.setTenantConcurrency("tn_a", 2);
  const third = await eng.claim("w3");
  assert.equal(third.tenant_id, "tn_a");
  assert.equal(await eng.claim("w4"), null, "tenant A is at its cap of 2; nothing else is queued");
  await assert.rejects(eng.setTenantConcurrency("tn_a", 0));
});

test("document.revised is emitted for collected and enrichment revisions", async () => {
  const { db, eng } = await setup();
  const s = await eng.createSource(A, { url: "https://a.example/f" });
  body = "v1"; await eng.requestCollection(A, "k1", { source_ids: [s.id] }); while (await eng.workOnce("w"));
  assert.equal((await events(db, "document.revised")).length, 0, "first collection is not a revision");
  body = "v2 changed"; await eng.requestCollection(A, "k2", { source_ids: [s.id] }); while (await eng.workOnce("w"));
  const ev = await events(db, "document.revised");
  assert.equal(ev.length, 1);
  const p = typeof ev[0].payload === "string" ? JSON.parse(ev[0].payload) : ev[0].payload;
  assert.deepEqual([p.revision, p.previous_revision, p.cause], [2, 1, "collection"]);
  body = "v1";
});

test("research.completed is emitted when a research run finishes", async () => {
  const { db, eng } = await setup();
  const s = await eng.createSource(A, { url: "https://a.example/f" });
  await eng.requestCollection(A, "k1", { source_ids: [s.id] }); while (await eng.workOnce("w"));
  const rp = new Reports(eng);
  const r = await rp.startResearch(A, "rk", { parent_run_id: "run_1", question: "储能" });
  assert.equal(r.status, "succeeded");
  const ev = await events(db, "research.completed");
  assert.equal(ev.length, 1);
  assert.equal(ev[0].job_id, r.id);
});

test("event-occurrence time is separate from published/collected time", async () => {
  assert.deepEqual(eventOccurredAt({ title: "x", occurred_at: "2026-10-01T08:00:00Z" }), { at: "2026-10-01T08:00:00.000Z", source: "connector" });
  assert.deepEqual(eventOccurredAt({ title: "宁德时代2026年9月30日发布公告", published_at: "2026-10-02T00:00:00Z" }), { at: "2026-09-30T00:00:00.000Z", source: "title_date" });
  assert.equal(eventOccurredAt({ title: "2027-01-01 预告", published_at: "2026-10-02T00:00:00Z" }).at, null, "never after publication");
  assert.equal(eventOccurredAt({ title: "2026-02-30" }).at, null);
  assert.equal(eventOccurredAt({ title: "无日期" }).at, null);
  const { eng } = await setup();
  const s = await eng.createSource(A, { url: "https://a.example/f" });
  await eng.requestCollection(A, "k1", { source_ids: [s.id] }); while (await eng.workOnce("w"));
  const [d] = await eng.listDocuments(A, {});
  assert.equal(new Date(d.event_occurred_at).toISOString(), "2026-10-10T00:00:00.000Z");
  assert.ok(d.collected_at && d.published_at);
  const rd = await eng.readDocument(A, { document_id: d.id });
  assert.equal(rd.times.event_occurred_at_source, "title_date");
});

test("research runs record the caller's capability snapshot id", async () => {
  const { db, eng } = await setup();
  const rp = new Reports(eng);
  const r = await rp.startResearch(A, "rk2", { parent_run_id: "run_2", question: "储能", capability_snapshot_id: "cap_abc" });
  assert.equal((await db.query(`SELECT capability_snapshot_id FROM flow_research_runs WHERE id=$1`, [r.id])).rows[0].capability_snapshot_id, "cap_abc");
});

test("ASR: stored audio of a new document is queued in the collection commit, transcribed as a revision, asr_seconds metered", async () => {
  const key = "tenants/tn_a/workspaces/ws_a/telegram/chan/9";
  const db = await pgliteDb(); await migrate(db);
  const eng = new Engine(db, (async () => ({ collector: "telethon", mode: "fixture", items: [{ external_id: "9", title: "语音", body: "频道语音", url: "https://t.me/chan/9", published_at: "2026-10-12T00:00:00Z",
    media: [{ url: key, type: "audio", status: "stored", reason: "", content_type: "audio/ogg" }] }] })) as any, () => new Date("2026-10-13T00:00:00Z"));
  const calls: any[] = [];
  eng.asr = async (k, sc, lang) => { calls.push([k, sc.tenant_id, lang]); return { text: "储能电站并网发电", seconds: 37, language: "zh", digest: "d" }; };
  const prev = process.env.ASR_ENABLED; process.env.ASR_ENABLED = "true";
  try {
    const s = await eng.createSource(A, { kind: "telegram", config: { handle: "chan_abc" } });
    await eng.requestCollection(A, "k", { source_ids: [s.id] }); while (await eng.workOnce("w"));
    assert.equal((await db.query(`SELECT count(*)::int n FROM flow_media_asr WHERE status='queued'`)).rows[0].n, 1);
    assert.equal(await eng.asrTick(), 1);
    assert.deepEqual(calls[0], [key, "tn_a", undefined]);
    const [d] = await eng.listDocuments(A, {});
    const rd = await eng.readDocument(A, { document_id: d.id });
    assert.match(rd.text, /\[转写: .*\]\n储能电站并网发电/);
    const usage = (await events(db, "usage.recorded")).map((e: any) => typeof e.payload === "string" ? JSON.parse(e.payload) : e.payload);
    assert.deepEqual(usage.map((u: any) => [u.usage_type, u.quantity]), [["asr_seconds", 37]]);
    assert.equal(await eng.asrTick(), 0, "succeeded targets are not re-run");
    const again = await eng.asrDocument(A, { document_id: d.id });
    assert.equal(again.processed.length, 0);
    await assert.rejects(eng.asrDocument(A, { document_id: d.id, language: "zh;x" }), /language/);
    // Failure is recorded and retried (bounded) — unsupported is final.
    eng.asr = async () => { throw Object.assign(new Error("asr_unsupported"), { code: "asr_unsupported" }); };
    await db.query(`UPDATE flow_media_asr SET status='failed'`);
    await eng.asrTick();
    assert.equal((await db.query(`SELECT status FROM flow_media_asr`)).rows[0].status, "unsupported");
  } finally { if (prev === undefined) delete process.env.ASR_ENABLED; else process.env.ASR_ENABLED = prev; }
});

test("jobs view lists recent collection jobs with job_state and source", async () => {
  const { eng } = await setup();
  const s = await eng.createSource(A, { url: "https://a.example/f", title: "A feed" });
  await eng.requestCollection(A, "k1", { source_ids: [s.id] });
  let jobs = await eng.listJobs(A);
  assert.deepEqual([jobs.length, jobs[0].job_state, jobs[0].source_title], [1, "pending", "A feed"]);
  while (await eng.workOnce("w"));
  jobs = await eng.listJobs(A, { status: "succeeded" });
  assert.equal(jobs.length, 1);
  assert.equal((await eng.listJobs(B)).length, 0, "tenant scoped");
});
