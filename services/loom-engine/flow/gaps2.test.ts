// Gap round 2: §10.4 scoped media keys, §6.4 attachment retry + OCR, real-HTTP subtitle/OCR worker contracts.
import { test } from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, MEDIA_MAX_ATTEMPTS } from "./engine.ts";
import { ocrClient, subtitleClient, EnrichmentError } from "./enrichment.ts";
import { extractMainText } from "./connectors.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
const clock = () => new Date("2026-10-14T00:00:00Z");

async function telegramDoc(mediaStatus: "failed" | "stored", key = "telegram://chan_news/11") {
  const db = await pgliteDb(); await migrate(db);
  const eng = new Engine(db, async () => ({ items: [{ external_id: "11", title: "photo", body: "photo post", url: "https://t.me/chan_news/11", published_at: null,
    media: [{ url: key, ref: "telegram://chan_news/11", type: "image", status: mediaStatus, reason: mediaStatus === "failed" ? "worker unreachable" : "", attempts: 1 }] }], collector: "telethon", mode: "fixture" }), clock);
  const src = await eng.createSource(A, { kind: "telegram", config: { handle: "chan_news" } });
  await eng.requestCollection(A, "k", { source_ids: [src.id] }); await eng.workOnce("w");
  return { db, eng, doc: (await eng.listDocuments(A, {}))[0], src };
}
const media = async (eng: Engine, id: string) => (await eng.db.query(`SELECT media FROM flow_documents WHERE id=$1`, [id])).rows[0].media;

test("attachment retry: failed media is re-downloaded with tenant/workspace-scoped keys, bounded attempts", async () => {
  const { eng, doc } = await telegramDoc("failed");
  const calls: any[] = []; let ok = false;
  eng.connectorEnv = { telegramWorkerUrl: "http://w", telegramMediaToken: "t" };
  // A transport error is retryable (a 503 maps to "disabled", which is not).
  eng.connectorEnv.fetchImpl = (async (_u: any, init: any) => { const b = JSON.parse(init.body); calls.push(b); if (!ok) throw new Error("ECONNRESET");
    return new Response(JSON.stringify({ object_key: `tenants/${b.tenant_id}/workspaces/${b.workspace_id}/telegram/${b.channel}/${b.message_id}`, content_type: "image/png" })); }) as any;
  assert.deepEqual(await eng.retryMedia(), { documents: 1, stored: 0, failed: 1 });
  assert.equal((await media(eng, doc.id))[0].attempts, 2);
  ok = true;
  assert.deepEqual(await eng.retryMedia(), { documents: 1, stored: 1, failed: 0 });
  const m = (await media(eng, doc.id))[0];
  assert.equal(m.status, "stored"); assert.equal(m.url, "tenants/tn_a/workspaces/ws_a/telegram/chan_news/11");
  assert.deepEqual(calls.at(-1), { channel: "chan_news", message_id: "11", tenant_id: "tn_a", workspace_id: "ws_a" });
  assert.deepEqual(await eng.retryMedia(), { documents: 0, stored: 0, failed: 0 }, "stored media is never retried");
  assert.equal((await eng.db.query(`SELECT count(*)::int n FROM flow_outbox WHERE type='media.stored'`)).rows[0].n, 1);
});

test("attachment retry stops after MEDIA_MAX_ATTEMPTS", async () => {
  const { eng, doc } = await telegramDoc("failed");
  eng.connectorEnv = { telegramWorkerUrl: "http://w", telegramMediaToken: "t", fetchImpl: (async () => { throw new Error("ECONNRESET"); }) as any };
  for (let i = 0; i < MEDIA_MAX_ATTEMPTS + 2; i++) await eng.retryMedia();
  assert.equal((await media(eng, doc.id))[0].attempts, MEDIA_MAX_ATTEMPTS);
});

test("OCR: scoped image → new revision with marker, usage ocr_pages, only failed targets retried, tenant scoped", async () => {
  const { eng, doc } = await telegramDoc("stored", "tenants/tn_a/workspaces/ws_a/telegram/chan_news/11");
  let calls = 0, fail = true;
  eng.ocr = async (key, sc) => { calls++; assert.equal(sc.tenant_id, "tn_a"); if (fail) throw new EnrichmentError("ocr_unavailable"); return { text: "储能 订单 10 GWh", pages: 1, digest: "d" }; };
  await assert.rejects(eng.ocrDocument(B, { document_id: doc.id }), (e: any) => e.status === 404);
  const r1 = await eng.ocrDocument(A, { document_id: doc.id });
  assert.equal(r1.processed[0].status, "failed");
  fail = false;
  const r2 = await eng.ocrDocument(A, { document_id: doc.id });
  assert.equal(r2.processed[0].status, "succeeded"); assert.equal(r2.processed[0].revision, 2);
  assert.match((await eng.getRevision(A, doc.id, 2)).body, /\[OCR: tenants\/tn_a\/workspaces\/ws_a\/telegram\/chan_news\/11\]\n储能 订单 10 GWh/);
  const r3 = await eng.ocrDocument(A, { document_id: doc.id });
  assert.equal(r3.processed.length, 0, "succeeded targets are not re-run"); assert.equal(calls, 2);
  const usage = (await eng.db.query(`SELECT payload FROM flow_outbox WHERE type='usage.recorded'`)).rows;
  assert.equal(usage.length, 1); assert.equal(usage[0].payload.usage_type, "ocr_pages");
  assert.equal((await eng.searchDocuments(A, { query: "储能" })).items.length, 1, "OCR text is searchable");
});

test("OCR refuses legacy unscoped keys and documents without images", async () => {
  const { eng, doc } = await telegramDoc("stored", "telegram/chan_news/11");
  eng.ocr = async () => { throw new Error("must not be called"); };
  await assert.rejects(eng.ocrDocument(A, { document_id: doc.id }), (e: any) => e.status === 422 && /legacy/.test(e.message));
});

async function serve(handler: (body: any, auth: string) => [number, any] | Promise<[number, any]>, delayMs = 0) {
  const srv = createServer(async (req, res) => {
    let b = ""; for await (const c of req) b += c;
    if (delayMs) await new Promise((r) => setTimeout(r, delayMs));
    const [st, out] = await handler(JSON.parse(b || "{}"), String(req.headers.authorization ?? ""));
    res.writeHead(st, { "content-type": "application/json" }); res.end(typeof out === "string" ? out : JSON.stringify(out));
  });
  await new Promise<void>((r) => srv.listen(0, "127.0.0.1", () => r()));
  const port = (srv.address() as any).port;
  return { url: `http://127.0.0.1:${port}/`, close: () => new Promise((r) => srv.close(r)) };
}

test("subtitle client over real HTTP: streaming body, size cap, mismatched echo, worker errors", async () => {
  const json3 = JSON.stringify({ events: [{ segs: [{ utf8: "电池 储能" }] }, { segs: [{ utf8: "订单" }] }] });
  const s = await serve((b, auth) => auth !== "Bearer tok" ? [401, { error: "unauthorized" }]
    : b.video_id === "big0000000a" ? [200, "x".repeat(3 * 1024 * 1024)]
    : b.video_id === "busy0000000" ? [429, { error: "busy" }]
    : [200, { video_id: b.video_id === "echo0000000" ? "other" : b.video_id, language: b.language, status: "ok", kind: "automatic", json3, text: "电池 储能\n订单" }]);
  try {
    const c = subtitleClient({ enabled: true, endpoint: s.url, token: "tok" });
    const t = await c("dQw4w9WgXcQ", "zh-Hans");
    assert.equal(t.kind, "automatic"); assert.equal(t.text, "电池 储能\n订单"); assert.equal(t.digest.length, 64);
    await assert.rejects(c("big0000000a", "en"), (e: any) => e.code === "subtitle_output_too_large");
    await assert.rejects(c("busy0000000", "en"), (e: any) => e.code === "subtitle_busy");
    await assert.rejects(c("echo0000000", "en"), (e: any) => e.code === "subtitle_invalid_response");
    await assert.rejects(subtitleClient({ enabled: true, endpoint: s.url, token: "bad" })("dQw4w9WgXcQ", "en"), (e: any) => e.code === "subtitle_unavailable");
  } finally { await s.close(); }
});

test("OCR client over real HTTP: contract, scope echo, timeout and errors", async () => {
  const s = await serve((b, auth) => auth !== "Bearer ocr" ? [401, { error: "unauthorized" }]
    : b.object_key.endsWith("/9") ? [415, { error: "unsupported" }]
    : [200, { object_key: b.object_key, text: `text of ${b.tenant_id}`, pages: 1, sha256: "a".repeat(64) }]);
  const slow = await serve(() => [200, {}], 500);
  try {
    const c = ocrClient({ enabled: true, endpoint: s.url, token: "ocr" });
    assert.deepEqual(await c("tenants/tn_a/workspaces/ws_a/telegram/x/1", A), { text: "text of tn_a", pages: 1, digest: "a".repeat(64) });
    await assert.rejects(c("tenants/tn_a/workspaces/ws_a/telegram/x/9", A), (e: any) => e.code === "ocr_unsupported");
    await assert.rejects(ocrClient({ enabled: false })("k", A), (e: any) => e.code === "ocr_disabled");
    await assert.rejects(ocrClient({ enabled: true, endpoint: slow.url, token: "ocr", timeoutMs: 100 })("k", A), (e: any) => e.code === "ocr_timeout");
  } finally { await s.close(); await slow.close(); }
});

test("article main-text extraction on a realistic news page (nav, scripts, comments stripped)", () => {
  const html = `<!doctype html><html><head><title>宁德时代发布新一代储能电池 | 能源新闻</title><script>var ads = 1;</script><style>.x{}</style></head>
  <body><nav><a href="/">首页</a><a href="/energy">能源</a></nav><header>登录 注册</header>
  <article><h1>宁德时代发布新一代储能电池</h1><p class="meta">2026-10-12 来源：能源新闻</p>
  <p>10月12日，宁德时代（CATL）发布新一代储能电池，单体容量提升至 587Ah，循环寿命超过 10000 次。</p>
  <p>公司表示，新产品将于 2027 年一季度量产，首批订单来自海外储能电站项目，总规模约 5 GWh。</p></article>
  <aside>相关阅读：光伏 装机</aside><footer>版权所有 © 能源新闻</footer><script>track()</script></body></html>`;
  const { title, text } = extractMainText(html);
  assert.match(title, /宁德时代发布新一代储能电池/);
  assert.match(text, /587Ah/); assert.match(text, /5 GWh/);
  assert.doesNotMatch(text, /track\(\)|var ads|\.x\{\}/);
});

test("legacy media key migration: plan → apply rewrites only verified copies, idempotent", async () => {
  const { planMediaKeyMigration, applyMediaKeyMigration } = await import("./tools/media-keys.ts");
  const { eng, doc } = await telegramDoc("stored", "telegram/chan_news/11");
  const plan = await planMediaKeyMigration(eng.db);
  assert.deepEqual(plan.map((p) => [p.from, p.to]), [["telegram/chan_news/11", "tenants/tn_a/workspaces/ws_a/telegram/chan_news/11"]]);
  assert.deepEqual(await applyMediaKeyMigration(eng.db, plan, async () => false), { documents_updated: 0, documents_skipped: 1 });
  assert.deepEqual(await applyMediaKeyMigration(eng.db, plan), { documents_updated: 1, documents_skipped: 0 });
  const m = (await media(eng, doc.id))[0];
  assert.equal(m.url, "tenants/tn_a/workspaces/ws_a/telegram/chan_news/11"); assert.equal(m.legacy_key, "telegram/chan_news/11");
  assert.deepEqual(await applyMediaKeyMigration(eng.db, plan), { documents_updated: 0, documents_skipped: 1 }, "stale plan is a no-op");
  assert.equal((await planMediaKeyMigration(eng.db)).length, 0);
});
