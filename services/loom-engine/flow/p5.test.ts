import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, type FeedItem } from "./engine.ts";
import { Reports } from "./reports.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
let clock = new Date("2026-10-13T00:00:00Z");
const item = (id: string, body: string): FeedItem => ({ external_id: id, title: `Doc ${id}`, body, url: id, published_at: "2026-10-12T20:00:00Z" });

async function setup() {
  const db = await pgliteDb(); await migrate(db);
  const eng = new Engine(db, async () => [item("1", "solar tariff announced")], () => clock);
  const s1 = await eng.createSource(A, { url: "https://one.example/f" });
  await eng.requestCollection(A, "c1", { source_ids: [s1.id] }); await eng.workOnce("w");
  const rp = new Reports(eng);
  clock = new Date("2026-10-13T00:00:00Z");
  const pv = await rp.previewBrief(A, { query: "solar", source_ids: [s1.id], cron: "0 9 * * *", timezone: "Asia/Shanghai" });
  const a = await rp.applyBrief(A, "user:u", "policy_v1", { preview_id: pv.preview_id, approval_id: "ap", idempotency_key: "b1" });
  clock = new Date("2026-10-13T01:00:30Z");
  return { db, eng, rp, s1, sched: a.schedule };
}

test("revoked grant disables collection and brief schedules", async () => {
  const { eng, rp, s1, sched } = await setup();
  const sc = await eng.createSchedule(A, "user:gone", "policy_v1", { source_ids: [s1.id], interval_seconds: 60, start_at: "2026-10-13T00:00:00Z" });
  eng.authorize = async (s) => s.grant_actor !== "user:gone" && s.grant_actor !== "user:u";
  assert.equal(await eng.schedulerTick(), 0);
  const row = (await eng.db.query(`SELECT enabled, disabled_reason FROM flow_schedules WHERE id=$1`, [sc.id])).rows[0];
  assert.equal(row.enabled, false); assert.equal(row.disabled_reason, "grant_revoked");
  assert.equal(await rp.briefTick(), 0);
  const b = (await eng.db.query(`SELECT enabled, disabled_reason FROM flow_brief_schedules WHERE id=$1`, [sched.id])).rows[0];
  assert.equal(b.enabled, false); assert.equal(b.disabled_reason, "grant_revoked");
  assert.equal((await rp.listReports(A)).length, 0);
});

test("brief uses platform summarization; engine re-validates citations", async () => {
  const { rp } = await setup();
  let seen: any;
  rp.summarize = async (input) => {
    seen = input;
    const ev = input.evidence[0];
    return { body: "LLM summary", claims: [{ text: "Tariff announced", kind: "source_statement", citations: [{ evidence_id: ev.evidence_id, revision: ev.revision }] }] };
  };
  assert.equal(await rp.briefTick(), 1);
  assert.equal(seen.grant_actor, "user:u"); assert.ok(seen.evidence[0].quote);
  const [r] = await rp.listReports(A);
  const rep = await rp.getReport(A, r.id);
  assert.match(rep.body, /LLM summary/); assert.equal(rep.coverage.summarized_by, "model_gateway");
});

test("brief generation is transactional: failed save leaves the window due and no report", async () => {
  const { rp, eng, sched } = await setup();
  rp.summarize = async () => ({ body: "x", claims: [{ text: "bogus", kind: "source_statement", citations: [{ evidence_id: "ev_fake", revision: 1 }] }] });
  await assert.rejects(rp.briefTick());
  const b = (await eng.db.query(`SELECT next_run_at FROM flow_brief_schedules WHERE id=$1`, [sched.id])).rows[0];
  assert.equal(new Date(b.next_run_at).toISOString(), "2026-10-13T01:00:00.000Z");
  assert.equal((await rp.listReports(A)).length, 0);
  rp.summarize = async () => { throw new Error("gateway 502"); }; // falls back to extractive brief
  assert.equal(await rp.briefTick(), 1);
  const rep = await rp.getReport(A, (await rp.listReports(A))[0].id);
  assert.match(rep.coverage.summarization_error, /502/);
});

test("A13: 429 backs off scheduled collection; invalid credential pauses the source", async () => {
  const { classifyFailure } = await import("./engine.ts");
  assert.equal(classifyFailure("HTTP 429"), "rate_limited");
  assert.equal(classifyFailure("telegram: worker HTTP 401"), "auth");
  assert.equal(classifyFailure("HTTP 503"), "transient");
  const db = await pgliteDb(); await migrate(db);
  let mode = "429";
  let t = new Date("2026-10-13T00:00:00Z");
  const eng = new Engine(db, async () => { throw new Error(mode === "429" ? "HTTP 429" : "HTTP 401"); }, () => t);
  const s = await eng.createSource(A, { url: "https://r.example/f" });
  await eng.createSchedule(A, "user:u", "policy_v1", { source_ids: [s.id], interval_seconds: 60, start_at: t.toISOString() });
  assert.equal(await eng.schedulerTick(), 1);
  await eng.workOnce("w");
  const src = (await db.query(`SELECT status, backoff_until FROM flow_sources WHERE id=$1`, [s.id])).rows[0];
  assert.equal(src.status, "active"); assert.ok(src.backoff_until);
  // while backing off, due windows create no new runs (no infinite retry)
  await db.query(`UPDATE flow_sources SET backoff_until = now() + interval '1 hour' WHERE id=$1`, [s.id]);
  t = new Date(Date.now() + 120_000);
  assert.equal(await eng.schedulerTick(), 0);
  await db.query(`UPDATE flow_sources SET backoff_until = NULL WHERE id=$1`, [s.id]);
  mode = "401";
  t = new Date(Date.now() + 240_000);
  assert.equal(await eng.schedulerTick(), 1);
  await eng.workOnce("w");
  const p = (await db.query(`SELECT status, paused_reason FROM flow_sources WHERE id=$1`, [s.id])).rows[0];
  assert.equal(p.status, "paused"); assert.equal(p.paused_reason, "credential_invalid");
  t = new Date(Date.now() + 600_000);
  assert.equal(await eng.schedulerTick(), 0);
});
