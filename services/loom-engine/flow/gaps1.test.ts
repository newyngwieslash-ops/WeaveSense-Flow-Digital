// Gap round 1: §7.3 term dictionary, §8.3 list sources / compare evidence, §11 PATCH source, revisions, REST research.
import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, type FeedItem } from "./engine.ts";
import { Reports, numericFacts } from "./reports.ts";
import { expandQuery, mergeAliases, BUILTIN_TERM_GROUPS } from "./terms.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
let feeds: Record<string, FeedItem[] | Error> = {};
const clock = new Date("2026-10-13T00:00:00Z");
async function setup() {
  const db = await pgliteDb(); await migrate(db);
  const eng = new Engine(db, async (url) => { const f = feeds[url]; if (f instanceof Error) throw f; return f ?? []; }, () => clock);
  const s1 = await eng.createSource(A, { url: "https://one.example/f", title: "One" });
  const s2 = await eng.createSource(A, { url: "https://two.example/f" });
  const collect = async (src: any, k: string) => { await eng.requestCollection(A, k, { source_ids: [src.id] }); return eng.workOnce("w"); };
  return { db, eng, rp: new Reports(eng), s1, s2, collect };
}
const item = (id: string, title: string, body: string): FeedItem => ({ external_id: id, title, body, url: `https://x.example/${id}`, published_at: "2026-10-12T00:00:00Z" });

test("term dictionary: CJK substring and Latin whole-word expansion", () => {
  const e = expandQuery("宁德时代 储能 订单");
  assert.ok(e.variants.some((v) => v.includes("CATL")));
  assert.ok(e.matched.some((m) => m.term === "宁德时代"));
  assert.deepEqual(expandQuery("what she said").variants, ["what she said"], "AI must not expand inside 'said'");
  assert.ok(expandQuery("AI chips").variants.includes("人工智能 chips"));
  assert.ok(expandQuery("国家药监局 批准").matched.every((m) => m.term !== "药监局"), "longest term wins");
  assert.ok(BUILTIN_TERM_GROUPS.length >= 40);
  const g = mergeAliases([{ term: "宁德时代", alias: "宁王" }, { term: "foo corp", alias: "富公司" }]);
  assert.ok(g.find((x) => x.terms.includes("CATL"))!.terms.includes("宁王"));
  assert.ok(g.some((x) => x.terms.includes("富公司")));
});

test("search expands Chinese names to acronyms and workspace aliases (tenant-scoped)", async () => {
  const { eng, s1, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "CATL wins storage order", "CATL signs a 10 GWh deal"), item("2", "Pharma", "NMPA approves a new ADC drug")] };
  await collect(s1, "c1");
  const r = await eng.searchDocuments(A, { query: "宁德时代" });
  assert.equal(r.items.length, 1);
  assert.equal(r.items[0].title, "CATL wins storage order");
  assert.ok((await eng.searchDocuments(A, { query: "国家药监局" })).items.length === 1);
  assert.equal((await eng.searchDocuments(A, { query: "宁王" })).items.length, 0);
  await eng.addTermAliases(A, "user:u", { items: [{ term: "宁德时代", alias: "宁王" }] });
  assert.equal((await eng.searchDocuments(A, { query: "宁王" })).items.length, 1);
  assert.equal((await eng.listTerms(B)).custom.length, 0, "aliases are workspace scoped");
  await assert.rejects(eng.addTermAliases(A, "u", { items: [{ term: "x", alias: "X" }] }), (e: any) => e.status === 400);
  assert.equal((await eng.deleteTermAlias(A, { term: "宁德时代", alias: "宁王" })).deleted, 1);
});

test("PATCH source: expected_revision, replay, conflict, immutable identity, pause cancels queued runs", async () => {
  const { eng, s1 } = await setup();
  const u = await eng.updateSource(A, s1.id, { expected_revision: 1, title: "Renamed" });
  assert.equal(u.revision, 2); assert.equal(u.title, "Renamed");
  assert.equal((await eng.updateSource(A, s1.id, { expected_revision: 1, title: "Renamed" })).replayed, true);
  await assert.rejects(eng.updateSource(A, s1.id, { expected_revision: 1, title: "Other" }), (e: any) => e.status === 409);
  await assert.rejects(eng.updateSource(A, s1.id, { title: "x" }), (e: any) => e.status === 400);
  await assert.rejects(eng.updateSource(A, s1.id, { expected_revision: 2, config: { handle: "x" } }), (e: any) => e.status === 400);
  await assert.rejects(eng.updateSource(B, s1.id, { expected_revision: 2, title: "y" }), (e: any) => e.status === 404);
  await eng.requestCollection(A, "q1", { source_ids: [s1.id] });
  const p = await eng.updateSource(A, s1.id, { expected_revision: 2, status: "paused" });
  assert.equal(p.status, "paused");
  assert.equal((await eng.getOperation(A, (await eng.requestCollection(A, "q1", { source_ids: [s1.id] })).operation_id)).status, "cancelled");
  assert.equal((await eng.updateSource(A, s1.id, { expected_revision: 3, status: "active" })).status, "active");
});

test("intel_list_sources health and document revision routes with evidence", async () => {
  const { eng, rp, s1, s2, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "Lithium", "lithium price 10%")], "https://two.example/f": new Error("HTTP 503 unavailable") };
  await collect(s1, "c1"); await collect(s2, "c2");
  const h = await eng.listSourcesHealth(A);
  assert.equal(h.find((x: any) => x.id === s1.id).health, "healthy");
  assert.equal(h.find((x: any) => x.id === s2.id).health, "backoff");
  assert.equal(h.find((x: any) => x.id === s2.id).last_run.status, "failed");
  feeds["https://one.example/f"] = [item("1", "Lithium", "lithium price 12% (revised)")];
  await collect(s1, "c3");
  const doc = (await eng.listDocuments(A, {}))[0];
  await rp.startResearch(A, "rk", { parent_run_id: "run_1", question: "lithium" });
  const revs = await eng.listRevisions(A, doc.id);
  assert.deepEqual(revs.revisions.map((r: any) => r.revision), [2, 1]);
  const r2 = await eng.getRevision(A, doc.id, 2);
  assert.ok(r2.evidence.length >= 1);
  await assert.rejects(eng.listRevisions(B, doc.id), (e: any) => e.status === 404);
});

test("compare evidence: per-source grouping, numeric divergence, stale revision; REST research 202 + cancel", async () => {
  const { eng, rp, s1, s2, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "Lithium A", "lithium carbonate price rose 10% this week")],
    "https://two.example/f": [item("2", "Lithium B", "lithium carbonate price rose 15% this week")] };
  await collect(s1, "c1"); await collect(s2, "c2");
  const r = await rp.startResearch(A, "k", { parent_run_id: "run_c", question: "lithium" });
  const cmp = await rp.compareEvidence(A, { evidence_ids: [...r.output.evidence_ids, "ev_missing"] });
  assert.equal(cmp.sources, 2);
  assert.deepEqual(cmp.not_found, ["ev_missing"]);
  assert.ok(cmp.divergences.some((d: any) => d.unit === "%"));
  assert.ok(cmp.shared_terms.some((t: any) => t.term === "lithium"));
  await assert.rejects(rp.compareEvidence(A, { evidence_ids: ["one"] }), (e: any) => e.status === 400);
  assert.equal((await rp.compareEvidence(B, { evidence_ids: r.output.evidence_ids })).items.length, 0);
  assert.deepEqual(numericFacts("增长 12.5% ，装机 3 GWh"), [{ value: 12.5, unit: "%", text: "12.5%" }, { value: 3, unit: "gwh", text: "3 GWh" }]);

  const a = await rp.startResearch(A, "rest1", { parent_run_id: "rest:x", question: "lithium" }, { async: true });
  assert.equal(a.status, "running");
  await rp.settle(a.id);
  assert.equal((await rp.getResearch(A, a.id)).status, "succeeded");
  const c = await rp.startResearch(A, "rest2", { parent_run_id: "rest:y", question: "lithium" }, { async: true });
  const cancelled = await rp.cancelResearch(A, c.id, "user");
  await rp.settle(c.id);
  assert.equal(cancelled.cancelled, true);
  assert.equal((await rp.getResearch(A, c.id)).status, "cancelled", "late completion must not overwrite cancel");
  assert.equal((await rp.cancelResearch(A, a.id)).cancelled, false);
  await assert.rejects(rp.getResearch(B, a.id), (e: any) => e.status === 404);
});
