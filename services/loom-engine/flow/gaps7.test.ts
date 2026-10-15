// Audit-6 gaps: §9.4 producer draft → host Reviewer, and parent→child cancel propagation (A15).
import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, type FeedItem } from "./engine.ts";
import { Reports, supportRatio } from "./reports.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
let feeds: Record<string, FeedItem[]> = {};
async function setup() {
  const db = await pgliteDb(); await migrate(db);
  const eng = new Engine(db, async (url) => feeds[url] ?? [], () => new Date("2026-10-13T00:00:00Z"));
  const s1 = await eng.createSource(A, { url: "https://one.example/f" });
  const s2 = await eng.createSource(A, { url: "https://two.example/f" });
  const collect = async (src: any, k: string) => { await eng.requestCollection(A, k, { source_ids: [src.id] }); await eng.workOnce("w"); };
  return { db, eng, rp: new Reports(eng), s1, s2, collect };
}
const item = (id: string, body: string): FeedItem => ({ external_id: id, title: `Doc ${id}`, body, url: id, published_at: "2026-10-12T12:00:00Z" });

test("research output = reviewed draft: reviewer verdict recorded, supported claims kept", async () => {
  const { rp, s1, s2, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "lithium price rises 10% this month")], "https://two.example/f": [item("2", "lithium price rises 12% on tight supply")] };
  await collect(s1, "c1"); await collect(s2, "c2");
  const r: any = await rp.startResearch(A, "k1", { parent_run_id: "run_rv", question: "lithium" });
  assert.equal(r.status, "succeeded");
  assert.equal(r.output.review.reviewer, "host_reviewer_v1");
  assert.equal(r.output.review.checked, r.output.draft.claims.length);
  assert.ok(r.output.claims.length > 0 && r.output.review.rejected.length === 0);
  // Diverging numbers of the same unit across sources are flagged as a conflict.
  const cross = r.output.claims.find((c: any) => c.kind === "cross_source_verified");
  assert.ok(cross.review_flags.includes("conflict"), JSON.stringify(cross));
});

test("reviewer rejects unsupported, wrong-revision, foreign and single-source claims; flags inference", async () => {
  const { rp, s1, s2, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "cobalt output falls 10 percent in Congo")], "https://two.example/f": [item("2", "nickel demand grows in Indonesia")] };
  await collect(s1, "c1"); await collect(s2, "c2");
  const r: any = await rp.startResearch(A, "k1", { parent_run_id: "run_rj", question: "cobalt" });
  const cobalt = r.output.draft.claims.find((c: any) => /cobalt/.test(c.text)).citations[0];
  const out = await rp.review(A, [
    { text: "cobalt output falls in Congo", kind: "source_statement", citations: [cobalt] },
    { text: "gold reserves doubled worldwide", kind: "source_statement", citations: [cobalt] },
    { text: "cobalt output falls", kind: "source_statement", citations: [{ ...cobalt, revision: cobalt.revision + 5 }] },
    { text: "cobalt output falls", kind: "source_statement", citations: [] },
    { text: "cobalt output falls", kind: "cross_source_verified", citations: [cobalt] },
    { text: "prices may rise", kind: "model_inference", citations: [] },
  ]);
  assert.equal(out.review.verdict, "rejected_claims");
  assert.deepEqual(out.review.rejected.map((x: any) => x.idx), [1, 2, 3, 4]);
  assert.equal(out.claims.length, 2);
  assert.deepEqual(out.claims[1].review_flags, ["uncertain"]);
  // Tenant B cannot cite tenant A evidence.
  const foreign = await rp.review(B, [{ text: "cobalt output falls in Congo", kind: "source_statement", citations: [cobalt] }]);
  assert.equal(foreign.claims.length, 0);
  assert.ok(supportRatio("cobalt output", ["cobalt output falls"]) === 1);
});

test("A15: cancelling the parent run cancels its queued and running child research; late completion cannot overwrite", async () => {
  const { db, rp, s1, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "graphite exports restricted")] };
  await collect(s1, "c1");
  // A running child (async, still in flight) and a queued child.
  let release!: () => void; const gate = new Promise<void>((r) => (release = r));
  const orig = (rp as any).gather.bind(rp);
  (rp as any).gather = async (...a: any[]) => { await gate; return orig(...a); };
  const running: any = await rp.startResearch(A, "k_run", { parent_run_id: "run_parent", question: "graphite" }, { async: true });
  await db.query(`INSERT INTO flow_research_runs (id, tenant_id, workspace_id, parent_run_id, kind, question, status, idempotency_key) VALUES ('rr_q','tn_a','ws_a','run_parent','research','graphite','queued','k_q')`);
  await db.query(`INSERT INTO flow_research_runs (id, tenant_id, workspace_id, parent_run_id, kind, question, status, idempotency_key) VALUES ('rr_other','tn_a','ws_a','run_other','research','x','queued','k_o')`);
  // Another tenant cannot cancel these children.
  assert.deepEqual((await rp.cancelChildren(B, "run_parent")).cancelled, []);
  const c = await rp.cancelChildren(A, "run_parent", "parent run cancelled");
  assert.deepEqual(c.cancelled.sort(), [running.id, "rr_q"].sort());
  release(); await rp.settle(running.id);
  assert.equal((await rp.getResearch(A, running.id)).status, "cancelled");
  assert.equal((await rp.getResearch(A, "rr_q")).status, "cancelled");
  assert.equal((await rp.getResearch(A, "rr_other")).status, "queued");
  const ev = (await db.query(`SELECT payload FROM flow_outbox WHERE type='research.completed'`)).rows.map((r: any) => (typeof r.payload === "string" ? JSON.parse(r.payload) : r.payload));
  assert.equal(ev.filter((p: any) => p.status === "cancelled" && p.parent_run_id === "run_parent").length, 2);
  // Idempotent.
  assert.deepEqual((await rp.cancelChildren(A, "run_parent")).cancelled, []);
});

test("brief: a model draft whose claims all fail review falls back to the extractive brief", async () => {
  const { rp, eng, s1, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "silicon wafer prices steady")] };
  await collect(s1, "c1");
  const pv = await rp.previewBrief(A, { query: "silicon", source_ids: [s1.id], cron: "0 * * * *", timezone: "UTC" });
  await rp.applyBrief(A, "user:u", "policy_v1", { preview_id: pv.preview_id, approval_id: "ap1", idempotency_key: "b1" });
  await eng.db.query(`UPDATE flow_brief_schedules SET next_run_at='2026-10-12T23:00:00Z'`);
  rp.summarize = async () => ({ body: "x", claims: [{ text: "bogus", kind: "source_statement", citations: [{ evidence_id: "ev_fake", revision: 1 }] }] });
  assert.equal(await rp.briefTick(), 1);
  const rep = await rp.getReport(A, (await rp.listReports(A))[0].id);
  assert.equal(rep.coverage.summarized_by, "extractive");
  assert.equal(rep.coverage.review.verdict, "rejected_claims");
  assert.ok(rep.claims.every((c: any) => c.text !== "bogus"));
});
