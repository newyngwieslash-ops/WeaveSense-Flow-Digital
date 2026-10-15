// Audit-8 gaps (PRD §5, §5.1, §5.2, §6.3, §6.4, §7.3, §10.3, §10.4): raw-response digests,
// extraction completeness, embed/analyze jobs, monitor plan/budget/re-approval,
// evidence snippet hash + URL, report export and scoped media download.
import { createHash, createHmac, randomBytes } from "node:crypto";
import { affected, type Db, type Q } from "./db.ts";
import { HttpError } from "./errors.ts";

type Scope = { tenant_id: string; workspace_id: string };
const sha = (s: string) => createHash("sha256").update(s).digest("hex");
const nid = (p: string) => p + randomBytes(10).toString("hex");
const RAW_ITEM_MAX = 64 * 1024;

// ---------- §6.3 raw response or traceable digest ----------
/** The connector item as received (capped at 64 KiB) and the sha256 of its full canonical JSON. */
export function rawItem(it: unknown): { json: string; digest: string } {
  const full = JSON.stringify(it ?? null);
  const json = full.length <= RAW_ITEM_MAX ? full : JSON.stringify({ truncated: true, bytes: full.length, head: full.slice(0, RAW_ITEM_MAX - 200) });
  return { json, digest: sha(full) };
}

// ---------- §7.3 evidence snippet hash ----------
export const snippetHash = (quote: string) => sha(String(quote ?? ""));

/** Recomputes snippet hash + URL for resolved evidence rows (needs quote, snippet_hash, url, doc_url, rev_text). */
export function verifySnippet(x: { quote: string; snippet_hash?: string | null; url?: string | null; doc_url?: string | null; rev_text?: string | null }) {
  const snippet_ok = !!x.snippet_hash && x.snippet_hash === snippetHash(x.quote) && (x.rev_text == null || x.quote === "" || x.rev_text.includes(x.quote));
  const url_ok = (x.url ?? "") === (x.doc_url ?? "");
  return { snippet_ok, url_ok };
}

// ---------- §5 extraction completeness ----------
export function extractionCompleteness(method: string | null, bodyChars: number, media: { status?: string; type?: string }[]) {
  const m = method || "feed";
  const total = media.length, stored = media.filter((x) => x.status === "stored").length;
  const level = ["extract", "reader", "transcript", "ocr", "asr"].includes(m) ? "full_text" : bodyChars > 0 ? "feed_summary" : "title_only";
  return { level, method: m, body_chars: bodyChars, media_total: total, media_stored: stored, media_missing: total - stored,
    complete: level === "full_text" && stored === total };
}

// ---------- §6.4 embed / analyze jobs ----------
export async function enqueueDerived(q: Q, s: Scope, documentId: string, revision: number, contentHash: string) {
  for (const kind of ["embed", "analyze"]) {
    // Unique per content hash: an unchanged body (frequent ticks, re-fetch) is never queued twice.
    await q.query(`INSERT INTO flow_derived_jobs (id, tenant_id, workspace_id, document_id, revision, kind, content_hash) VALUES ($1,$2,$3,$4,$5,$6,$7)
        ON CONFLICT (tenant_id, document_id, kind, content_hash) DO NOTHING`, [nid("dj_"), s.tenant_id, s.workspace_id, documentId, revision, kind, contentHash]);
  }
}

/** Pluggable embedding provider. Vector search is optional (PRD §7.3), so the default is "none" (job skipped, recorded). */
export type Embedder = { name: string; dims: number; embed: (texts: string[]) => Promise<number[][]> };
export function embedderFromEnv(env = process.env): Embedder | null {
  const p = env.EMBED_PROVIDER ?? "none";
  if (p === "none" || !p) return null;
  if (p === "hash") return hashEmbedder(Number(env.EMBED_DIMS) || 64);
  throw new Error(`unknown EMBED_PROVIDER ${p}`);
}
/** Deterministic local feature-hashing embedder (no network, no cost): for tests and offline deployments. */
export function hashEmbedder(dims = 64): Embedder {
  return { name: `hash-${dims}`, dims, embed: async (texts) => texts.map((t) => {
    const v = new Array(dims).fill(0);
    for (const tok of String(t).toLowerCase().match(/[\p{L}\p{N}]+/gu) ?? []) { const h = createHash("md5").update(tok).digest(); v[h.readUInt32LE(0) % dims] += h[4] & 1 ? 1 : -1; }
    const n = Math.hypot(...v) || 1;
    return v.map((x) => x / n);
  }) };
}

const ANALYZE_MAX_ATTEMPTS = 3;
export const NUM_RE = /(\d+(?:\.\d+)?)\s*(%|percent|万|亿|元|美元|吨|万吨|GW|MW|GWh)/gi;
/** Lexical analysis (no model call): language, length, numeric facts, matched monitor keywords and terms. */
export function analyzeText(title: string, body: string, keywords: string[]) {
  const text = `${title}\n${body}`;
  const cjk = (text.match(/[\u4e00-\u9fff]/g) ?? []).length;
  const facts = [...text.matchAll(NUM_RE)].slice(0, 20).map((m) => ({ value: Number(m[1]), unit: m[2] }));
  const lower = text.toLowerCase();
  const matched = [...new Set(keywords.filter((k) => k && lower.includes(k.toLowerCase())))];
  return { analyzer: "lexical_v1", language: cjk > text.length * 0.1 ? "zh" : "other", chars: text.length, numeric_facts: facts, matched_keywords: matched };
}

export async function derivedTick(db: Db, embedder: Embedder | null, limit = 20) {
  const due = (await db.query(`SELECT * FROM flow_derived_jobs WHERE status='queued' OR (status='failed' AND attempts < $1) ORDER BY created_at LIMIT $2`, [ANALYZE_MAX_ATTEMPTS, limit])).rows as any[];
  let n = 0;
  for (const j of due) {
    const claimed = await db.query(`UPDATE flow_derived_jobs SET attempts=attempts+1, updated_at=now() WHERE id=$1 AND attempts=$2 AND status IN ('queued','failed')`, [j.id, j.attempts]);
    if (!affected(claimed)) continue;
    try {
      const rev = (await db.query(`SELECT r.title, r.body, r.content_hash, d.current_revision FROM flow_document_revisions r JOIN flow_documents d ON d.id=r.document_id
          WHERE r.document_id=$1 AND r.revision=$2 AND r.tenant_id=$3`, [j.document_id, j.revision, j.tenant_id])).rows[0];
      if (!rev || rev.content_hash !== j.content_hash) { await setJob(db, j.id, "skipped", null, "revision superseded"); n++; continue; }
      if (j.kind === "analyze") {
        const kw = (await db.query(`SELECT DISTINCT unnest(keywords) AS k FROM flow_monitors m JOIN flow_documents d ON d.source_id = ANY(m.source_ids)
            WHERE d.id=$1 AND m.tenant_id=$2`, [j.document_id, j.tenant_id])).rows.map((r: any) => r.k);
        const a = analyzeText(rev.title, rev.body, kw);
        await db.query(`INSERT INTO flow_document_analyses (tenant_id, workspace_id, document_id, revision, content_hash, analysis) VALUES ($1,$2,$3,$4,$5,$6)
            ON CONFLICT (tenant_id, document_id, revision) DO UPDATE SET analysis=EXCLUDED.analysis, content_hash=EXCLUDED.content_hash`,
          [j.tenant_id, j.workspace_id, j.document_id, j.revision, j.content_hash, JSON.stringify(a)]);
        await setJob(db, j.id, "succeeded", a.analyzer, null);
      } else if (!embedder) {
        await setJob(db, j.id, "skipped", "none", "vector search disabled (EMBED_PROVIDER=none)");
      } else {
        const chunks = (await db.query(`SELECT chunk_index, text FROM flow_document_chunks WHERE tenant_id=$1 AND document_id=$2 AND revision=$3 ORDER BY chunk_index`,
          [j.tenant_id, j.document_id, j.revision])).rows as any[];
        const vecs = await embedder.embed(chunks.map((c) => c.text));
        for (const [i, c] of chunks.entries()) {
          await db.query(`INSERT INTO flow_document_embeddings (tenant_id, workspace_id, document_id, revision, chunk_index, provider, dims, vector) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
              ON CONFLICT DO NOTHING`, [j.tenant_id, j.workspace_id, j.document_id, j.revision, c.chunk_index, embedder.name, embedder.dims, vecs[i]]);
        }
        await setJob(db, j.id, "succeeded", embedder.name, null);
      }
      n++;
    } catch (e: any) {
      await setJob(db, j.id, "failed", null, String(e?.message ?? e).slice(0, 500));
    }
  }
  return n;
}
async function setJob(db: Db, id: string, status: string, provider: string | null, error: string | null) {
  await db.query(`UPDATE flow_derived_jobs SET status=$2, provider=coalesce($3, provider), error=$4, updated_at=now() WHERE id=$1`, [id, status, provider, error]);
}

export async function listDerivedJobs(db: Db, s: Scope, documentId?: string) {
  return (await db.query(`SELECT id, document_id, revision, kind, status, attempts, provider, error, created_at, updated_at FROM flow_derived_jobs
      WHERE tenant_id=$1 AND workspace_id=$2 AND ($3::text IS NULL OR document_id=$3) ORDER BY created_at DESC LIMIT 200`, [s.tenant_id, s.workspace_id, documentId || null])).rows;
}

export async function getAnalysis(db: Db, s: Scope, documentId: string) {
  const r = (await db.query(`SELECT document_id, revision, analysis, created_at FROM flow_document_analyses WHERE tenant_id=$1 AND workspace_id=$2 AND document_id=$3
      ORDER BY revision DESC LIMIT 1`, [s.tenant_id, s.workspace_id, documentId])).rows[0];
  if (!r) throw new HttpError(404, "analysis not found");
  return { ...r, analysis: typeof r.analysis === "string" ? JSON.parse(r.analysis) : r.analysis };
}

// ---------- §5.1 monitor plan: extraction method + cost estimate ----------
export type PriceHint = { version?: string; per_source_run_micro?: Record<string, number> };
export function extractionMethodFor(kinds: string[]) {
  const m: Record<string, string> = { rss: "feed_summary (RSS/Atom entry body; full text on demand via extract)", telegram: "telegram_message (+ stored media, OCR/ASR when enabled)", wechat: "wechat_article (gateway Atom feed)" };
  return [...new Set(kinds)].map((k) => m[k] ?? k).join("; ");
}
export function costEstimate(kinds: string[], runsPerDayPerSource: number, price?: PriceHint) {
  const unit = price?.per_source_run_micro ?? {};
  const perWindow = kinds.reduce((a, k) => a + Math.max(0, Number(unit[k] ?? 0)), 0);
  return { price_version: price?.version ?? "unpriced", per_window_micro: perWindow, per_day_micro: perWindow * runsPerDayPerSource,
    per_30d_micro: perWindow * runsPerDayPerSource * 30, basis: "per source run (paid_source_calls); model/OCR/ASR usage is metered separately" };
}

/** Which dimensions of a monitor change widen what was approved (PRD §5.1: needs a new decision). */
export function monitorExpansion(old: any, next: any): string[] {
  const out: string[] = [];
  const oldSrc = new Set<string>(old.source_ids ?? []);
  if ((next.source_ids ?? []).some((x: string) => !oldSrc.has(x))) out.push("sources");
  if (Number(next.budget_micro_per_window) > Number(old.budget_micro_per_window)) out.push("budget");
  if (Number(next.interval_seconds) < Number(old.interval_seconds)) out.push("frequency");
  const oldT = new Set<string>(old.notification_target_ids ?? []);
  const nt = next.notification_target_ids ?? [];
  if (nt.length !== oldT.size || nt.some((x: string) => !oldT.has(x))) out.push("notification_targets");
  return out;
}

// ---------- §5.2 report export ----------
export function renderReport(r: any, format: string) {
  const safe = String(r.title || "report").replace(/[^\p{L}\p{N}_-]+/gu, "_").slice(0, 80);
  if (format === "json") return { filename: `${safe}.json`, content_type: "application/json", content: JSON.stringify(r, null, 2) };
  const esc = (s: string) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]!));
  const cite = (x: any, i: number) => `[${i + 1}] ${x.title ?? x.document_id} · rev ${x.revision} · evidence ${x.evidence_id}${x.url ? ` · ${x.url}` : ""}${x.snippet_hash ? ` · sha256:${String(x.snippet_hash).slice(0, 12)}` : ""}`;
  if (format === "md") {
    const lines = [`# ${r.title}`, "", `- report_id: ${r.id}`, `- kind: ${r.kind}`, `- created_at: ${new Date(r.created_at).toISOString()}`, ""];
    if (r.body) lines.push(r.body, "");
    lines.push("## Claims", "");
    for (const c of r.claims ?? []) {
      lines.push(`${c.idx + 1}. **[${c.kind}]** ${c.text}`);
      (c.citations ?? []).forEach((x: any, i: number) => lines.push(`   - ${cite(x, i)}`, ...(x.quote ? [`     > ${String(x.quote).replace(/\n/g, " ")}`] : [])));
    }
    lines.push("", "## Coverage", "", "```json", JSON.stringify(r.coverage ?? {}, null, 2), "```", "");
    return { filename: `${safe}.md`, content_type: "text/markdown; charset=utf-8", content: lines.join("\n") };
  }
  if (format === "html") {
    const claims = (r.claims ?? []).map((c: any) => `<li><b>[${esc(c.kind)}]</b> ${esc(c.text)}<ul>${(c.citations ?? []).map((x: any, i: number) => `<li>${esc(cite(x, i))}${x.quote ? `<blockquote>${esc(x.quote)}</blockquote>` : ""}</li>`).join("")}</ul></li>`).join("");
    return { filename: `${safe}.html`, content_type: "text/html; charset=utf-8",
      content: `<!doctype html><html><head><meta charset="utf-8"><title>${esc(r.title)}</title></head><body><h1>${esc(r.title)}</h1><p>${esc(r.id)} · ${esc(r.kind)}</p>${r.body ? `<div style="white-space:pre-wrap">${esc(r.body)}</div>` : ""}<h2>Claims</h2><ol>${claims}</ol><h2>Coverage</h2><pre>${esc(JSON.stringify(r.coverage ?? {}, null, 2))}</pre></body></html>` };
  }
  throw new HttpError(400, "format must be md, html or json");
}

// ---------- §10.4 scoped media download (verify, then proxy) ----------
export type ObjectStore = (key: string) => Promise<{ bytes: Buffer; content_type: string }>;
export const MEDIA_DOWNLOAD_MAX = 25 * 1024 * 1024;

/** SigV4 presigned GET against the private S3/MinIO bucket (internal endpoint; never handed to the browser). */
export function presignGet(o: { endpoint: string; bucket: string; key: string; accessKey: string; secretKey: string; region?: string; now?: Date; expires?: number }) {
  const region = o.region ?? "us-east-1", now = o.now ?? new Date();
  const amz = now.toISOString().replace(/[-:]|\.\d{3}/g, ""), day = amz.slice(0, 8);
  const base = /^https?:\/\//.test(o.endpoint) ? o.endpoint : `http://${o.endpoint}`;
  const u = new URL(base);
  const path = `/${o.bucket}/${o.key.split("/").map(encodeURIComponent).join("/")}`;
  const scope = `${day}/${region}/s3/aws4_request`;
  const q = new URLSearchParams({ "X-Amz-Algorithm": "AWS4-HMAC-SHA256", "X-Amz-Credential": `${o.accessKey}/${scope}`, "X-Amz-Date": amz,
    "X-Amz-Expires": String(o.expires ?? 60), "X-Amz-SignedHeaders": "host" });
  const qs = [...q.entries()].sort(([a], [b]) => (a < b ? -1 : 1)).map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(v)}`).join("&");
  const canon = ["GET", path, qs, `host:${u.host}`, "", "host", "UNSIGNED-PAYLOAD"].join("\n");
  const sts = ["AWS4-HMAC-SHA256", amz, scope, sha(canon)].join("\n");
  const h = (k: Buffer | string, d: string) => createHmac("sha256", k).update(d).digest();
  const sig = createHmac("sha256", h(h(h(h(`AWS4${o.secretKey}`, day), region), "s3"), "aws4_request")).update(sts).digest("hex");
  return `${u.protocol}//${u.host}${path}?${qs}&X-Amz-Signature=${sig}`;
}

export function objectStoreFromEnv(env = process.env, fetchImpl: typeof fetch = fetch): ObjectStore | null {
  const endpoint = env.MINIO_ENDPOINT, bucket = env.MINIO_BUCKET, accessKey = env.MINIO_ACCESS_KEY, secretKey = env.MINIO_SECRET_KEY;
  if (!endpoint || !bucket || !accessKey || !secretKey) return null;
  return async (key) => {
    const res = await fetchImpl(presignGet({ endpoint, bucket, key, accessKey, secretKey, region: env.MINIO_REGION }), { redirect: "error", signal: AbortSignal.timeout(60_000) });
    if (res.status === 404) throw new HttpError(404, "object not found");
    if (!res.ok) throw new HttpError(502, "object store unavailable");
    if (Number(res.headers.get("content-length") ?? 0) > MEDIA_DOWNLOAD_MAX) throw new HttpError(413, "object too large to proxy");
    const bytes = Buffer.from(await res.arrayBuffer());
    if (bytes.length > MEDIA_DOWNLOAD_MAX) throw new HttpError(413, "object too large to proxy");
    return { bytes, content_type: res.headers.get("content-type") ?? "application/octet-stream" };
  };
}

/** Authorises (document in caller's tenant/workspace, key referenced by it and inside the scoped prefix), then proxies. */
export async function downloadMedia(db: Db, store: ObjectStore | null, s: Scope, o: { document_id: string; object_key: string }) {
  const key = String(o.object_key ?? "");
  const prefix = `tenants/${s.tenant_id}/workspaces/${s.workspace_id}/`;
  if (!key.startsWith(prefix) || key.includes("..")) throw new HttpError(404, "object not found");
  const d = (await db.query(`SELECT media FROM flow_documents WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3`, [s.tenant_id, s.workspace_id, String(o.document_id ?? "")])).rows[0];
  if (!d) throw new HttpError(404, "object not found");
  const media: any[] = typeof d.media === "string" ? JSON.parse(d.media) : (d.media ?? []);
  const m = media.find((x) => x.url === key && x.status === "stored");
  if (!m) throw new HttpError(404, "object not found");
  if (!store) throw new HttpError(503, "object storage not configured");
  const got = await store(key);
  if (m.sha256 && createHash("sha256").update(got.bytes).digest("hex") !== m.sha256) throw new HttpError(502, "object integrity check failed");
  return { object_key: key, content_type: m.content_type || got.content_type, size: got.bytes.length, sha256: createHash("sha256").update(got.bytes).digest("hex"),
    expected_sha256: m.sha256 ?? null, filename: key.split("/").slice(-2).join("_"), data_base64: got.bytes.toString("base64") };
}
