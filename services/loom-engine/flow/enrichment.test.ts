import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine } from "./engine.ts";
import { articleHostsFromEnv, documentVideoId, EnrichmentError, subtitleClient, enrichmentStatus } from "./enrichment.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };

async function withDoc(url: string, externalId = "1", body = "short teaser") {
  const db = await pgliteDb(); await migrate(db);
  const eng = new Engine(db, async () => [{ external_id: externalId, title: "T", body, url, published_at: null }], () => new Date("2026-10-14T00:00:00Z"));
  const src = await eng.createSource(A, { url: "https://e.com/f" });
  await eng.requestCollection(A, "k", { source_ids: [src.id] }); await eng.workOnce("w");
  return { eng, doc: (await eng.listDocuments(A, {}))[0] };
}
const json3 = JSON.stringify({ events: [{ segs: [{ utf8: "hello battery" }] }, { segs: [{ utf8: "storage world" }] }] });
const worker = (reply: any, status = 200, calls: any[] = []): typeof fetch => (async (url: any, init: any) => {
  calls.push({ url: String(url), auth: init.headers.authorization, body: JSON.parse(init.body), redirect: init.redirect });
  return new Response(JSON.stringify(typeof reply === "function" ? reply(JSON.parse(init.body)) : reply), { status });
}) as any;

test("reader extraction: upstream article candidate becomes a tenant-scoped revision", async () => {
  const { eng, doc } = await withDoc("https://news.example/a");
  const seen: string[] = [];
  eng.articleReader = async (u) => { seen.push(u); return { title: "Full", body: "The battery plant in Jiangsu started production this week with 10 GWh capacity.", sourceUrl: u, digest: "d" }; };
  await assert.rejects(eng.extractDocument(B, { document_id: doc.id, via: "reader" }), (e: any) => e.status === 404);
  assert.deepEqual(seen, [], "foreign tenant never triggers an outbound read");
  const r: any = await eng.extractDocument(A, { document_id: doc.id, via: "reader" });
  assert.equal(r.changed, true); assert.equal(r.revision, 2); assert.equal(r.via, "reader");
  assert.match((await eng.getRevision(A, doc.id, 2)).body, /10 GWh/);
  assert.equal((await eng.getRevision(A, doc.id, 1)).body, "short teaser");
  const again: any = await eng.extractDocument(A, { document_id: doc.id, via: "reader" });
  assert.equal(again.changed, false);
  await assert.rejects(eng.extractDocument(A, { document_id: doc.id, via: "bogus" as any }), (e: any) => e.status === 400);
});

test("reader extraction: disabled/not-allowed map to 422, upstream failures to 502", async () => {
  const { eng, doc } = await withDoc("https://news.example/a");
  delete process.env.ARTICLE_READER_ENABLED;
  await assert.rejects(eng.extractDocument(A, { document_id: doc.id, via: "reader" }), (e: any) => e.status === 422 && e.message === "article_disabled");
  eng.articleReader = async () => { throw new Error("article_target_not_allowed"); };
  await assert.rejects(eng.extractDocument(A, { document_id: doc.id, via: "reader" }), (e: any) => e.status === 422);
  eng.articleReader = async () => { throw new Error("socket hang up"); };
  await assert.rejects(eng.extractDocument(A, { document_id: doc.id, via: "reader" }), (e: any) => e.status === 502 && e.message === "article_unavailable");
  assert.deepEqual([...articleHostsFromEnv("News.Example, b.org")], ["news.example", "b.org"]);
  assert.equal(articleHostsFromEnv("a.com,*.b.com").size, 0, "one wildcard disables the allowlist");
});

test("youtube transcript: worker contract, appended revision, idempotent, tenant scoped", async () => {
  const { eng, doc } = await withDoc("https://www.youtube.com/watch?v=dQw4w9WgXcQ", "yt:video:dQw4w9WgXcQ", "video description");
  const calls: any[] = [];
  eng.subtitles = subtitleClient({ enabled: true, endpoint: "http://subtitle-worker:8080/", token: "t0k", fetchImpl: worker((b: any) =>
    ({ video_id: b.video_id, language: b.language, status: "ok", kind: "manual", json3, text: "hello battery\nstorage world" }), 200, calls) });
  await assert.rejects(eng.transcribeDocument(B, { document_id: doc.id }), (e: any) => e.status === 404);
  assert.equal(calls.length, 0);
  const r: any = await eng.transcribeDocument(A, { document_id: doc.id, language: "en" });
  assert.equal(r.changed, true); assert.equal(r.kind, "manual"); assert.equal(r.digest.length, 64);
  assert.deepEqual(calls[0], { url: "http://subtitle-worker:8080/v1/transcript", auth: "Bearer t0k", body: { video_id: "dQw4w9WgXcQ", language: "en" }, redirect: "error" });
  const body = (await eng.getRevision(A, doc.id, 2)).body;
  assert.match(body, /^video description\n\n\[字幕: en; manual\]\nhello battery\nstorage world$/);
  assert.equal(((await eng.transcribeDocument(A, { document_id: doc.id, language: "en" })) as any).changed, false);
  const hits = await eng.searchDocuments(A, { query: "storage world" } as any);
  assert.ok(JSON.stringify(hits).includes(doc.id), "transcript text is searchable");
  await assert.rejects(eng.transcribeDocument(A, { document_id: doc.id, language: "fr" }), (e: any) => e.status === 400);
});

test("youtube transcript: unsupported urls, no_subtitles, disabled and bad worker replies", async () => {
  const { eng, doc } = await withDoc("https://news.example/a");
  eng.subtitles = subtitleClient({ enabled: true, endpoint: "http://w/", token: "t", fetchImpl: worker({}) });
  await assert.rejects(eng.transcribeDocument(A, { document_id: doc.id }), (e: any) => e.status === 422 && e.message === "subtitle_unsupported");
  assert.equal(documentVideoId({ url: "https://youtu.be/dQw4w9WgXcQ" }), "dQw4w9WgXcQ");
  assert.throws(() => documentVideoId({ url: "https://youtu.be/dQw4w9WgXcQ", external_id: "yt:video:AAAAAAAAAAA" }), EnrichmentError);
  const v = "dQw4w9WgXcQ";
  const none = subtitleClient({ enabled: true, endpoint: "http://w/", token: "t", fetchImpl: worker({ video_id: v, language: "en", status: "no_subtitles", kind: "none", text: "" }) });
  assert.equal((await none(v, "en")).status, "no_subtitles");
  await assert.rejects(subtitleClient({ enabled: false })(v, "en"), (e: any) => e.code === "subtitle_disabled");
  await assert.rejects(subtitleClient({ enabled: true, endpoint: "http://w/x", token: "t" })(v, "en"), (e: any) => e.code === "subtitle_unavailable");
  const mismatch = subtitleClient({ enabled: true, endpoint: "http://w/", token: "t", fetchImpl: worker({ video_id: v, language: "en", status: "ok", kind: "manual", json3, text: "tampered" }) });
  await assert.rejects(mismatch(v, "en"), (e: any) => e.code === "subtitle_invalid_response");
  const busy = subtitleClient({ enabled: true, endpoint: "http://w/", token: "t", fetchImpl: worker({ error: "busy" }, 503) });
  await assert.rejects(busy(v, "en"), (e: any) => e.code === "subtitle_busy");
  assert.equal(enrichmentStatus("subtitle_busy"), 429);
  const big = subtitleClient({ enabled: true, endpoint: "http://w/", token: "t", fetchImpl: (async () => new Response("x".repeat(3 * 1024 * 1024))) as any });
  await assert.rejects(big(v, "en"), (e: any) => e.code === "subtitle_output_too_large");
});
