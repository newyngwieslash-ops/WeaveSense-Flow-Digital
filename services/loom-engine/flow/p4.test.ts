import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, connectorFetcher } from "./engine.ts";
import { fetchTelegram, fetchWechat, extractMainText } from "./connectors.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
const clock = () => new Date("2026-10-13T00:00:00Z");

// Fixture: Telethon worker /v1/history (contract of workers/telethon/server.py). Honors min_id.
function telethonFixture(posts: { id: number; text: string; media?: string[] }[], calls: any[] = [], fail?: () => boolean): typeof fetch {
  return (async (url: any, init: any) => {
    const body = JSON.parse(init.body);
    calls.push({ url: String(url), body });
    if (fail?.()) return new Response(JSON.stringify({ error: "worker down" }), { status: 502 });
    const min = Number(body.min_id);
    const items = posts.filter((p) => p.id > min).map((p) => ({ external_id: `${body.channel}/${p.id}`, title: "", body: p.text, url: `https://t.me/${body.channel}/${p.id}`,
      published_at: "2026-10-12T00:00:00Z", media_urls: p.media ?? [], media_type: p.media?.length ? "image" : "text" }));
    return new Response(JSON.stringify({ items, last_id: String(posts.at(-1)?.id ?? min) }), { status: 200 });
  }) as any;
}
const previewHtml = (ids: number[]) => ids.map((id) => `<div class="tgme_widget_message js-widget_message" data-post="energynews/${id}"><div class="tgme_widget_message_text">Post ${id} about storage</div><time datetime="2026-10-12T0${id % 10}:00:00+00:00"></time></div>`).join("");

async function setup(fetcher: any) {
  const db = await pgliteDb(); await migrate(db);
  return new Engine(db, fetcher, clock);
}

test("telegram via Telethon: incremental by min_id, cursor advances only on commit, media marked unsupported, sample recorded", async () => {
  const posts = [{ id: 10, text: "first" }, { id: 11, text: "second with photo", media: ["https://cdn.t.me/p.jpg"] }];
  const calls: any[] = []; let failNext = false;
  const env = { telegramWorkerUrl: "http://telethon.fixture", fetchImpl: telethonFixture(posts, calls, () => failNext), mode: "fixture" as const };
  const eng = await setup(connectorFetcher(env));
  const src = await eng.createSource(A, { kind: "telegram", config: { handle: "@energynews" } });
  assert.equal(src.url, "https://t.me/s/energynews");
  await eng.requestCollection(A, "k1", { source_ids: [src.id] }); assert.equal((await eng.workOnce("w"))!.items_new, 2);
  assert.equal(calls[0].body.min_id, "0");
  const doc = (await eng.db.query(`SELECT media FROM flow_documents WHERE external_id='energynews/11'`)).rows[0];
  assert.equal(doc.media[0].status, "unsupported");
  const smp = await eng.listConnectorSamples(A);
  assert.equal(smp.length, 1); assert.equal(smp[0].collector, "telethon"); assert.equal(smp[0].mode, "fixture"); assert.equal(smp[0].media_unsupported, 1);
  // A failed run must not move the cursor.
  posts.push({ id: 12, text: "third" }); failNext = true;
  await eng.requestCollection(A, "k2", { source_ids: [src.id] }); assert.equal((await eng.workOnce("w"))!.status, "failed");
  assert.equal((await eng.db.query(`SELECT last_external_id FROM flow_source_cursors WHERE source_id=$1`, [src.id])).rows[0].last_external_id, "11");
  failNext = false;
  await eng.requestCollection(A, "k3", { source_ids: [src.id] }); assert.equal((await eng.workOnce("w"))!.items_new, 1);
  assert.equal(calls.at(-1).body.min_id, "11");
  assert.equal((await eng.db.query(`SELECT last_external_id FROM flow_source_cursors WHERE source_id=$1`, [src.id])).rows[0].last_external_id, "12");
  assert.equal((await eng.listConnectorSamples(B)).length, 0);
});

test("telegram web preview: old posts at or below the cursor are never re-ingested; cursor never moves backwards", async () => {
  const src = { id: "src_x", kind: "telegram", url: "", config: { handle: "energynews" }, last_external_id: "105" };
  const b = await fetchTelegram({ fetchPublicText: async () => previewHtml([101, 104, 105, 106, 107]), mode: "fixture" }, src);
  assert.equal(b.collector, "t.me/s");
  assert.deepEqual(b.items.map((i) => i.external_id.split("/").pop()), ["106", "107"]);
  const none = await fetchTelegram({ fetchPublicText: async () => previewHtml([90, 100]), mode: "fixture" }, src);
  assert.equal(none.items.length, 0);
  await assert.rejects(fetchTelegram({}, { ...src, config: { handle: "../etc" } }), /invalid channel handle/);
});

test("wechat via auth gateway: requires external feed config, bearer token, records backup provider", async () => {
  const seen: any[] = [];
  const atom = `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"><title>OA</title>
    <entry><id>wx-1</id><title>储能招标结果公布</title><link href="https://mp.weixin.qq.com/s/abc"/><updated>2026-10-12T08:00:00Z</updated><content type="html">&lt;p&gt;某储能项目中标&lt;/p&gt;</content></entry></feed>`;
  const fetchImpl = (async (url: any, init: any) => { seen.push({ url: String(url), auth: init.headers.Authorization, redirect: init.redirect });
    return new Response(atom, { status: 200, headers: { "x-loom-provider": "dajiala" } }); }) as any;
  const env = { wechatGatewayUrl: "http://gateway.internal", wechatGatewayToken: "gw-token", fetchImpl, mode: "fixture" as const };
  const eng = await setup(connectorFetcher(env));
  await assert.rejects(eng.createSource(A, { kind: "wechat", config: {} }), (e: any) => e.status === 400);
  const src = await eng.createSource(A, { kind: "wechat", config: { gateway_source_id: "wx_energy_oa" } });
  await eng.requestCollection(A, "k", { source_ids: [src.id] });
  assert.equal((await eng.workOnce("w"))!.items_new, 1);
  assert.equal(seen[0].url, "http://gateway.internal/v1/feeds/wx_energy_oa.atom");
  assert.equal(seen[0].auth, "Bearer gw-token"); assert.equal(seen[0].redirect, "error");
  const smp = await eng.listConnectorSamples(A);
  assert.equal(smp[0].kind, "wechat"); assert.equal(smp[0].provider, "dajiala");
  await assert.rejects(fetchWechat({ mode: "fixture" }, { id: "s", kind: "wechat", url: "", config: { gateway_source_id: "x" } }), /gateway not configured/);
});

test("revoked source: scheduled windows stop, queued runs cancelled, nothing fetched", async () => {
  let fetched = 0;
  const eng = await setup(async () => { fetched++; return []; });
  const src = await eng.createSource(A, { url: "https://e.com/f" });
  await eng.createSchedule(A, "user:u", "p", { source_ids: [src.id], interval_seconds: 60, start_at: "2026-10-12T00:00:00Z" });
  await eng.requestCollection(A, "q1", { source_ids: [src.id] });
  await assert.rejects(eng.revokeSource(B, src.id), (e: any) => e.status === 404);
  const r = await eng.revokeSource(A, src.id, "connection revoked");
  assert.equal(r.cancelled_runs, 1);
  assert.equal(await eng.schedulerTick(), 0);
  assert.equal(await eng.workOnce("w"), null);
  assert.equal(fetched, 0);
});

test("on-demand extraction: main text becomes a new revision; old revision kept", async () => {
  const eng = await setup(async () => [{ external_id: "1", title: "T", body: "short teaser", url: "https://news.example/a", published_at: null }]);
  const src = await eng.createSource(A, { url: "https://e.com/f" });
  await eng.requestCollection(A, "k", { source_ids: [src.id] }); await eng.workOnce("w");
  const doc = (await eng.listDocuments(A, {}))[0];
  eng.extractor = async () => `<html><head><title>Full story</title></head><body><nav>menu menu</nav><article><p>The battery plant in Jiangsu started production this week with 10 GWh capacity.</p><p>Officials expect exports to rise next year according to the operator.</p></article><footer>copyright</footer></body></html>`;
  const r = await eng.extractDocument(A, { document_id: doc.id });
  assert.equal(r.changed, true); assert.equal(r.revision, 2);
  assert.match((await eng.getRevision(A, doc.id, 2)).body, /10 GWh/);
  assert.doesNotMatch((await eng.getRevision(A, doc.id, 2)).body, /menu|copyright/);
  assert.equal((await eng.getRevision(A, doc.id, 1)).body, "short teaser");
  await assert.rejects(eng.extractDocument(B, { document_id: doc.id }), (e: any) => e.status === 404);
  assert.equal(extractMainText("<p>tiny</p>").text, "tiny");
});
