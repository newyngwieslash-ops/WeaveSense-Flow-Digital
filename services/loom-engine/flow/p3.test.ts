import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, type FeedItem } from "./engine.ts";
import { Reports, nextCron, MAX_RESEARCH_CHILDREN } from "./reports.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
let feeds: Record<string, FeedItem[] | Error> = {};
let clock = new Date("2026-10-13T00:00:00Z");
async function setup() {
  const db = await pgliteDb(); await migrate(db);
  const eng = new Engine(db, async (url) => { const f = feeds[url]; if (f instanceof Error) throw f; return f ?? []; }, () => clock);
  const s1 = await eng.createSource(A, { url: "https://one.example/f" });
  const s2 = await eng.createSource(A, { url: "https://two.example/f" });
  const sb = await eng.createSource(B, { url: "https://one.example/f" });
  const collect = async (s: any, src: any, k: string) => { await eng.requestCollection(s, k, { source_ids: [src.id] }); await eng.workOnce("w"); };
  return { db, eng, rp: new Reports(eng), s1, s2, sb, collect };
}
const item = (id: string, body: string, pub = "2026-10-12T12:00:00Z"): FeedItem => ({ external_id: id, title: `Doc ${id}`, body, url: id, published_at: pub });

test("cron evaluates in the schedule timezone", () => {
  assert.equal(nextCron("0 9 * * *", "Asia/Shanghai", new Date("2026-10-13T00:00:00Z")).toISOString(), "2026-10-13T01:00:00.000Z");
  assert.equal(nextCron("30 8 * * 1", "UTC", new Date("2026-10-13T00:00:00Z")).toISOString(), "2026-10-19T08:30:00.000Z");
  assert.throws(() => nextCron("61 * * * *", "UTC", clock), (e: any) => e.status === 400);
  assert.throws(() => nextCron("0 9 * * *", "Mars/Base", clock), (e: any) => e.status === 400);
});

test("research: single depth, max 3 children per parent run, idempotent, cross-source claim", async () => {
  const { rp, s1, s2, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "lithium price rises sharply")], "https://two.example/f": [item("2", "lithium supply tight")] };
  await collect(A, s1, "c1"); await collect(A, s2, "c2");
  const r1 = await rp.startResearch(A, "k1", { parent_run_id: "run_x", question: "lithium" });
  assert.equal(r1.status, "succeeded");
  assert.ok(r1.output.claims.some((c: any) => c.kind === "cross_source_verified" && c.citations.length === 2));
  assert.equal((await rp.startResearch(A, "k1", { parent_run_id: "run_x", question: "lithium" })).replayed, true);
  await rp.startResearch(A, "k2", { parent_run_id: "run_x", question: "lithium" });
  await rp.startResearch(A, "k3", { parent_run_id: "run_x", question: "lithium" });
  await assert.rejects(rp.startResearch(A, "k4", { parent_run_id: "run_x", question: "lithium" }), (e: any) => e.status === 429);
  assert.equal(MAX_RESEARCH_CHILDREN, 3);
  await assert.rejects(rp.startResearch(A, "k5", { parent_run_id: r1.id, question: "lithium" }), (e: any) => e.status === 400);
  await assert.rejects(rp.startResearch(A, "k6", { parent_run_id: "run_y", question: "x", evidence_scope: { source_ids: ["src_foreign"] } }), (e: any) => e.status === 404);
});

test("report keeps resolving the original revision after the document is updated; engine rejects bad citations", async () => {
  const { rp, eng, s1, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "cobalt output falls 10 percent")] };
  await collect(A, s1, "c1");
  const r = await rp.startResearch(A, "k1", { parent_run_id: "run_r", question: "cobalt" });
  const cite = r.output.claims[0].citations[0];
  const saved = await rp.saveReport(A, "user:u", "rep1", { title: "Cobalt", claims: [{ text: "Cobalt output fell", kind: "source_statement", citations: [cite] }] });
  assert.equal((await rp.saveReport(A, "user:u", "rep1", { title: "Cobalt", claims: [] })).replayed, true);
  feeds = { "https://one.example/f": [item("1", "cobalt output RISES 5 percent (corrected)")] };
  await collect(A, s1, "c2");
  assert.equal((await eng.listDocuments(A, {}))[0].current_revision, 2);
  const rep = await rp.getReport(A, saved.report_id!);
  assert.equal(rep.claims[0].citations[0].revision, 1);
  assert.match(rep.claims[0].citations[0].chunk_text, /falls 10 percent/);
  await assert.rejects(rp.getReport(B, saved.report_id!), (e: any) => e.status === 404);
  assert.equal((await rp.resolveEvidence(B, [cite.evidence_id])).length, 0);
  await assert.rejects(rp.saveReport(A, "u", "rep2", { title: "x", claims: [{ text: "t", kind: "source_statement", citations: [{ ...cite, revision: 2 }] }] }), (e: any) => e.status === 422);
  await assert.rejects(rp.saveReport(B, "u", "rep3", { title: "x", claims: [{ text: "t", kind: "source_statement", citations: [cite] }] }), (e: any) => e.status === 422);
  const ev = (await eng.db.query(`SELECT type FROM flow_outbox WHERE type='report.completed'`)).rows;
  assert.equal(ev.length, 1);
});

test("brief schedule: approval required, one report per window across repeated ticks, partial source failure surfaced", async () => {
  const { rp, eng, s1, s2, collect } = await setup();
  clock = new Date("2026-10-13T00:00:00Z");
  feeds = { "https://one.example/f": [item("1", "solar tariff announced", "2026-10-12T20:00:00Z")], "https://two.example/f": new Error("upstream 503") };
  await collect(A, s1, "c1"); await collect(A, s2, "c2");
  const pv = await rp.previewBrief(A, { query: "solar", source_ids: [s1.id, s2.id], cron: "0 9 * * *", timezone: "Asia/Shanghai" });
  assert.equal(pv.next_runs[0], "2026-10-13T01:00:00.000Z");
  await assert.rejects(rp.applyBrief(A, "u", "p", { preview_id: pv.preview_id, idempotency_key: "b1" }), (e: any) => e.status === 403);
  await assert.rejects(rp.applyBrief(B, "u", "p", { preview_id: pv.preview_id, approval_id: "ap", idempotency_key: "b1" }), (e: any) => e.status === 404);
  const a = await rp.applyBrief(A, "user:u", "policy_v1", { preview_id: pv.preview_id, approval_id: "ap", idempotency_key: "b1" });
  assert.equal((await rp.applyBrief(A, "user:u", "policy_v1", { preview_id: pv.preview_id, approval_id: "ap", idempotency_key: "b1" })).replayed, true);
  assert.equal(await rp.briefTick(), 0); // not due yet
  clock = new Date("2026-10-13T01:00:30Z");
  assert.equal(await rp.briefTick(), 1);
  await eng.db.query(`UPDATE flow_brief_schedules SET next_run_at=$2 WHERE id=$1`, [a.schedule.id, "2026-10-13T01:00:00Z"]); // simulate a duplicate tick for the same window
  assert.equal(await rp.briefTick(), 0);
  const list = await rp.listReports(A);
  assert.equal(list.length, 1); assert.equal(list[0].kind, "brief");
  const rep = await rp.getReport(A, list[0].id);
  assert.equal(rep.coverage.source_failures.length, 1);
  assert.equal(rep.coverage.source_failures[0].source_id, s2.id);
  assert.match(rep.body, /1 source\(s\) failed/);
  assert.ok(rep.claims.length >= 1);
  assert.equal((await rp.listReports(B)).length, 0);
});
