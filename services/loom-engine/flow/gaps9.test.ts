// Audit-8 gaps: monitor plan/budget/re-approval, evidence snippet hash + URL, raw digests,
// embed/analyze jobs, extraction completeness, report export, scoped media download.
import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, type FeedItem } from "./engine.ts";
import { Reports } from "./reports.ts";
import { derivedTick, hashEmbedder, downloadMedia, presignGet, monitorExpansion, snippetHash } from "./gaps9.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
let feeds: Record<string, FeedItem[]> = {};
const item = (id: string, body: string): FeedItem => ({ external_id: id, title: `Doc ${id}`, body, url: `https://one.example/${id}`, published_at: "2026-10-12T12:00:00Z" });
async function setup() {
  const db = await pgliteDb(); await migrate(db);
  const eng = new Engine(db, async (url) => feeds[url] ?? [], () => new Date("2026-10-13T00:00:00Z"));
  const s1 = await eng.createSource(A, { url: "https://one.example/f" });
  const s2 = await eng.createSource(A, { url: "https://two.example/f" });
  const collect = async (src: any, k: string) => { await eng.requestCollection(A, k, { source_ids: [src.id] }); while (await eng.workOnce("w")); };
  return { db, eng, rp: new Reports(eng), s1, s2, collect };
}

test("§5.1 preview is a structured plan with extraction method, cost estimate, budget and targets; apply binds the budget", async () => {
  const { db, eng, s1 } = await setup();
  const pv: any = await eng.previewMonitor(A, { keywords: ["锂"], source_ids: [s1.id], interval_seconds: 3600, timezone: "Asia/Shanghai",
    notification_target_ids: ["nt_1"], price: { version: "price_v9", per_source_run_micro: { rss: 1500 } } });
  assert.equal(pv.cost_estimate.per_window_micro, 1500);
  assert.equal(pv.cost_estimate.per_day_micro, 1500 * 24);
  assert.equal(pv.cost_estimate.price_version, "price_v9");
  assert.equal(pv.budget_micro_per_window, 1500, "budget defaults to the estimate");
  assert.match(pv.extraction_method, /feed_summary/);
  assert.deepEqual(pv.notification_target_ids, ["nt_1"]);
  assert.equal(pv.sources[0].id, s1.id);
  assert.equal(pv.requires_approval, true);
  const got: any = await eng.getMonitorPreview(A, pv.preview_id);
  assert.equal(got.cost_estimate.per_window_micro, 1500);
  await assert.rejects(eng.getMonitorPreview(B, pv.preview_id), (e: any) => e.status === 404);
  const { monitor } = await eng.applyMonitor(A, "user:u", "p1", { preview_id: pv.preview_id, approval_id: "ap_1", idempotency_key: "k1" });
  assert.equal(Number(monitor.budget_micro_per_window), 1500);
  const sch = (await db.query(`SELECT budget_micro_per_window FROM flow_schedules WHERE id=$1`, [monitor.schedule_id])).rows[0];
  assert.equal(Number(sch.budget_micro_per_window), 1500, "schedule reserves the approved budget per window");
});

test("§5.1 monitor update: narrowing applies, widening sources/budget/frequency/targets needs re-approval", async () => {
  const { db, eng, s1, s2 } = await setup();
  const pv: any = await eng.previewMonitor(A, { keywords: ["锂"], source_ids: [s1.id], interval_seconds: 3600, budget_micro_per_window: 1000 });
  const { monitor } = await eng.applyMonitor(A, "user:u", "p1", { preview_id: pv.preview_id, approval_id: "ap_1", idempotency_key: "k1" });
  const narrowed: any = await eng.updateMonitor(A, "user:u", monitor.id, { keywords: ["锂", "碳酸锂"], budget_micro_per_window: 500, expected_version: 1 });
  assert.deepEqual(narrowed.expansion, []);
  assert.equal(narrowed.version, 2);
  for (const change of [{ source_ids: [s1.id, s2.id] }, { budget_micro_per_window: 5000 }, { interval_seconds: 600 }, { notification_target_ids: ["nt_x"] }]) {
    await assert.rejects(eng.updateMonitor(A, "user:u", monitor.id, change as any), (e: any) => e.status === 409 && /reapproval_required/.test(e.message));
  }
  await assert.rejects(eng.updateMonitor(A, "user:u", monitor.id, { keywords: ["x"], expected_version: 1 }), (e: any) => e.status === 409);
  const widened: any = await eng.updateMonitor(A, "user:u", monitor.id, { source_ids: [s1.id, s2.id], budget_micro_per_window: 2000, approval_id: "ap_2" });
  assert.deepEqual(widened.expansion, ["sources", "budget"]);
  const sch = (await db.query(`SELECT source_ids, budget_micro_per_window FROM flow_schedules WHERE id=$1`, [monitor.schedule_id])).rows[0];
  assert.equal(sch.source_ids.length, 2);
  assert.equal(Number(sch.budget_micro_per_window), 2000);
  // Agent path: a preview of an existing monitor carries the expansion and applies as an approved update.
  const pv2: any = await eng.previewMonitor(A, { monitor_id: monitor.id, keywords: ["锂"], source_ids: [s1.id, s2.id], interval_seconds: 3600, budget_micro_per_window: 9000 });
  assert.deepEqual(pv2.expansion, ["budget"]);
  const r: any = await eng.applyMonitor(A, "agent:a", "p1", { preview_id: pv2.preview_id, approval_id: "ap_3", idempotency_key: "k3" });
  assert.equal(r.monitor.id, monitor.id);
  const revs = await eng.listMonitorRevisions(A, monitor.id);
  assert.deepEqual(revs.map((x: any) => [x.version, x.expanded, x.approval_id]), [[1, true, "ap_1"], [2, false, null], [3, true, "ap_2"], [4, true, "ap_3"]]);
  await assert.rejects(eng.updateMonitor(B, "user:b", monitor.id, { keywords: ["x"] }), (e: any) => e.status === 404);
  assert.deepEqual(monitorExpansion({ source_ids: ["a"], budget_micro_per_window: 1, interval_seconds: 300, notification_target_ids: [] },
    { source_ids: ["a"], budget_micro_per_window: 1, interval_seconds: 600, notification_target_ids: [] }), []);
});

test("§7.3 evidence stores snippet hash + URL; tampered quote or moved URL fails citation checks", async () => {
  const { db, eng, rp, s1, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "cobalt output falls 10 percent")] };
  await collect(s1, "c1");
  const r: any = await rp.startResearch(A, "k1", { parent_run_id: "run_r", question: "cobalt" });
  const cite = r.output.claims[0].citations[0];
  const [ev]: any = await rp.resolveEvidence(A, [cite.evidence_id]);
  assert.equal(ev.snippet_hash, snippetHash(ev.quote));
  assert.equal(ev.url, "https://one.example/1");
  assert.equal(ev.snippet_ok && ev.url_ok, true);
  await assert.rejects(rp.saveReport(A, "u", "r0", { title: "x", claims: [{ text: "t", kind: "source_statement", citations: [{ ...cite, snippet_hash: "0".repeat(64) }] }] }), (e: any) => e.status === 422);
  await assert.rejects(rp.saveReport(A, "u", "r0b", { title: "x", claims: [{ text: "t", kind: "source_statement", citations: [{ ...cite, url: "https://evil.example/" }] }] }), (e: any) => e.status === 422);
  assert.ok((await rp.saveReport(A, "u", "r1", { title: "ok", claims: [{ text: "t", kind: "source_statement", citations: [{ ...cite, snippet_hash: ev.snippet_hash, url: ev.url }] }] })).report_id);
  await db.query(`UPDATE flow_evidence SET quote='cobalt output RISES' WHERE id=$1`, [cite.evidence_id]);
  const [bad]: any = await rp.resolveEvidence(A, [cite.evidence_id]);
  assert.equal(bad.snippet_ok, false);
  await assert.rejects(rp.saveReport(A, "u", "r2", { title: "x", claims: [{ text: "t", kind: "source_statement", citations: [cite] }] }), (e: any) => e.status === 422 && /snippet hash/.test(e.message));
  await db.query(`UPDATE flow_evidence SET quote=$2, snippet_hash=$3 WHERE id=$1`, [cite.evidence_id, ev.quote, ev.snippet_hash]);
  await db.query(`UPDATE flow_documents SET url='https://moved.example/1' WHERE id=$1`, [cite.document_id ?? ev.document_id]);
  await assert.rejects(rp.saveReport(A, "u", "r3", { title: "x", claims: [{ text: "t", kind: "source_statement", citations: [cite] }] }), (e: any) => e.status === 422 && /url mismatch/.test(e.message));
});

test("§6.3 raw item + digest per revision and per run; §5 content library shows source and extraction completeness", async () => {
  const { db, eng, s1, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "lithium")] };
  await collect(s1, "c1");
  const rv = (await db.query(`SELECT raw_item, raw_digest, extraction FROM flow_document_revisions`)).rows[0];
  const raw = typeof rv.raw_item === "string" ? JSON.parse(rv.raw_item) : rv.raw_item;
  assert.equal(raw.external_id, "1");
  assert.match(rv.raw_digest, /^[0-9a-f]{64}$/);
  assert.equal(rv.extraction, "feed");
  const run = (await db.query(`SELECT raw_digest, raw_items FROM flow_collection_runs`)).rows[0];
  assert.match(run.raw_digest, /^[0-9a-f]{64}$/);
  assert.equal(run.raw_items, 1);
  const [d]: any = await eng.listDocuments(A, {});
  assert.equal(d.source_title !== undefined && d.source_kind, "rss");
  assert.equal(d.extraction.level, "feed_summary");
  assert.equal(d.extraction.complete, false);
});

test("§6.4 embed and analyze are separate jobs, once per content hash; embedding provider is pluggable", async () => {
  const { db, eng, s1, collect } = await setup();
  const pv: any = await eng.previewMonitor(A, { keywords: ["lithium"], source_ids: [s1.id], interval_seconds: 3600 });
  await eng.applyMonitor(A, "user:u", "p", { preview_id: pv.preview_id, approval_id: "ap", idempotency_key: "k" });
  feeds = { "https://one.example/f": [item("1", "lithium price up 10% in Q3")] };
  await collect(s1, "c1");
  await collect(s1, "c2"); // unchanged body: nothing new queued
  const jobs = (await db.query(`SELECT kind, status FROM flow_derived_jobs ORDER BY kind`)).rows;
  assert.deepEqual(jobs.map((j: any) => [j.kind, j.status]), [["analyze", "queued"], ["embed", "queued"]]);
  assert.equal(await derivedTick(db, null), 2);
  const after = (await db.query(`SELECT kind, status, provider, error FROM flow_derived_jobs ORDER BY kind`)).rows;
  assert.deepEqual(after.map((j: any) => [j.kind, j.status]), [["analyze", "succeeded"], ["embed", "skipped"]]);
  assert.match(after[1].error, /vector search disabled/);
  const [doc]: any = await eng.listDocuments(A, {});
  const { getAnalysis } = await import("./gaps9.ts");
  const an: any = await getAnalysis(db, A, doc.id);
  assert.deepEqual(an.analysis.matched_keywords, ["lithium"]);
  assert.deepEqual(an.analysis.numeric_facts, [{ value: 10, unit: "%" }]);
  await assert.rejects(getAnalysis(db, B, doc.id), (e: any) => e.status === 404);
  // Body changes → a new revision → new embed/analyze jobs; hash embedder writes vectors.
  feeds = { "https://one.example/f": [item("1", "lithium price up 12% in Q3 (revised)")] };
  await collect(s1, "c3");
  assert.equal(await derivedTick(db, hashEmbedder(16)), 2);
  const vec = (await db.query(`SELECT provider, dims, array_length(vector, 1) AS n FROM flow_document_embeddings WHERE revision=2`)).rows[0];
  assert.deepEqual([vec.provider, vec.dims, vec.n], ["hash-16", 16, 16]);
  assert.equal(await derivedTick(db, hashEmbedder(16)), 0, "frequent ticks do not re-spend");
});

test("§5.2/§10.3 report export (md/html/json) is tenant-scoped (A02)", async () => {
  const { eng, rp, s1, collect } = await setup();
  feeds = { "https://one.example/f": [item("1", "cobalt output falls 10 percent")] };
  await collect(s1, "c1");
  const r: any = await rp.startResearch(A, "k1", { parent_run_id: "run_r", question: "cobalt" });
  const saved: any = await rp.saveReport(A, "u", "rep", { title: "Cobalt <weekly>", body: "summary", claims: [{ text: "Cobalt fell", kind: "source_statement", citations: [r.output.claims[0].citations[0]] }] });
  const md: any = await rp.exportReport(A, saved.report_id, "md");
  assert.equal(md.content_type, "text/markdown; charset=utf-8");
  assert.match(md.content, /^# Cobalt <weekly>/);
  assert.match(md.content, /\*\*\[source_statement\]\*\* Cobalt fell/);
  assert.match(md.content, /rev 1 · evidence ev_/);
  const html: any = await rp.exportReport(A, saved.report_id, "html");
  assert.match(html.content, /<h1>Cobalt &lt;weekly&gt;<\/h1>/);
  const js: any = await rp.exportReport(A, saved.report_id, "json");
  assert.equal(JSON.parse(js.content).id, saved.report_id);
  await assert.rejects(rp.exportReport(A, saved.report_id, "exe"), (e: any) => e.status === 400);
  await assert.rejects(rp.exportReport(B, saved.report_id, "md"), (e: any) => e.status === 404, "A02: guessed foreign report id is not exportable");
  void eng;
});

test("§10.4 media download: verify scope + reference first, then proxy from the private bucket", async () => {
  const key = "tenants/tn_a/workspaces/ws_a/telegram/chan/9";
  const db = await pgliteDb(); await migrate(db);
  const bytes = Buffer.from("PNGDATA");
  const sha = (await import("node:crypto")).createHash("sha256").update(bytes).digest("hex");
  const eng = new Engine(db, (async () => ({ collector: "telethon", mode: "fixture", items: [{ external_id: "9", title: "图", body: "图片", url: "https://t.me/chan/9", published_at: "2026-10-12T00:00:00Z",
    media: [{ url: key, type: "image", status: "stored", reason: "", content_type: "image/png", sha256: sha }, { url: "tenants/tn_a/workspaces/ws_a/telegram/chan/10", type: "image", status: "failed", reason: "x" }] }] })) as any,
    () => new Date("2026-10-13T00:00:00Z"));
  const s = await eng.createSource(A, { kind: "telegram", config: { handle: "chan_abc" } });
  await eng.requestCollection(A, "k", { source_ids: [s.id] }); while (await eng.workOnce("w"));
  const [d]: any = await eng.listDocuments(A, {});
  assert.equal(d.extraction.media_total, 2);
  assert.equal(d.extraction.media_missing, 1);
  const fetched: string[] = [];
  const store = async (k: string) => { fetched.push(k); return { bytes, content_type: "application/octet-stream" }; };
  const got: any = await downloadMedia(db, store, A, { document_id: d.id, object_key: key });
  assert.equal(Buffer.from(got.data_base64, "base64").toString(), "PNGDATA");
  assert.equal(got.content_type, "image/png");
  assert.equal(got.sha256, sha);
  for (const [sc, k] of [[B, key], [A, "tenants/tn_b/workspaces/ws_b/telegram/chan/9"], [A, "tenants/tn_a/workspaces/ws_a/telegram/chan/10"], [A, "tenants/tn_a/workspaces/ws_a/../ws_x/a"]] as const) {
    await assert.rejects(downloadMedia(db, store, sc, { document_id: d.id, object_key: k }), (e: any) => e.status === 404);
  }
  assert.equal(fetched.length, 1, "unauthorised requests never reach the object store");
  await assert.rejects(downloadMedia(db, async () => ({ bytes: Buffer.from("tampered"), content_type: "" }), A, { document_id: d.id, object_key: key }), (e: any) => e.status === 502);
  await assert.rejects(downloadMedia(db, null, A, { document_id: d.id, object_key: key }), (e: any) => e.status === 503);
  const url = presignGet({ endpoint: "minio:9000", bucket: "flow-intel", key, accessKey: "ak", secretKey: "sk", now: new Date("2026-10-13T00:00:00Z") });
  assert.match(url, /^http:\/\/minio:9000\/flow-intel\/tenants\/tn_a\/workspaces\/ws_a\/telegram\/chan\/9\?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=ak%2F20261013%2Fus-east-1%2Fs3%2Faws4_request&X-Amz-Date=20261013T000000Z&X-Amz-Expires=60&X-Amz-SignedHeaders=host&X-Amz-Signature=[0-9a-f]{64}$/);
});
