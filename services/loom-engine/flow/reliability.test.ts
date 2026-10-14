// loom collection-reliability ports wired into Flow: conditional feeds, per-entry
// rejection evidence, async Telegram media via the worker's /v1/media, failure classes.
import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, classifyFailure, type Fetcher } from "./engine.ts";
import { fetchTelegram, storeTelegramMedia, type ConnectorItem } from "./connectors.ts";
import { fetchFeed, feedValidatorIdentity } from "../src/lib/server/rss.ts";
import { redirectHeaders } from "../src/lib/server/outbound-policy.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
const resolver = { async lookup() { return [{ address: "93.184.216.34", family: 4 }]; } } as any;
const body = (s: string) => new Response(s).body!;
const rss = (n: number) => `<rss><channel>${Array.from({ length: n }, (_, i) => `<item><guid>g${i}</guid><title>t${i}</title><link>/a/${i}</link></item>`).join("")}</channel></rss>`;

test("fetchFeed: validators are sent only when reusable; 304 keeps them; relative links use the final URL", async () => {
  const seen: Record<string, string>[] = [];
  const transport = async (url: URL, init: any) => {
    seen.push({ path: url.pathname, ...init.headers });
    if (init.headers["If-None-Match"] === '"v1"') return { status: 304, headers: {}, body: body("") };
    if (url.pathname === "/start") return { status: 301, headers: { location: "/feed.xml" }, body: body("") };
    return { status: 200, headers: { etag: '"v1"', "last-modified": "Tue, 13 Oct 2026 00:00:00 GMT" }, body: body(rss(2)) };
  };
  const first = await fetchFeed("https://feed.example/start", "s", "rss", null, { resolver, transport } as any);
  assert.equal(first.items.length, 2); assert.equal(first.items[0].url, "https://feed.example/a/0");
  assert.deepEqual(first.validators, { etag: '"v1"', last_modified: "Tue, 13 Oct 2026 00:00:00 GMT", identity: feedValidatorIdentity("https://feed.example/start", "rss") });
  assert.equal(seen[0]["If-None-Match"], undefined);
  const second = await fetchFeed("https://feed.example/start", "s", "rss", first.validators, { resolver, transport } as any);
  assert.equal(second.not_modified, true); assert.equal(second.items.length, 0); assert.equal(second.validators.etag, '"v1"');
  const other = await fetchFeed("https://feed.example/other", "s", "rss", first.validators, { resolver, transport } as any);
  assert.equal(other.not_modified, false, "validators of another URL are never reused");
  assert.equal(seen.at(-1)!["If-None-Match"], undefined);
});

test("redirect hygiene: validators never follow a redirect; secrets never cross origins", () => {
  const h = { "If-None-Match": "x", "If-Modified-Since": "y", Authorization: "Bearer t", Cookie: "c", "X-Api-Key": "k", Accept: "a" };
  assert.deepEqual(redirectHeaders(h, new URL("https://a.example/1"), new URL("https://a.example/2")), { Authorization: "Bearer t", Cookie: "c", "X-Api-Key": "k", Accept: "a" });
  assert.deepEqual(redirectHeaders(h, new URL("https://a.example/1"), new URL("https://b.example/2")), { Accept: "a" });
});

async function engineWith(fetcher: Fetcher) {
  const db = await pgliteDb(); await migrate(db);
  return { db, eng: new Engine(db, fetcher, () => new Date("2026-10-14T00:00:00Z")) };
}
async function run(eng: Engine, s: typeof A, sourceId: string, key: string) {
  await eng.requestCollection(s, key, { source_ids: [sourceId] }); return eng.workOnce("w");
}

test("engine: validators persist with the cursor, come back on the next run, survive failures, stay per tenant", async () => {
  const calls: any[] = [];
  let mode: "ok" | "304" | "fail" = "ok";
  const { db, eng } = await engineWith(async (_url, _id, src) => {
    calls.push(src?.feed_validators ?? null);
    if (mode === "fail") throw new Error("HTTP 503");
    const v = { etag: '"e1"', last_modified: "", identity: "id1" };
    return mode === "304" ? { items: [], collector: "rss", mode: "live", feed_validators: v, not_modified: true, rejected_entries: 0 }
      : { items: [{ external_id: "1", title: "t", body: "b", url: "https://x/1", published_at: null }], collector: "rss", mode: "live", feed_validators: v, not_modified: false, rejected_entries: 2 };
  });
  const a = await eng.createSource(A, { url: "https://feed.example/a" });
  const b = await eng.createSource(B, { url: "https://feed.example/a" });
  await run(eng, A, a.id, "k1");
  assert.equal(calls[0], null);
  const r1 = (await db.query(`SELECT items_rejected, not_modified FROM flow_collection_runs WHERE source_id=$1`, [a.id])).rows[0];
  assert.equal(r1.items_rejected, 2); assert.equal(r1.not_modified, false);
  mode = "fail"; await run(eng, A, a.id, "k2");
  mode = "304"; await run(eng, A, a.id, "k3");
  assert.deepEqual(calls[1], { etag: '"e1"', last_modified: "", identity: "id1" });
  assert.deepEqual(calls[2], calls[1], "a failed run keeps the committed validators");
  const last = (await db.query(`SELECT not_modified, items_new FROM flow_collection_runs WHERE source_id=$1 ORDER BY created_at DESC, id DESC LIMIT 1`, [a.id])).rows[0];
  assert.equal(last.not_modified, true); assert.equal(last.items_new, 0);
  mode = "ok"; await run(eng, B, b.id, "k1");
  assert.equal(calls[3], null, "tenant B never sees tenant A's validators");
});

const fixtureWorker = (handler: (url: string, body: any, auth: string) => [number, any], calls: any[] = []): typeof fetch => (async (url: any, init: any) => {
  const b = JSON.parse(init.body); calls.push({ url: String(url), b, auth: init.headers.Authorization });
  const [status, reply] = handler(String(url), b, init.headers.Authorization);
  return new Response(JSON.stringify(reply), { status });
}) as any;

test("telegram: history refs are stored through /v1/media with the media token; worker errors keep reasons", async () => {
  const calls: any[] = [];
  const fetchImpl = fixtureWorker((url, b) => {
    if (url.endsWith("/v1/history")) return [200, { items: [
      { external_id: "11", body: "photo post", url: "https://t.me/energynews/11", media_type: "image", media_urls: ["telegram://energynews/11"] },
      { external_id: "12", body: "huge video", url: "https://t.me/energynews/12", media_type: "video", media_urls: ["telegram://energynews/12"] },
      { external_id: "13", body: "text only", url: "https://t.me/energynews/13", media_type: "text", media_urls: [] },
    ], last_id: "13" }];
    if (b.message_id === "11") return [200, { object_key: "telegram/energynews/11", content_type: "image/jpeg", bytes: 10, sha256: "a".repeat(64) }];
    return [413, { error: "media_too_large" }];
  }, calls);
  const batch = await fetchTelegram({ telegramWorkerUrl: "http://telethon:8787", telegramMediaToken: "mt", fetchImpl }, { id: "s", kind: "telegram", url: "energynews", config: { handle: "energynews" }, last_external_id: "10" });
  assert.deepEqual(batch.items.map((i) => i.media?.map((m) => m.status)), [["stored"], ["too_large"], []]);
  assert.equal(batch.items[0].media![0].url, "telegram/energynews/11");
  assert.match(batch.items[1].body, /\[1 media attachment\(s\) not collected: too_large\]/);
  const media = calls.filter((c) => c.url.endsWith("/v1/media"));
  assert.deepEqual(media.map((c) => [c.b, c.auth]), [[{ channel: "energynews", message_id: "11" }, "Bearer mt"], [{ channel: "energynews", message_id: "12" }, "Bearer mt"]]);
});

test("telegram media: no token → disabled without calls; flood wait stops the batch; batch is bounded", async () => {
  const mk = (n: number): ConnectorItem[] => Array.from({ length: n }, (_, i) => ({ external_id: String(i + 1), title: "", body: "", url: "", published_at: null,
    media: [{ url: `telegram://chan_x/${i + 1}`, type: "image", status: "failed", reason: "pending" }] }));
  const calls: any[] = [];
  const none = mk(2); await storeTelegramMedia({ telegramWorkerUrl: "http://w", fetchImpl: fixtureWorker(() => [200, {}], calls) }, none);
  assert.deepEqual(none.map((i) => i.media![0].status), ["disabled", "disabled"]); assert.equal(calls.length, 0);
  const flood = mk(3); let n = 0;
  await storeTelegramMedia({ telegramWorkerUrl: "http://w", telegramMediaToken: "t", fetchImpl: fixtureWorker(() => (++n === 1 ? [429, { error: "flood_wait" }] : [200, { object_key: "telegram/chan_x/9" }]), calls) }, flood);
  assert.equal(n, 1, "no further downloads after a flood wait");
  assert.ok(flood.every((i) => i.media![0].status === "failed" && /flood/.test(i.media![0].reason)));
  const many = mk(12); let m = 0;
  await storeTelegramMedia({ telegramWorkerUrl: "http://w", telegramMediaToken: "t", fetchImpl: fixtureWorker((_u, b) => { m++; return [200, { object_key: `telegram/chan_x/${b.message_id}` }]; }) }, many);
  assert.equal(m, 10); assert.equal(many.filter((i) => i.media![0].status === "stored").length, 10);
  assert.match(many[11].media![0].reason, /budget/);
  const bad = mk(1);
  await storeTelegramMedia({ telegramWorkerUrl: "http://w", telegramMediaToken: "t", fetchImpl: fixtureWorker(() => [200, { object_key: "../../etc/passwd" }]) }, bad);
  assert.equal(bad[0].media![0].status, "failed", "an unexpected object key is never trusted");
});

test("failure classes for the reliability worker's errors", () => {
  assert.equal(classifyFailure("telegram: unavailable"), "transient");
  assert.equal(classifyFailure("telegram: membership_required"), "auth");
  assert.equal(classifyFailure("telegram: FloodWait 30s"), "rate_limited");
  assert.equal(classifyFailure("feed_no_usable_entries"), "permanent");
});
