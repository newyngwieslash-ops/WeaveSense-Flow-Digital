// §13.3 legacy Loom import: old_id → new_id mapping, dry run, idempotent re-run, search over imported docs.
import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine } from "./engine.ts";
import { importLegacy, mapLegacySource } from "./tools/legacy-import.ts";

async function legacyDb() {
  const db = await pgliteDb();
  await db.query(`CREATE TABLE sources (id text primary key, platform text not null, name text not null, handle text not null default '', url text not null default '',
      feed_url text not null default '', enabled boolean not null default true, adapter text not null default 'rss', created_at timestamptz not null default now())`);
  await db.query(`CREATE TABLE items (id text primary key, source_id text not null, platform text not null, external_id text not null, title text not null default '',
      body text not null default '', url text not null default '', published_at timestamptz)`);
  await db.query(`CREATE TABLE keywords (id text primary key, phrase text not null)`);
  await db.query(`INSERT INTO sources (id, platform, name, feed_url, adapter) VALUES ('s1','rss','Energy feed','https://example.com/feed.xml','rss')`);
  await db.query(`INSERT INTO sources (id, platform, name, handle) VALUES ('s2','telegram','TG','@chan_news')`);
  await db.query(`INSERT INTO sources (id, platform, name) VALUES ('s3','wechat','WX')`);
  await db.query(`INSERT INTO items (id, source_id, platform, external_id, title, body, published_at) VALUES
      ('i1','s1','rss','g-1','储能 招标','某储能项目开标，规模 200MWh','2026-09-01T00:00:00Z'),
      ('i2','s2','telegram','77','tg post','hello world',NULL),
      ('i3','s3','wechat','w1','wx','skipped',NULL)`);
  await db.query(`INSERT INTO keywords VALUES ('k1','储能')`);
  return db;
}

test("mapLegacySource maps rss/telegram and skips wechat", () => {
  assert.deepEqual(mapLegacySource({ platform: "rss", adapter: "rss", feed_url: "https://a.example/f", name: "A" }), { kind: "rss", url: "https://a.example/f", title: "A" });
  assert.deepEqual(mapLegacySource({ platform: "telegram", handle: "https://t.me/s/chan_news", name: "T" }), { kind: "telegram", title: "T", config: { handle: "chan_news" } });
  assert.ok("skip" in mapLegacySource({ platform: "wechat" }));
});

test("legacy import: dry run writes nothing; apply maps ids; re-run is idempotent", async () => {
  const legacy = await legacyDb();
  const db = await pgliteDb(); await migrate(db);
  const s = { tenant_id: "tn_a", workspace_id: "ws_a" };
  const dry = await importLegacy(legacy, db, s);
  assert.equal(dry.dry_run, true);
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_sources`)).rows[0].n, 0);
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_legacy_id_map`)).rows[0].n, 0);

  const rep = await importLegacy(legacy, db, s, { apply: true });
  assert.deepEqual(rep.counts.sources, { imported: 2, skipped: 1 });
  assert.deepEqual(rep.counts.items, { imported: 2, skipped: 1 });
  assert.deepEqual(rep.counts.keywords, { skipped: 1 });
  const map = (await db.query(`SELECT * FROM flow_legacy_id_map WHERE legacy_table='items' AND old_id='i1'`)).rows[0];
  assert.match(map.new_id, /^doc_/);
  const src = (await db.query(`SELECT status, paused_reason FROM flow_sources`)).rows;
  assert.ok(src.every((r: any) => r.status === "paused" && r.paused_reason === "legacy_import"), "imported sources start paused");
  const found = await new Engine(db).searchDocuments(s, { query: "储能" });
  assert.ok(JSON.stringify(found).includes(map.new_id), "imported document searchable");

  const again = await importLegacy(legacy, db, s, { apply: true });
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_documents`)).rows[0].n, 2, "no duplicates");
  assert.ok(again.entries.filter((e) => e.table === "items" && e.status === "imported").every((e) => e.reason === "already mapped"));
  // Another workspace gets its own copy and mapping.
  await importLegacy(legacy, db, { tenant_id: "tn_a", workspace_id: "ws_b" }, { apply: true });
  assert.equal((await db.query(`SELECT count(DISTINCT new_id)::int n FROM flow_legacy_id_map WHERE legacy_table='items' AND old_id='i1'`)).rows[0].n, 2);
  await legacy.close(); await db.close();
});
