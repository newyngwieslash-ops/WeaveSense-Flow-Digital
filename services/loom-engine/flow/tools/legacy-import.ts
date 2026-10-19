// §13.3 Loom legacy import: copies a legacy Loom database (tables sources, items, keywords)
// into one Flow tenant/workspace and records every old_id → new_id in flow_legacy_id_map.
//
//   LEGACY_DATABASE_URL=postgres://… DATABASE_URL=postgres://… \
//   node --experimental-strip-types flow/tools/legacy-import.ts <tenant_id> <workspace_id> [--apply] [--activate]
//
// Without --apply it is a dry run (prints the plan; writes nothing). Imported sources start
// paused (paused_reason=legacy_import) unless --activate, so nothing collects by surprise.
// Items become documents (revision 1, chunked for search). Keywords are reported as skipped:
// they map to monitors, which need a preview + explicit approval in the product.
// Legacy analyses and knowledge_notes become read-only archive rows (flow_legacy_archive),
// linked to the mapped documents; they are not evidence-bound reports. Each source's
// interval_minutes becomes a Flow schedule that starts DISABLED (state=disabled,
// blocked_reason=legacy_import) until connection and budget are re-bound. Every media URL in
// items.media_urls becomes an 'object' archive row with a tenant/workspace-scoped target key;
// bytes are copied only when a copyObject hook is given (library API); the CLI leaves
// copy_status=pending and the operator copies (mc cp legacy_url -> target_key), as in media-keys.ts.
// AI runs/jobs are not imported (archived history is not a resumable run, §13.3).
import { createHash } from "node:crypto";
import type { Db, Q } from "../db.ts";
import { Engine, newId } from "../engine.ts";

export type Scope = { tenant_id: string; workspace_id: string };
export type ImportEntry = { table: string; old_id: string; new_id: string | null; status: "imported" | "skipped"; reason?: string };
export type ImportReport = { import_id: string; dry_run: boolean; entries: ImportEntry[]; counts: Record<string, Record<string, number>> };

const sha = (s: string) => createHash("sha256").update(s).digest("hex");
const HANDLE = /^[A-Za-z0-9_]{4,64}$/;

/** Maps a legacy source row to a Flow source input, or a skip reason. */
export function mapLegacySource(r: any): { kind: string; url?: string; title: string; config?: any } | { skip: string } {
  const platform = String(r.platform ?? "").toLowerCase();
  const adapter = String(r.adapter ?? "").toLowerCase();
  const title = String(r.name ?? "");
  if (platform === "telegram") {
    const h = String(r.handle || r.url || "").replace(/^@/, "").replace(/^https?:\/\/t\.me\/(s\/)?/, "").split(/[/?#]/)[0];
    return HANDLE.test(h) ? { kind: "telegram", title, config: { handle: h } } : { skip: "telegram source without a valid channel handle" };
  }
  if (platform === "wechat") return { skip: "wechat sources need a gateway_source_id; re-create via the wechat gateway" };
  const url = String(r.feed_url || r.url || "");
  if ((adapter === "rss" || platform === "rss" || r.feed_url) && /^https?:\/\//.test(url)) return { kind: "rss", url, title };
  return { skip: `unsupported legacy platform/adapter ${platform}/${adapter}` };
}

async function mapped(q: Q, s: Scope, table: string, oldId: string): Promise<string | null | undefined> {
  const r = await q.query(`SELECT new_id, status FROM flow_legacy_id_map WHERE tenant_id=$1 AND workspace_id=$2 AND legacy_table=$3 AND old_id=$4`, [s.tenant_id, s.workspace_id, table, oldId]);
  return r.rows[0] ? (r.rows[0].status === "imported" ? r.rows[0].new_id : null) : undefined;
}
async function record(q: Q, s: Scope, imp: string, e: ImportEntry) {
  await q.query(`INSERT INTO flow_legacy_id_map (tenant_id, workspace_id, legacy_table, old_id, new_id, status, reason, import_id)
      VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, [s.tenant_id, s.workspace_id, e.table, e.old_id, e.new_id, e.status, e.reason ?? null, imp]);
}

export type CopyObject = (from: string, toKey: string) => Promise<void>;

async function hasTable(legacy: Q, t: string) {
  const r = await legacy.query(`SELECT to_regclass($1) IS NOT NULL AS ok`, [t]).catch(() => ({ rows: [{ ok: false }] }));
  return !!r.rows[0]?.ok;
}
const asArr = (v: any) => (Array.isArray(v) ? v : typeof v === "string" ? (() => { try { return JSON.parse(v); } catch { return []; } })() : []);

export async function importLegacy(legacy: Q, target: Db, s: Scope, opts: { apply?: boolean; activate?: boolean; batch?: number; copyObject?: CopyObject; importKeywords?: boolean } = {}): Promise<ImportReport> {
  if (!s.tenant_id || !s.workspace_id) throw new Error("tenant_id and workspace_id required");
  const engine = new Engine(target);
  const imp = newId("imp_");
  const entries: ImportEntry[] = [];
  const add = async (e: ImportEntry, already: boolean) => { entries.push(e); if (opts.apply && !already) await record(target, s, imp, e); };
  const srcMap = new Map<string, string>();
  const itemMap = new Map<string, string>();
  const srcRows: any[] = [];

  for (const r of (await legacy.query(`SELECT * FROM sources ORDER BY created_at, id`)).rows as any[]) {
    srcRows.push(r);
    const prev = await mapped(target, s, "sources", r.id);
    if (prev !== undefined) { if (prev) srcMap.set(r.id, prev); entries.push({ table: "sources", old_id: r.id, new_id: prev, status: prev ? "imported" : "skipped", reason: "already mapped" }); continue; }
    const m = mapLegacySource(r);
    if ("skip" in m) { await add({ table: "sources", old_id: r.id, new_id: null, status: "skipped", reason: m.skip }, false); continue; }
    if (!opts.apply) { srcMap.set(r.id, `(new:${r.id})`); entries.push({ table: "sources", old_id: r.id, new_id: null, status: "imported", reason: "dry run" }); continue; }
    const row = await engine.createSource(s, m);
    if (!opts.activate || !r.enabled) await target.query(`UPDATE flow_sources SET status='paused', paused_reason='legacy_import' WHERE id=$1 AND tenant_id=$2 AND status='active'`, [row.id, s.tenant_id]);
    srcMap.set(r.id, row.id);
    await add({ table: "sources", old_id: r.id, new_id: row.id, status: "imported" }, false);
  }

  const batch = opts.batch ?? 500;
  for (let after = ""; ;) {
    const items = (await legacy.query(`SELECT * FROM items WHERE id > $1 ORDER BY id LIMIT $2`, [after, batch])).rows as any[];
    if (!items.length) break;
    after = items[items.length - 1].id;
    for (const it of items) {
      const prev = await mapped(target, s, "items", it.id);
      if (prev) itemMap.set(it.id, prev);
      if (prev !== undefined) { entries.push({ table: "items", old_id: it.id, new_id: prev, status: prev ? "imported" : "skipped", reason: "already mapped" }); continue; }
      const sid = srcMap.get(it.source_id);
      if (!sid) { await add({ table: "items", old_id: it.id, new_id: null, status: "skipped", reason: "source not imported" }, false); continue; }
      if (!opts.apply) { itemMap.set(it.id, `(new:${it.id})`); entries.push({ table: "items", old_id: it.id, new_id: null, status: "imported", reason: "dry run" }); continue; }
      const pub = it.published_at ? new Date(it.published_at).toISOString() : null;
      const newIdv = await target.tx(async (q) => {
        const ins = await q.query(`INSERT INTO flow_documents (id, tenant_id, workspace_id, source_id, external_id, url, title, published_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (tenant_id, workspace_id, source_id, external_id) DO NOTHING RETURNING id`,
          [newId("doc_"), s.tenant_id, s.workspace_id, sid, String(it.external_id), it.url ?? "", it.title ?? "", pub]);
        if (!ins.rows[0]) {
          // Same document already collected by Flow: map to it instead of duplicating.
          return (await q.query(`SELECT id FROM flow_documents WHERE tenant_id=$1 AND workspace_id=$2 AND source_id=$3 AND external_id=$4`, [s.tenant_id, s.workspace_id, sid, String(it.external_id)])).rows[0].id as string;
        }
        const id = ins.rows[0].id as string;
        await q.query(`INSERT INTO flow_document_revisions (document_id, revision, tenant_id, content_hash, title, body, collected_by_run) VALUES ($1,1,$2,$3,$4,$5,$6)`,
          [id, s.tenant_id, sha(`${it.title ?? ""}\n${it.body ?? ""}`), it.title ?? "", it.body ?? "", `legacy:${imp}`]);
        await engine.indexChunks(q, s as any, sid, id, 1, `${it.title ?? ""}\n\n${it.body ?? ""}`, pub);
        await record(q, s, imp, { table: "items", old_id: it.id, new_id: id, status: "imported" });
        return id;
      });
      itemMap.set(it.id, newIdv);
      entries.push({ table: "items", old_id: it.id, new_id: newIdv, status: "imported" });
    }
  }

  // Archive rows (analyses, notes, objects): one row per legacy id, idempotent by the map.
  const archive = async (table: string, kind: string, oldId: string, docs: string[], title: string, body: string, payload: any, createdAt: any) => {
    const prev = await mapped(target, s, table, oldId);
    if (prev !== undefined) { entries.push({ table, old_id: oldId, new_id: prev, status: prev ? "imported" : "skipped", reason: "already mapped" }); return prev; }
    if (!opts.apply) { entries.push({ table, old_id: oldId, new_id: null, status: "imported", reason: "dry run" }); return null; }
    return target.tx(async (q) => {
      const id = newId("arc_");
      await q.query(`INSERT INTO flow_legacy_archive (id, tenant_id, workspace_id, kind, old_id, document_ids, title, body, payload, import_id, legacy_created_at)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (tenant_id, workspace_id, kind, old_id) DO NOTHING`,
        [id, s.tenant_id, s.workspace_id, kind, oldId, docs, title.slice(0, 300), body.slice(0, 200_000), JSON.stringify(payload), imp, createdAt ? new Date(createdAt).toISOString() : null]);
      const row = (await q.query(`SELECT id FROM flow_legacy_archive WHERE tenant_id=$1 AND workspace_id=$2 AND kind=$3 AND old_id=$4`, [s.tenant_id, s.workspace_id, kind, oldId])).rows[0];
      await record(q, s, imp, { table, old_id: oldId, new_id: row.id, status: "imported" });
      entries.push({ table, old_id: oldId, new_id: row.id, status: "imported" });
      return row.id as string;
    });
  };
  const skip = async (table: string, oldId: string, reason: string) => {
    const prev = await mapped(target, s, table, oldId);
    await add({ table, old_id: oldId, new_id: null, status: "skipped", reason }, prev !== undefined);
  };

  if (await hasTable(legacy, "analyses")) {
    for (const a of (await legacy.query(`SELECT * FROM analyses ORDER BY created_at, id`)).rows as any[]) {
      const doc = itemMap.get(a.item_id);
      if (!doc) { await skip("analyses", a.id, "item not imported"); continue; }
      await archive("analyses", "analysis", a.id, opts.apply ? [doc] : [], `Legacy analysis (${a.model ?? "model"})`, [a.summary, a.insights].filter(Boolean).join("\n\n"),
        { topics: asArr(a.topics), entities: asArr(a.entities), tags: asArr(a.tags), model: a.model ?? null, verified: false, note: "legacy model output; not evidence-verified" }, a.created_at);
    }
  }
  if (await hasTable(legacy, "knowledge_notes")) {
    for (const n of (await legacy.query(`SELECT * FROM knowledge_notes ORDER BY created_at, id`)).rows as any[]) {
      const ids = asArr(n.item_ids).map(String);
      const docs = ids.map((i: string) => itemMap.get(i)).filter(Boolean) as string[];
      await archive("knowledge_notes", "note", n.id, opts.apply ? docs : [], String(n.title ?? ""), String(n.content ?? ""),
        { kind: n.kind ?? "note", legacy_item_ids: ids, unresolved_item_ids: ids.filter((i: string) => !itemMap.has(i)), verified: false }, n.created_at);
    }
  }

  // Objects: media URLs referenced by imported items.
  for (let after = ""; ;) {
    const items = (await legacy.query(`SELECT id, media_urls FROM items WHERE id > $1 ORDER BY id LIMIT $2`, [after, batch]).catch(() => ({ rows: [] }))).rows as any[];
    if (!items.length) break;
    after = items[items.length - 1].id;
    for (const it of items) {
      const urls = asArr(it.media_urls).map((u: any) => String(typeof u === "string" ? u : u?.url ?? "")).filter(Boolean);
      const doc = itemMap.get(it.id);
      for (const [i, url] of urls.entries()) {
        const oldId = `${it.id}#${i}`;
        if (!doc) { await skip("objects", oldId, "item not imported"); continue; }
        const name = (url.split(/[?#]/)[0].split("/").pop() || `object-${i}`).replace(/[^A-Za-z0-9._-]/g, "_").slice(0, 120);
        const key = `tenants/${s.tenant_id}/workspaces/${s.workspace_id}/legacy/${doc}/${i}-${name}`;
        let copy = "pending", err: string | undefined;
        if (opts.apply && opts.copyObject && (await mapped(target, s, "objects", oldId)) === undefined) {
          try { await opts.copyObject(url, key); copy = "copied"; } catch (e: any) { copy = "failed"; err = String(e?.message ?? e).slice(0, 200); }
        }
        await archive("objects", "object", oldId, opts.apply ? [doc] : [], name, "", { legacy_url: url, target_key: key, copy_status: copy, error: err }, null);
      }
    }
  }

  // Schedules: legacy interval_minutes → disabled Flow schedule (re-bind connection + budget, then enable).
  for (const r of srcRows) {
    const sid = srcMap.get(r.id);
    const mins = Number(r.interval_minutes ?? 0);
    if (!sid) { await skip("schedules", r.id, "source not imported"); continue; }
    if (!(mins > 0)) { await skip("schedules", r.id, "no interval"); continue; }
    const prev = await mapped(target, s, "schedules", r.id);
    if (prev !== undefined) { entries.push({ table: "schedules", old_id: r.id, new_id: prev, status: prev ? "imported" : "skipped", reason: "already mapped" }); continue; }
    if (!opts.apply) { entries.push({ table: "schedules", old_id: r.id, new_id: null, status: "imported", reason: "dry run" }); continue; }
    const sc = await engine.createSchedule(s, "legacy_import", "policy_v1", { source_ids: [sid], interval_seconds: Math.max(60, mins * 60) });
    await target.query(`UPDATE flow_schedules SET enabled=false, state='disabled', blocked_reason='legacy_import: re-bind connection and budget, then enable' WHERE id=$1 AND tenant_id=$2`, [sc.id, s.tenant_id]);
    await record(target, s, imp, { table: "schedules", old_id: r.id, new_id: sc.id, status: "imported" });
    entries.push({ table: "schedules", old_id: r.id, new_id: sc.id, status: "imported" });
  }

  const kw = await legacy.query(`SELECT to_regclass('keywords') IS NOT NULL AS ok`).catch(() => ({ rows: [{ ok: false }] }));
  if (kw.rows[0]?.ok) {
    const importedSources = [...srcMap.values()];
    for (const k of (await legacy.query(`SELECT id, phrase FROM keywords ORDER BY id`)).rows as any[]) {
      const prev = await mapped(target, s, "keywords", k.id);
      const phrase = String(k.phrase ?? "").trim().slice(0, 80);
      if (opts.importKeywords) {
        if (prev !== undefined) {
          entries.push({ table: "keywords", old_id: k.id, new_id: prev, status: prev ? "imported" : "skipped", reason: "already mapped" });
          continue;
        }
        if (!phrase || !importedSources.length) {
          await add({ table: "keywords", old_id: k.id, new_id: null, status: "skipped", reason: !phrase ? "empty keyword phrase" : "no imported sources for monitor preview" }, false);
          continue;
        }
        if (!opts.apply) {
          entries.push({ table: "keywords", old_id: k.id, new_id: null, status: "imported", reason: "dry run (monitor preview)" });
          continue;
        }
        const pv = await engine.previewMonitor(s, { name: phrase, keywords: [phrase], source_ids: importedSources, interval_seconds: 3600, timezone: "Asia/Shanghai" });
        await add({ table: "keywords", old_id: k.id, new_id: pv.preview_id, status: "imported", reason: "imported as monitor preview (pending approval)" }, false);
        continue;
      }
      await add({ table: "keywords", old_id: k.id, new_id: null, status: "skipped", reason: `keyword "${phrase}": re-create as a monitor (preview + approval)` }, prev !== undefined);
    }
  }
  const counts: ImportReport["counts"] = {};
  for (const e of entries) { (counts[e.table] ??= {})[e.status] = ((counts[e.table] ??= {})[e.status] ?? 0) + 1; }
  return { import_id: imp, dry_run: !opts.apply, entries, counts };
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const [tenant, workspace, ...flags] = process.argv.slice(2);
  if (!tenant || !workspace) { console.error("usage: legacy-import.ts <tenant_id> <workspace_id> [--apply] [--activate] [--import-keywords]"); process.exit(2); }
  const { pgDb, migrate } = await import("../db.ts");
  const legacy = await pgDb(process.env.LEGACY_DATABASE_URL ?? "");
  const target = await pgDb(process.env.DATABASE_URL ?? "");
  await migrate(target);
  const rep = await importLegacy(legacy, target, { tenant_id: tenant, workspace_id: workspace }, { apply: flags.includes("--apply"), activate: flags.includes("--activate"), importKeywords: flags.includes("--import-keywords") });
  for (const e of rep.entries) console.log(JSON.stringify(e));
  console.error(JSON.stringify({ import_id: rep.import_id, dry_run: rep.dry_run, counts: rep.counts }));
  await legacy.close(); await target.close();
}
