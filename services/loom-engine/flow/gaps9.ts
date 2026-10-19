// Audit-8 gaps (PRD §5, §5.1, §5.2, §6.3, §6.4, §7.3, §10.3, §10.4): raw-response digests,
// extraction completeness, embed/analyze jobs, monitor plan/budget/re-approval,
// evidence snippet hash + URL, report export and scoped media download.
import { createHash, createHmac, randomBytes } from "node:crypto";
import { crc32 } from "node:zlib";
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
export function embedderFromEnv(env = process.env, fetchImpl: typeof fetch = fetch): Embedder | null {
  const p = env.EMBED_PROVIDER ?? "none";
  if (p === "none" || !p) return null;
  if (p === "hash") return hashEmbedder(Number(env.EMBED_DIMS) || 64);
  if (p === "openai" || p === "http") {
    const apiKey = env.EMBED_API_KEY ?? "";
    if (!apiKey) throw new Error("EMBED_API_KEY is required when EMBED_PROVIDER=openai");
    return openaiEmbedder({
      baseUrl: env.EMBED_BASE_URL || "https://api.openai.com",
      apiKey,
      model: env.EMBED_MODEL || "text-embedding-3-small",
      dims: Number(env.EMBED_DIMS) || 1536,
      fetchImpl,
    });
  }
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
/** Real OpenAI-compatible HTTP embeddings provider (POST /v1/embeddings). */
export function openaiEmbedder(o: { baseUrl?: string; apiKey: string; model?: string; dims?: number; fetchImpl?: typeof fetch }): Embedder {
  const model = o.model || "text-embedding-3-small";
  const dims = o.dims || 1536;
  const f = o.fetchImpl ?? fetch;
  const base = (o.baseUrl || "https://api.openai.com").replace(/\/+$/, "");
  const endpoint = base.endsWith("/embeddings") ? base : base.endsWith("/v1") ? `${base}/embeddings` : `${base}/v1/embeddings`;
  return {
    name: `openai:${model}`,
    dims,
    embed: async (texts) => {
      if (!texts.length) return [];
      const res = await f(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json", Authorization: `Bearer ${o.apiKey}` },
        body: JSON.stringify({ model, input: texts, dimensions: dims }),
        signal: AbortSignal.timeout(30_000),
      });
      if (!res.ok) throw new HttpError(502, `embedding provider HTTP ${res.status}`);
      const body: any = await res.json().catch(() => ({}));
      const items: any[] = Array.isArray(body?.data) ? [...body.data].sort((a, b) => (a.index ?? 0) - (b.index ?? 0)) : [];
      if (items.length !== texts.length) throw new HttpError(502, `embedding provider returned ${items.length}/${texts.length} vectors`);
      return items.map((it) => {
        const raw: number[] = Array.isArray(it.embedding) ? it.embedding.map(Number) : [];
        if (raw.length !== dims) throw new HttpError(502, `embedding dims mismatch: expected ${dims}, got ${raw.length}`);
        const n = Math.hypot(...raw) || 1;
        return raw.map((x) => x / n);
      });
    },
  };
}
export function cosineSimilarity(a: number[], b: number[]): number {
  if (!a.length || a.length !== b.length) return 0;
  let dot = 0, na = 0, nb = 0;
  for (let i = 0; i < a.length; i++) {
    const x = Number(a[i]) || 0, y = Number(b[i]) || 0;
    dot += x * y;
    na += x * x;
    nb += y * y;
  }
  const denom = Math.sqrt(na) * Math.sqrt(nb);
  return denom > 0 ? dot / denom : 0;
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
function buildZipStore(entries: { name: string; data: Buffer }[]): Buffer {
  const locals: Buffer[] = [];
  const centrals: Buffer[] = [];
  let offset = 0;
  for (const e of entries) {
    const nameBuf = Buffer.from(e.name, "utf8");
    const sum = crc32(e.data) >>> 0;
    const local = Buffer.alloc(30 + nameBuf.length);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt16LE(0x0800, 6); // UTF-8 filename flag
    local.writeUInt16LE(0, 8); // store (uncompressed)
    local.writeUInt32LE(0, 10);
    local.writeUInt32LE(sum, 14);
    local.writeUInt32LE(e.data.length, 18);
    local.writeUInt32LE(e.data.length, 22);
    local.writeUInt16LE(nameBuf.length, 26);
    local.writeUInt16LE(0, 28);
    nameBuf.copy(local, 30);
    locals.push(local, e.data);

    const central = Buffer.alloc(46 + nameBuf.length);
    central.writeUInt32LE(0x02014b50, 0);
    central.writeUInt16LE(20, 4);
    central.writeUInt16LE(20, 6);
    central.writeUInt16LE(0x0800, 8);
    central.writeUInt16LE(0, 10);
    central.writeUInt32LE(0, 12);
    central.writeUInt32LE(sum, 16);
    central.writeUInt32LE(e.data.length, 20);
    central.writeUInt32LE(e.data.length, 24);
    central.writeUInt16LE(nameBuf.length, 28);
    central.writeUInt16LE(0, 30);
    central.writeUInt16LE(0, 32);
    central.writeUInt16LE(0, 34);
    central.writeUInt16LE(0, 36);
    central.writeUInt32LE(0, 38);
    central.writeUInt32LE(offset, 42);
    nameBuf.copy(central, 46);
    centrals.push(central);
    offset += local.length + e.data.length;
  }
  const cdBuf = Buffer.concat(centrals);
  const eocd = Buffer.alloc(22);
  eocd.writeUInt32LE(0x06054b50, 0);
  eocd.writeUInt16LE(0, 4);
  eocd.writeUInt16LE(0, 6);
  eocd.writeUInt16LE(entries.length, 8);
  eocd.writeUInt16LE(entries.length, 10);
  eocd.writeUInt32LE(cdBuf.length, 12);
  eocd.writeUInt32LE(offset, 16);
  eocd.writeUInt16LE(0, 20);
  return Buffer.concat([...locals, cdBuf, eocd]);
}

function renderDocxBuffer(r: any, esc: (s: string) => string, cite: (x: any, i: number) => string): Buffer {
  const p = (text: string, opts: { bold?: boolean; sizeHalfPt?: number; indentTwips?: number } = {}) => {
    const rPr = [
      `<w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="Microsoft YaHei" w:cs="Calibri"/>`,
      opts.bold ? `<w:b/>` : "",
      opts.sizeHalfPt ? `<w:sz w:val="${opts.sizeHalfPt}"/><w:szCs w:val="${opts.sizeHalfPt}"/>` : "",
    ].join("");
    const pPr = opts.indentTwips ? `<w:pPr><w:ind w:left="${opts.indentTwips}"/></w:pPr>` : "";
    return `<w:p>${pPr}<w:r><w:rPr>${rPr}</w:rPr><w:t xml:space="preserve">${esc(text)}</w:t></w:r></w:p>`;
  };
  const paras: string[] = [
    p(String(r.title || "Report"), { bold: true, sizeHalfPt: 36 }),
    p(`report_id: ${r.id} · kind: ${r.kind} · created_at: ${new Date(r.created_at ?? Date.now()).toISOString()}`, { sizeHalfPt: 20 }),
  ];
  if (r.body) {
    for (const line of String(r.body).split(/\r?\n/)) paras.push(p(line, { sizeHalfPt: 22 }));
  }
  paras.push(p("Claims", { bold: true, sizeHalfPt: 28 }));
  for (const c of r.claims ?? []) {
    paras.push(p(`${(c.idx ?? 0) + 1}. [${c.kind}] ${c.text}`, { bold: true, sizeHalfPt: 22 }));
    (c.citations ?? []).forEach((x: any, i: number) => {
      paras.push(p(`- ${cite(x, i)}`, { sizeHalfPt: 20, indentTwips: 360 }));
      if (x.quote) paras.push(p(`> ${String(x.quote).replace(/\r?\n/g, " ")}`, { sizeHalfPt: 18, indentTwips: 720 }));
    });
  }
  paras.push(p("Coverage", { bold: true, sizeHalfPt: 28 }));
  for (const line of JSON.stringify(r.coverage ?? {}, null, 2).split("\n")) {
    paras.push(p(line, { sizeHalfPt: 18, indentTwips: 240 }));
  }
  const docXml = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
    paras.join("") +
    `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440"/></w:sectPr>` +
    `</w:body></w:document>`;
  const contentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
    `<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
    `<Default Extension="xml" ContentType="application/xml"/>` +
    `<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
    `</Types>`;
  const rels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
    `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
    `<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
    `</Relationships>`;
  return buildZipStore([
    { name: "[Content_Types].xml", data: Buffer.from(contentTypes, "utf8") },
    { name: "_rels/.rels", data: Buffer.from(rels, "utf8") },
    { name: "word/document.xml", data: Buffer.from(docXml, "utf8") },
  ]);
}

function renderPdfBuffer(lines: string[]): Buffer {
  const wrapped: string[] = [];
  for (const raw of lines) {
    const s = String(raw ?? "");
    if (s.length <= 80) { wrapped.push(s); continue; }
    for (let i = 0; i < s.length; i += 80) wrapped.push(s.slice(i, i + 80));
  }
  const perPage = 42;
  const pageChunks: string[][] = [];
  for (let i = 0; i < Math.max(1, wrapped.length); i += perPage) {
    pageChunks.push(wrapped.slice(i, i + perPage));
  }
  const pdfEsc = (s: string) => s.replace(/\\/g, "\\\\").replace(/\(/g, "\\(").replace(/\)/g, "\\)");
  const utf16Hex = (s: string) => {
    let out = "FEFF";
    for (let i = 0; i < s.length; i++) out += s.charCodeAt(i).toString(16).toUpperCase().padStart(4, "0");
    return out;
  };
  const objs: string[] = [];
  // 1: Catalog, 2: Pages, 3: Helvetica (ASCII), 4: Type0 STSong-Light (CJK UniGB-UTF16-H), 5: CIDFont
  const pageObjNums: number[] = [];
  for (let p = 0; p < pageChunks.length; p++) pageObjNums.push(6 + p * 2);
  objs.push(`<< /Type /Catalog /Pages 2 0 R >>`);
  objs.push(`<< /Type /Pages /Kids [ ${pageObjNums.map((n) => `${n} 0 R`).join(" ")} ] /Count ${pageObjNums.length} >>`);
  objs.push(`<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>`);
  objs.push(`<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UTF16-H /DescendantFonts [ 5 0 R ] >>`);
  objs.push(`<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 4 >> /DW 1000 /W [ 1 95 500 ] >>`);
  for (let p = 0; p < pageChunks.length; p++) {
    const pageNum = 6 + p * 2;
    const streamNum = pageNum + 1;
    const ops: string[] = [];
    let y = 790;
    for (const ln of pageChunks[p]) {
      const sz = ln.startsWith("# ") ? 15 : ln.startsWith("## ") ? 13 : 10;
      if (/^[\x20-\x7e]*$/.test(ln)) {
        ops.push(`BT /F1 ${sz} Tf 48 ${y} Td (${pdfEsc(ln)}) Tj ET`);
      } else {
        ops.push(`BT /F2 ${sz} Tf 48 ${y} Td <${utf16Hex(ln)}> Tj ET % ${ln.replace(/[\r\n]+/g, " ")}`);
      }
      y -= sz + 6;
    }
    const streamData = ops.join("\n");
    const streamBytes = Buffer.byteLength(streamData, "utf8");
    objs.push(`<< /Type /Page /Parent 2 0 R /MediaBox [ 0 0 595 842 ] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents ${streamNum} 0 R >>`);
    objs.push(`<< /Length ${streamBytes} >>\nstream\n${streamData}\nendstream`);
  }
  const chunks: Buffer[] = [Buffer.from("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n", "binary")];
  const offsets: number[] = [0];
  let pos = chunks[0].length;
  for (let i = 0; i < objs.length; i++) {
    offsets.push(pos);
    const b = Buffer.from(`${i + 1} 0 obj\n${objs[i]}\nendobj\n`, "utf8");
    chunks.push(b);
    pos += b.length;
  }
  const xrefPos = pos;
  const xrefLines = [`xref\n0 ${objs.length + 1}\n0000000000 65535 f \n`];
  for (let i = 1; i <= objs.length; i++) {
    xrefLines.push(`${String(offsets[i]).padStart(10, "0")} 00000 n \n`);
  }
  xrefLines.push(`trailer\n<< /Size ${objs.length + 1} /Root 1 0 R >>\nstartxref\n${xrefPos}\n%%EOF\n`);
  chunks.push(Buffer.from(xrefLines.join(""), "utf8"));
  return Buffer.concat(chunks);
}

export function renderReport(r: any, format: string) {
  const safe = String(r.title || "report").replace(/[^\p{L}\p{N}_-]+/gu, "_").slice(0, 80);
  if (format === "json") return { filename: `${safe}.json`, content_type: "application/json", content: JSON.stringify(r, null, 2) };
  const esc = (s: string) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]!));
  const cite = (x: any, i: number) => `[${i + 1}] ${x.title ?? x.document_id} · rev ${x.revision} · evidence ${x.evidence_id}${x.url ? ` · ${x.url}` : ""}${x.snippet_hash ? ` · sha256:${String(x.snippet_hash).slice(0, 12)}` : ""}`;
  const mdLines = () => {
    const lines = [`# ${r.title}`, "", `- report_id: ${r.id}`, `- kind: ${r.kind}`, `- created_at: ${new Date(r.created_at).toISOString()}`, ""];
    if (r.body) lines.push(r.body, "");
    lines.push("## Claims", "");
    for (const c of r.claims ?? []) {
      lines.push(`${c.idx + 1}. **[${c.kind}]** ${c.text}`);
      (c.citations ?? []).forEach((x: any, i: number) => lines.push(`   - ${cite(x, i)}`, ...(x.quote ? [`     > ${String(x.quote).replace(/\n/g, " ")}`] : [])));
    }
    lines.push("", "## Coverage", "", "```json", JSON.stringify(r.coverage ?? {}, null, 2), "```", "");
    return lines;
  };
  if (format === "md") {
    return { filename: `${safe}.md`, content_type: "text/markdown; charset=utf-8", content: mdLines().join("\n") };
  }
  if (format === "html") {
    const claims = (r.claims ?? []).map((c: any) => `<li><b>[${esc(c.kind)}]</b> ${esc(c.text)}<ul>${(c.citations ?? []).map((x: any, i: number) => `<li>${esc(cite(x, i))}${x.quote ? `<blockquote>${esc(x.quote)}</blockquote>` : ""}</li>`).join("")}</ul></li>`).join("");
    return { filename: `${safe}.html`, content_type: "text/html; charset=utf-8",
      content: `<!doctype html><html><head><meta charset="utf-8"><title>${esc(r.title)}</title></head><body><h1>${esc(r.title)}</h1><p>${esc(r.id)} · ${esc(r.kind)}</p>${r.body ? `<div style="white-space:pre-wrap">${esc(r.body)}</div>` : ""}<h2>Claims</h2><ol>${claims}</ol><h2>Coverage</h2><pre>${esc(JSON.stringify(r.coverage ?? {}, null, 2))}</pre></body></html>` };
  }
  if (format === "pdf") {
    const buf = renderPdfBuffer(mdLines());
    return { filename: `${safe}.pdf`, content_type: "application/pdf", content_base64: buf.toString("base64"), content: buf.toString("utf8") };
  }
  if (format === "docx") {
    const buf = renderDocxBuffer(r, esc, cite);
    return { filename: `${safe}.docx`, content_type: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
      content_base64: buf.toString("base64"), content: buf.toString("utf8") };
  }
  throw new HttpError(400, "format must be md, html, json, pdf or docx");
}

// ---------- §10.4 scoped media download (verify, then stream/proxy or short-lived scoped presign) ----------
export type ObjectStore = (key: string) => Promise<{ bytes: Buffer; content_type: string }>;
export const MEDIA_DOWNLOAD_MAX = 25 * 1024 * 1024;
export const MEDIA_STREAM_MAX = 512 * 1024 * 1024;

/** SigV4 presigned GET against the private S3/MinIO bucket. */
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

export function objectStoreFromEnv(env = process.env, fetchImpl: typeof fetch = fetch, maxBytes = Number(env.MEDIA_DOWNLOAD_MAX_BYTES) || MEDIA_STREAM_MAX): ObjectStore | null {
  const endpoint = env.MINIO_ENDPOINT, bucket = env.MINIO_BUCKET, accessKey = env.MINIO_ACCESS_KEY, secretKey = env.MINIO_SECRET_KEY;
  if (!endpoint || !bucket || !accessKey || !secretKey) return null;
  return async (key) => {
    const res = await fetchImpl(presignGet({ endpoint, bucket, key, accessKey, secretKey, region: env.MINIO_REGION }), { redirect: "error", signal: AbortSignal.timeout(120_000) });
    if (res.status === 404) throw new HttpError(404, "object not found");
    if (!res.ok) throw new HttpError(502, "object store unavailable");
    if (Number(res.headers.get("content-length") ?? 0) > maxBytes) throw new HttpError(413, "object too large to proxy");
    const bytes = Buffer.from(await res.arrayBuffer());
    if (bytes.length > maxBytes) throw new HttpError(413, "object too large to proxy");
    return { bytes, content_type: res.headers.get("content-type") ?? "application/octet-stream" };
  };
}

export async function authorizeMedia(db: Db, s: Scope, o: { document_id: string; object_key: string }) {
  const key = String(o.object_key ?? "");
  const prefix = `tenants/${s.tenant_id}/workspaces/${s.workspace_id}/`;
  if (!key.startsWith(prefix) || key.includes("..")) throw new HttpError(404, "object not found");
  const d = (await db.query(`SELECT media FROM flow_documents WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3`, [s.tenant_id, s.workspace_id, String(o.document_id ?? "")])).rows[0];
  if (!d) throw new HttpError(404, "object not found");
  const media: any[] = typeof d.media === "string" ? JSON.parse(d.media) : (d.media ?? []);
  const m = media.find((x) => x.url === key && x.status === "stored");
  if (!m) throw new HttpError(404, "object not found");
  return { key, m, filename: key.split("/").slice(-2).join("_") };
}

/** Authorises (document in caller's tenant/workspace, key referenced by it and inside the scoped prefix), then streams/proxies or signs a short-lived scoped URL. */
export async function downloadMedia(
  db: Db,
  store: ObjectStore | null,
  s: Scope,
  o: { document_id: string; object_key: string; mode?: "proxy" | "presign"; stream?: boolean; expires?: number; presign_env?: Record<string, string | undefined>; now?: Date },
) {
  const { key, m, filename } = await authorizeMedia(db, s, o);
  if (o.mode === "presign") {
    const env = o.presign_env ?? process.env;
    const endpoint = env.MINIO_PUBLIC_ENDPOINT || env.MINIO_ENDPOINT, bucket = env.MINIO_BUCKET, accessKey = env.MINIO_ACCESS_KEY, secretKey = env.MINIO_SECRET_KEY;
    if (!endpoint || !bucket || !accessKey || !secretKey) throw new HttpError(503, "object storage not configured");
    const expires = Math.min(300, Math.max(15, Number(o.expires) || 60));
    return {
      mode: "presign" as const,
      object_key: key,
      content_type: m.content_type || "application/octet-stream",
      expected_sha256: m.sha256 ?? null,
      filename,
      presigned_url: presignGet({ endpoint, bucket, key, accessKey, secretKey, region: env.MINIO_REGION, now: o.now, expires }),
      expires_in: expires,
    };
  }
  if (!store) throw new HttpError(503, "object storage not configured");
  const got = await store(key);
  const maxCap = o.stream ? MEDIA_STREAM_MAX : MEDIA_DOWNLOAD_MAX;
  if (got.bytes.length > maxCap) throw new HttpError(413, "object too large to proxy");
  const digest = createHash("sha256").update(got.bytes).digest("hex");
  if (m.sha256 && digest !== m.sha256) throw new HttpError(502, "object integrity check failed");
  if (o.stream) {
    return { object_key: key, content_type: m.content_type || got.content_type, size: got.bytes.length, sha256: digest,
      expected_sha256: m.sha256 ?? null, filename, bytes: got.bytes };
  }
  return { object_key: key, content_type: m.content_type || got.content_type, size: got.bytes.length, sha256: digest,
    expected_sha256: m.sha256 ?? null, filename, data_base64: got.bytes.toString("base64") };
}

// ---------- §6.3 raw HTTP response byte archiving & scoped download ----------
export type ObjectStorePut = (key: string, bytes: Buffer, contentType: string) => Promise<void>;

/** SigV4 presigned PUT against the private S3/MinIO bucket. */
export function presignPut(o: { endpoint: string; bucket: string; key: string; accessKey: string; secretKey: string; region?: string; now?: Date; expires?: number }) {
  const region = o.region ?? "us-east-1", now = o.now ?? new Date();
  const amz = now.toISOString().replace(/[-:]|\.\d{3}/g, ""), day = amz.slice(0, 8);
  const base = /^https?:\/\//.test(o.endpoint) ? o.endpoint : `http://${o.endpoint}`;
  const u = new URL(base);
  const path = `/${o.bucket}/${o.key.split("/").map(encodeURIComponent).join("/")}`;
  const scope = `${day}/${region}/s3/aws4_request`;
  const q = new URLSearchParams({ "X-Amz-Algorithm": "AWS4-HMAC-SHA256", "X-Amz-Credential": `${o.accessKey}/${scope}`, "X-Amz-Date": amz,
    "X-Amz-Expires": String(o.expires ?? 60), "X-Amz-SignedHeaders": "host" });
  const qs = [...q.entries()].sort(([a], [b]) => (a < b ? -1 : 1)).map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(v)}`).join("&");
  const canon = ["PUT", path, qs, `host:${u.host}`, "", "host", "UNSIGNED-PAYLOAD"].join("\n");
  const sts = ["AWS4-HMAC-SHA256", amz, scope, sha(canon)].join("\n");
  const h = (k: Buffer | string, d: string) => createHmac("sha256", k).update(d).digest();
  const sig = createHmac("sha256", h(h(h(h(`AWS4${o.secretKey}`, day), region), "s3"), "aws4_request")).update(sts).digest("hex");
  return `${u.protocol}//${u.host}${path}?${qs}&X-Amz-Signature=${sig}`;
}

export function objectStoreWriterFromEnv(env = process.env, fetchImpl: typeof fetch = fetch): ObjectStorePut | null {
  const endpoint = env.MINIO_ENDPOINT, bucket = env.MINIO_BUCKET, accessKey = env.MINIO_ACCESS_KEY, secretKey = env.MINIO_SECRET_KEY;
  if (!endpoint || !bucket || !accessKey || !secretKey) return null;
  return async (key, bytes, contentType) => {
    const res = await fetchImpl(presignPut({ endpoint, bucket, key, accessKey, secretKey, region: env.MINIO_REGION }), {
      method: "PUT",
      headers: { "Content-Type": contentType || "application/octet-stream", "Content-Length": String(bytes.length) },
      body: new Uint8Array(bytes),
      redirect: "error",
      signal: AbortSignal.timeout(60_000),
    });
    if (!res.ok) throw new HttpError(502, `object store put failed (HTTP ${res.status})`);
  };
}

export const scopedRawRunKey = (s: Scope, sourceId: string, runId: string) =>
  `tenants/${s.tenant_id}/workspaces/${s.workspace_id}/raw-runs/${sourceId}/${runId}`;

export async function archiveRawResponse(
  s: Scope,
  sourceId: string,
  runId: string,
  raw: { status?: number; content_type?: string; headers?: Record<string, string>; body: string | Buffer },
  put?: ObjectStorePut | null,
) {
  const bytes = Buffer.isBuffer(raw.body) ? raw.body : Buffer.from(String(raw.body ?? ""), "utf8");
  const digest = createHash("sha256").update(bytes).digest("hex");
  const contentType = raw.content_type || "application/octet-stream";
  const key = scopedRawRunKey(s, sourceId, runId);
  let storedKey: string | null = null;
  if (put) {
    await put(key, bytes, contentType);
    storedKey = key;
  }
  const meta = {
    status: raw.status ?? 200,
    content_type: contentType,
    headers: raw.headers ?? {},
  };
  return { object_key: storedKey, sha256: digest, bytes: bytes.length, meta };
}

export async function downloadRawResponse(
  db: Db,
  store: ObjectStore | null,
  s: Scope,
  o: { run_id: string },
) {
  const r = (await db.query(
    `SELECT id, source_id, raw_response_key, raw_response_sha256, raw_response_bytes, raw_response_meta FROM flow_collection_runs WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3`,
    [s.tenant_id, s.workspace_id, String(o.run_id ?? "")],
  )).rows[0];
  if (!r || !r.raw_response_key) throw new HttpError(404, "raw response not found");
  const key = String(r.raw_response_key);
  const prefix = `tenants/${s.tenant_id}/workspaces/${s.workspace_id}/raw-runs/`;
  if (!key.startsWith(prefix) || key.includes("..")) throw new HttpError(404, "raw response not found");
  if (!store) throw new HttpError(503, "object storage not configured");
  const got = await store(key);
  const digest = createHash("sha256").update(got.bytes).digest("hex");
  if (r.raw_response_sha256 && digest !== r.raw_response_sha256) {
    throw new HttpError(502, "raw response integrity check failed");
  }
  const meta = typeof r.raw_response_meta === "string" ? JSON.parse(r.raw_response_meta) : (r.raw_response_meta ?? {});
  return {
    run_id: r.id,
    source_id: r.source_id,
    object_key: key,
    content_type: meta.content_type || got.content_type || "application/octet-stream",
    size: got.bytes.length,
    sha256: digest,
    meta,
    data_base64: got.bytes.toString("base64"),
  };
}

