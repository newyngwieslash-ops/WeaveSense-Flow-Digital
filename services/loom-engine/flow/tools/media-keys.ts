// §10.4 compat: moves legacy Telegram media keys (telegram/<channel>/<id>, shared prefix) to
// tenants/<tenant>/workspaces/<workspace>/telegram/<channel>/<id>.
//
//   node --experimental-strip-types flow/tools/media-keys.ts plan  > plan.jsonl     # read-only
//   (operator copies objects, e.g. the printed `mc cp` lines, and verifies them)
//   node --experimental-strip-types flow/tools/media-keys.ts apply plan.jsonl       # rewrites document media refs
//
// The DB rewrite is per document and optimistic (only if the media JSON is unchanged since the plan),
// so it is safe to re-run. Old objects are not deleted by this tool; remove them after verification.
import { readFileSync } from "node:fs";
import type { Db } from "../db.ts";
import { LEGACY_MEDIA_KEY, scopedMediaKey } from "../connectors.ts";

export type MediaKeyMove = { document_id: string; tenant_id: string; workspace_id: string; from: string; to: string; media_before: string };

export async function planMediaKeyMigration(db: Db): Promise<MediaKeyMove[]> {
  const r = await db.query(`SELECT id, tenant_id, workspace_id, media FROM flow_documents
      WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(media) m WHERE m->>'status'='stored' AND m->>'url' LIKE 'telegram/%') ORDER BY id`);
  const out: MediaKeyMove[] = [];
  for (const d of r.rows as any[]) {
    const media = typeof d.media === "string" ? JSON.parse(d.media) : d.media;
    for (const m of media) {
      if (m.status === "stored" && LEGACY_MEDIA_KEY.test(m.url)) {
        out.push({ document_id: d.id, tenant_id: d.tenant_id, workspace_id: d.workspace_id, from: m.url, to: scopedMediaKey(d, m.url), media_before: JSON.stringify(media) });
      }
    }
  }
  return out;
}

/** Rewrites refs for moves whose objects were copied (`copied(from,to)` lets callers verify existence first). */
export async function applyMediaKeyMigration(db: Db, plan: MediaKeyMove[], copied: (m: MediaKeyMove) => Promise<boolean> = async () => true) {
  const byDoc = new Map<string, MediaKeyMove[]>();
  for (const m of plan) byDoc.set(m.document_id, [...(byDoc.get(m.document_id) ?? []), m]);
  let updated = 0, skipped = 0;
  for (const [id, moves] of byDoc) {
    const ok = [];
    for (const m of moves) if (await copied(m)) ok.push(m);
    if (!ok.length) { skipped++; continue; }
    const before = moves[0].media_before;
    const media = JSON.parse(before).map((x: any) => {
      const mv = ok.find((m) => m.from === x.url);
      return mv ? { ...x, url: mv.to, legacy_key: mv.from } : x;
    });
    const u = await db.query(`UPDATE flow_documents SET media=$2::jsonb WHERE id=$1 AND tenant_id=$3 AND media=$4::jsonb`, [id, JSON.stringify(media), moves[0].tenant_id, before]);
    (u.rowCount ?? (u as any).affectedRows ?? 0) ? updated++ : skipped++;
  }
  return { documents_updated: updated, documents_skipped: skipped };
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const { pgDb } = await import("../db.ts");
  const db = await pgDb(process.env.DATABASE_URL ?? "");
  const bucket = process.env.MINIO_BUCKET ?? "flow-media";
  try {
    if (process.argv[2] === "plan") {
      const plan = await planMediaKeyMigration(db);
      for (const m of plan) console.log(JSON.stringify(m));
      for (const m of plan) console.error(`mc cp "flow/${bucket}/${m.from}" "flow/${bucket}/${m.to}"`);
    } else if (process.argv[2] === "apply" && process.argv[3]) {
      const plan = readFileSync(process.argv[3], "utf8").split("\n").filter(Boolean).map((l) => JSON.parse(l));
      console.log(JSON.stringify(await applyMediaKeyMigration(db, plan)));
    } else {
      console.error("usage: media-keys.ts plan | apply <plan.jsonl>"); process.exitCode = 2;
    }
  } finally { await db.close(); }
}
