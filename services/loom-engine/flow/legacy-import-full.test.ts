// §13.3 full legacy import: analyses, knowledge notes, schedules, objects (keywords skipped).
// PGlite always; real PostgreSQL (FORCE RLS, non-superuser) when FLOW_TEST_PG_ADMIN_URL/FLOW_TEST_PG_APP_ROLE are set.
import { test } from "node:test";
import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { pgliteDb, pgDb, migrate, type Db } from "./db.ts";
import { importLegacy } from "./tools/legacy-import.ts";

const LEGACY = [
  `CREATE TABLE sources (id text primary key, platform text not null, name text not null, handle text not null default '', url text not null default '',
     feed_url text not null default '', enabled boolean not null default true, adapter text not null default 'rss', interval_minutes int default 30, created_at timestamptz not null default now())`,
  `CREATE TABLE items (id text primary key, source_id text not null, platform text not null, external_id text not null, title text not null default '',
     body text not null default '', url text not null default '', published_at timestamptz, media_type text, media_urls jsonb default '[]')`,
  `CREATE TABLE keywords (id text primary key, phrase text not null)`,
  `CREATE TABLE analyses (id text primary key, item_id text, summary text, topics jsonb, entities jsonb, insights text, tags jsonb, model text, created_at timestamptz default now())`,
  `CREATE TABLE knowledge_notes (id text primary key, title text, content text, kind text, item_ids jsonb, created_at timestamptz default now())`,
  `INSERT INTO sources (id, platform, name, feed_url, interval_minutes) VALUES ('s1','rss','Energy','https://example.com/f.xml',15),('s2','rss','NoInt','https://example.com/g.xml',0),('s3','wechat','WX','',30)`,
  `INSERT INTO items (id, source_id, platform, external_id, title, body, media_urls) VALUES
     ('i1','s1','rss','g1','储能','储能招标 200MWh','["https://cdn.example.com/a.jpg","https://cdn.example.com/b.png?x=1"]'),
     ('i2','s2','rss','g2','t2','b2','[]'),('i3','s3','wechat','w1','wx','x','["https://cdn.example.com/w.jpg"]')`,
  `INSERT INTO keywords VALUES ('k1','储能')`,
  `INSERT INTO analyses (id, item_id, summary, topics, tags, model, insights) VALUES ('a1','i1','招标摘要','["储能"]','["t"]','gpt','洞察'),('a2','i3','wx','[]','[]','m',NULL)`,
  `INSERT INTO knowledge_notes (id, title, content, kind, item_ids) VALUES ('n1','笔记','内容','note','["i1","i2","i9"]')`,
];

async function scenario(legacy: Db, db: Db) {
  for (const q of LEGACY) await legacy.query(q);
  const s = { tenant_id: "tn_a", workspace_id: "ws_a" };
  const count = async (t: string) => (await db.query(`SELECT count(*)::int n FROM ${t}`)).rows[0].n;

  const dry = await importLegacy(legacy, db, s);
  assert.equal(dry.dry_run, true);
  assert.deepEqual(dry.counts.analyses, { imported: 1, skipped: 1 });
  assert.deepEqual(dry.counts.knowledge_notes, { imported: 1 });
  assert.deepEqual(dry.counts.objects, { imported: 2, skipped: 1 });
  assert.deepEqual(dry.counts.schedules, { imported: 1, skipped: 2 });
  for (const t of ["flow_sources", "flow_documents", "flow_schedules", "flow_legacy_archive", "flow_legacy_id_map"]) assert.equal(await count(t), 0, `dry run wrote ${t}`);

  const copies: string[] = [];
  const rep = await importLegacy(legacy, db, s, { apply: true, copyObject: async (from, key) => { if (from.includes("b.png")) throw new Error("404"); copies.push(key); } });
  assert.deepEqual(rep.counts.keywords, { skipped: 1 });
  assert.deepEqual(rep.counts.analyses, { imported: 1, skipped: 1 });
  assert.deepEqual(rep.counts.schedules, { imported: 1, skipped: 2 });
  const m = async (t: string, id: string) => (await db.query(`SELECT new_id FROM flow_legacy_id_map WHERE legacy_table=$1 AND old_id=$2 AND workspace_id='ws_a'`, [t, id])).rows[0]?.new_id;
  const doc1 = await m("items", "i1");
  const a1 = (await db.query(`SELECT * FROM flow_legacy_archive WHERE id=$1`, [await m("analyses", "a1")])).rows[0];
  assert.equal(a1.kind, "analysis"); assert.deepEqual(a1.document_ids, [doc1]); assert.equal(a1.payload.verified, false);
  assert.equal(await count("flow_reports"), 0, "analyses never become evidence-bound reports");
  const n1 = (await db.query(`SELECT * FROM flow_legacy_archive WHERE id=$1`, [await m("knowledge_notes", "n1")])).rows[0];
  assert.equal(n1.document_ids.length, 2); assert.deepEqual(n1.payload.unresolved_item_ids, ["i9"]);
  const sc = (await db.query(`SELECT * FROM flow_schedules WHERE id=$1`, [await m("schedules", "s1")])).rows[0];
  assert.equal(sc.interval_seconds, 900); assert.equal(sc.enabled, false); assert.equal(sc.state, "disabled");
  assert.deepEqual(sc.source_ids, [await m("sources", "s1")]);
  const objs = (await db.query(`SELECT payload FROM flow_legacy_archive WHERE kind='object' ORDER BY old_id`)).rows.map((r: any) => r.payload);
  assert.equal(objs.length, 2);
  assert.ok(objs.every((o: any) => o.target_key.startsWith(`tenants/tn_a/workspaces/ws_a/legacy/${doc1}/`)));
  assert.deepEqual(objs.map((o: any) => o.copy_status), ["copied", "failed"]);
  assert.equal(copies.length, 1);

  // Idempotent: nothing new on re-run, no second copy.
  const before = await Promise.all(["flow_documents", "flow_schedules", "flow_legacy_archive", "flow_legacy_id_map"].map(count));
  const again = await importLegacy(legacy, db, s, { apply: true, copyObject: async (_f, k) => { copies.push(k); } });
  assert.deepEqual(await Promise.all(["flow_documents", "flow_schedules", "flow_legacy_archive", "flow_legacy_id_map"].map(count)), before);
  assert.equal(copies.length, 1);
  assert.ok(again.entries.filter((e) => e.status === "imported").every((e) => e.reason === "already mapped"));
}

test("legacy import (PGlite): analyses, notes, schedules, objects; dry run; idempotent", async () => {
  const legacy = await pgliteDb(); const db = await pgliteDb(); await migrate(db);
  await scenario(legacy, db);
  await legacy.close(); await db.close();
});

const admin = process.env.FLOW_TEST_PG_ADMIN_URL ?? "";
const [appUser, appPass] = (process.env.FLOW_TEST_PG_APP_ROLE ?? "").split(":");
test("legacy import (real PostgreSQL, FORCE RLS app role)", { skip: !admin || !appUser ? "FLOW_TEST_PG_ADMIN_URL / FLOW_TEST_PG_APP_ROLE not set" : false }, async () => {
  const sql = (url: string, s: string) => execFileSync("psql", [url, "-v", "ON_ERROR_STOP=1", "-Atc", s], { encoding: "utf8" });
  const urls = ["legacy_src_t", "legacy_dst_t"].map((name) => {
    sql(admin, `DROP DATABASE IF EXISTS ${name}`); sql(admin, `CREATE DATABASE ${name} OWNER ${appUser}`);
    const u = new URL(admin); u.pathname = `/${name}`; u.username = appUser; u.password = appPass; return u.toString();
  });
  const legacy = await pgDb(urls[0]); const db = await pgDb(urls[1]); await migrate(db);
  await scenario(legacy, db);
  await legacy.close(); await db.close();
});
