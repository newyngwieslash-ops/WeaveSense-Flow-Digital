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
// Analyses/knowledge notes/AI runs are not imported (no evidence-bound equivalent).
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

export async function importLegacy(legacy: Q, target: Db, s: Scope, opts: { apply?: boolean; activate?: boolean; batch?: number } = {}): Promise<ImportReport> {
  if (!s.tenant_id || !s.workspace_id) throw new Error("tenant_id and workspace_id required");
  const engine = new Engine(target);
  const imp = newId("imp_");
  const entries: ImportEntry[] = [];
  const add = async (e: ImportEntry, already: boolean) => { entries.push(e); if (opts.apply && !already) await record(target, s, imp, e); };
  const srcMap = new Map<string, string>();

  for (const r of (await legacy.query(`SELECT * FROM sources ORDER BY created_at, id`)).rows as any[]) {
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
      if (prev !== undefined) { entries.push({ table: "items", old_id: it.id, new_id: prev, status: prev ? "imported" : "skipped", reason: "already mapped" }); continue; }
      const sid = srcMap.get(it.source_id);
      if (!sid) { await add({ table: "items", old_id: it.id, new_id: null, status: "skipped", reason: "source not imported" }, false); continue; }
      if (!opts.apply) { entries.push({ table: "items", old_id: it.id, new_id: null, status: "imported", reason: "dry run" }); continue; }
      const pub = it.published_at ? new Date(it.published_at).toISOString() : null;
      const newIdv = await target.tx(async (q) => {
        const ins = await q.query(`INSERT INTO flow_documents (id, tenant_id, workspace_id, source_id, external_id, url, title, published_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (tenant_id, source_id, external_id) DO NOTHING RETURNING id`,
          [newId("doc_"), s.tenant_id, s.workspace_id, sid, String(it.external_id), it.url ?? "", it.title ?? "", pub]);
        if (!ins.rows[0]) {
          // Same document already collected by Flow: map to it instead of duplicating.
          return (await q.query(`SELECT id FROM flow_documents WHERE tenant_id=$1 AND source_id=$2 AND external_id=$3`, [s.tenant_id, sid, String(it.external_id)])).rows[0].id as string;
        }
        const id = ins.rows[0].id as string;
        await q.query(`INSERT INTO flow_document_revisions (document_id, revision, tenant_id, content_hash, title, body, collected_by_run) VALUES ($1,1,$2,$3,$4,$5,$6)`,
          [id, s.tenant_id, sha(`${it.title ?? ""}\n${it.body ?? ""}`), it.title ?? "", it.body ?? "", `legacy:${imp}`]);
        await engine.indexChunks(q, s as any, sid, id, 1, `${it.title ?? ""}\n\n${it.body ?? ""}`, pub);
        await record(q, s, imp, { table: "items", old_id: it.id, new_id: id, status: "imported" });
        return id;
      });
      entries.push({ table: "items", old_id: it.id, new_id: newIdv, status: "imported" });
    }
  }

  const kw = await legacy.query(`SELECT to_regclass('keywords') IS NOT NULL AS ok`).catch(() => ({ rows: [{ ok: false }] }));
  if (kw.rows[0]?.ok) {
    for (const k of (await legacy.query(`SELECT id, phrase FROM keywords ORDER BY id`)).rows as any[]) {
      const prev = await mapped(target, s, "keywords", k.id);
      await add({ table: "keywords", old_id: k.id, new_id: null, status: "skipped", reason: `keyword "${String(k.phrase).slice(0, 80)}": re-create as a monitor (preview + approval)` }, prev !== undefined);
    }
  }
  const counts: ImportReport["counts"] = {};
  for (const e of entries) { (counts[e.table] ??= {})[e.status] = ((counts[e.table] ??= {})[e.status] ?? 0) + 1; }
  return { import_id: imp, dry_run: !opts.apply, entries, counts };
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const [tenant, workspace, ...flags] = process.argv.slice(2);
  if (!tenant || !workspace) { console.error("usage: legacy-import.ts <tenant_id> <workspace_id> [--apply] [--activate]"); process.exit(2); }
  const { pgDb, migrate } = await import("../db.ts");
  const legacy = await pgDb(process.env.LEGACY_DATABASE_URL ?? "");
  const target = await pgDb(process.env.DATABASE_URL ?? "");
  await migrate(target);
  const rep = await importLegacy(legacy, target, { tenant_id: tenant, workspace_id: workspace }, { apply: flags.includes("--apply"), activate: flags.includes("--activate") });
  for (const e of rep.entries) console.log(JSON.stringify(e));
  console.error(JSON.stringify({ import_id: rep.import_id, dry_run: rep.dry_run, counts: rep.counts }));
  await legacy.close(); await target.close();
}
