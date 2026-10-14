// Loom Engine P1 core: tenant-scoped sources, idempotent collection requests,
// leased/fenced job execution, cursor advance only after commit, outbox events.
import { createHash, randomBytes } from "node:crypto";
import { affected, type Db, type Q } from "./db.ts";
import type { ExecutionContext } from "./auth.ts";
import { connectorEnvFromProcess, extractMainText, fetchTelegram, fetchWechat, normalizeHandle, telegramMessageId, storeTelegramMedia, SCOPED_MEDIA_KEY,
  type MediaRef,
  type ConnectorBatch, type ConnectorEnv, type ConnectorItem, type SourceRow } from "./connectors.ts";
import { expandQuery, mergeAliases, BUILTIN_TERMS_VERSION, BUILTIN_TERM_GROUPS } from "./terms.ts";
import { defaultArticleReader, documentVideoId, enrichmentStatus, isSubtitleLanguage, ocrClient, subtitleClient, type ArticleReader, type OcrClient, type SubtitleClient } from "./enrichment.ts";

/** Media download attempts (initial + retries) before a failed attachment is left as is. */
export const MEDIA_MAX_ATTEMPTS = 4;
export const OCR_MAX_ATTEMPTS = 3;
/** PRD §6.2 collection job states. `queued` is the PRD's "pending". */
export const JOB_STATES = ["queued", "leased", "running", "retry_wait", "partial", "reconciliation_required", "cancel_requested", "cancelled", "succeeded", "failed"] as const;
export const ACTIVE_JOB_STATES = ["queued", "leased", "running", "retry_wait", "reconciliation_required", "cancel_requested"];
export const MISFIRE_POLICIES = ["coalesce", "skip", "catch_up"] as const;
/** catch_up replays at most this many missed windows (never an unbounded backlog). */
export const MAX_CATCH_UP = 3;
/** skip runs a due window only if it is at most this late. */
export const MISFIRE_GRACE_MS = 120_000;
/** Retry backoff for retry_wait: 30s·2^(attempt-1), capped at 30 min. */
export const retryDelayMs = (attempt: number) => Math.min(30 * 60_000, 30_000 * 2 ** Math.max(0, attempt - 1));
/** A commit whose outcome is unknown (connection dropped mid-COMMIT etc.). */
export const isUnknownOutcome = (e: any) => !(e instanceof HttpError) && /connection (terminated|reset|lost)|ECONNRESET|EPIPE|socket hang up|outcome unknown/i.test(String(e?.message ?? e));
const MEDIA_RETRYABLE = new Set(["failed", "timeout"]);
const isImage = (m: MediaRef) => m.type === "image" || /^image\//.test(m.content_type ?? "");

export type FeedItem = { external_id: string; title: string; body: string; url: string; published_at: string | null };
export type FetchResult = FeedItem[] | ConnectorBatch;
export type Fetcher = (url: string, sourceId: string, src?: SourceRow) => Promise<FetchResult>;

/** Dispatches by source kind: RSS (WeaveSense fetchRss), Telegram, WeChat (connectors.ts). */
export const connectorFetcher = (env: ConnectorEnv): Fetcher => async (url, sourceId, src) => {
  if (src?.kind === "telegram") return fetchTelegram(env, src);
  if (src?.kind === "wechat") return fetchWechat(env, src);
  // Reuse WeaveSense's outbound-policy guarded fetch + RSS/Atom parser, conditionally
  // (If-None-Match / If-Modified-Since from the validators stored with the cursor).
  const { fetchFeed } = await import("../src/lib/server/rss.ts");
  const r = await fetchFeed(url, sourceId, "rss", src?.feed_validators ?? null);
  return { items: r.items, collector: "rss", mode: "live", feed_validators: r.validators, not_modified: r.not_modified, rejected_entries: r.rejected_entries };
};
/** A13 failure classes: auth ⇒ pause; rate_limited/transient ⇒ backoff; permanent ⇒ record only. */
export function classifyFailure(error: string): "auth" | "rate_limited" | "transient" | "permanent" {
  if (/\b(401|403)\b|unauthori[sz]ed|forbidden|credential|auth_key|session (expired|revoked)/i.test(error)) return "auth";
  if (/\b429\b|rate.?limit|too many requests|flood/i.test(error)) return "rate_limited";
  if (/membership_required/i.test(error)) return "auth"; // operator must (re)join the channel
  if (/\b5\d\d\b|timeout|timed out|unavailable|ECONN|ENOTFOUND|EAI_AGAIN|socket/i.test(error)) return "transient";
  return "permanent";
}

class CancelRequested extends Error {}

/**
 * Operation status shown to users (PRD §5.2: queued, running, retrying, completed→succeeded,
 * partial, failed, cancelled) aggregated from the job states.
 */
export function operationStatus(st: string[]): string {
  if (st.every((x) => x === "succeeded")) return "succeeded";
  if (st.every((x) => x === "cancelled")) return "cancelled";
  if (st.some((x) => x === "cancel_requested")) return "cancel_requested";
  if (st.some((x) => x === "reconciliation_required")) return "reconciliation_required";
  if (st.some((x) => x === "leased" || x === "running")) return "running";
  if (st.some((x) => x === "retry_wait")) return "retrying";
  if (st.some((x) => x === "queued")) return "queued";
  if (st.every((x) => x === "succeeded" || x === "partial")) return "partial";
  if (st.some((x) => x === "succeeded" || x === "partial")) return "partial";
  return "failed";
}

export const defaultFetcher: Fetcher = (url, sourceId, src) => connectorFetcher(connectorEnvFromProcess())(url, sourceId, src);
export type Extractor = (url: string) => Promise<string>;
const defaultExtractor: Extractor = async (url) => (await import("../src/lib/server/http.ts")).fetchText(url, 15000);

import { HttpError } from "./errors.ts";
import { nextCron, checkTimezone } from "./cron.ts";
export { HttpError };

export const newId = (p: string) => p + randomBytes(10).toString("hex");
const CHUNK = 1200;
const sha = (s: string) => createHash("sha256").update(s).digest("hex");
type Scope = Pick<ExecutionContext, "tenant_id" | "workspace_id">;

export class Engine {
  db: Db; fetcher: Fetcher; now: () => Date; extractor: Extractor = defaultExtractor;
  articleReader: ArticleReader = defaultArticleReader; subtitles: SubtitleClient = subtitleClient(); ocr: OcrClient = ocrClient();
  connectorEnv: ConnectorEnv = connectorEnvFromProcess();
  /** Platform authorization hook: false ⇒ grant revoked (schedule is disabled). Errors propagate (fail closed, retry next tick). */
  authorize?: (sc: { tenant_id: string; workspace_id: string; grant_actor: string }, scope: string) => Promise<boolean>;
  constructor(db: Db, fetcher: Fetcher = defaultFetcher, now: () => Date = () => new Date()) {
    this.db = db; this.fetcher = fetcher; this.now = now;
  }

  async createSource(s: Scope, input: { url?: string; title?: string; kind?: string; config?: any }) {
    const kind = input.kind ?? "rss";
    let url: string; let config: any = {};
    if (kind === "telegram") {
      const handle = normalizeHandle(input.config?.handle ?? input.url ?? "");
      if (!/^[A-Za-z0-9_]{4,64}$/.test(handle)) throw new HttpError(400, "telegram: channel handle required");
      url = `https://t.me/s/${handle}`; config = { handle };
    } else if (kind === "wechat") {
      const feed = String(input.config?.gateway_source_id ?? "");
      if (!/^[A-Za-z0-9._:-]{1,128}$/.test(feed)) throw new HttpError(400, "wechat: config.gateway_source_id (external gateway feed) required");
      url = `wechat://${feed}`; config = { gateway_source_id: feed, wechat_id: input.config?.wechat_id ?? null };
    } else if (kind === "rss") {
      let u: URL;
      try { u = new URL(input.url ?? ""); } catch { throw new HttpError(400, "invalid url"); }
      if (!/^https?:$/.test(u.protocol)) throw new HttpError(400, "http(s) only");
      url = u.toString();
    } else throw new HttpError(400, "unsupported source kind");
    const id = newId("src_");
    const r = await this.db.query(
      `INSERT INTO flow_sources (id, tenant_id, workspace_id, kind, url, title, config) VALUES ($1,$2,$3,$4,$5,$6,$7)
       ON CONFLICT (tenant_id, workspace_id, url) DO NOTHING RETURNING *`,
      [id, s.tenant_id, s.workspace_id, kind, url, input.title ?? "", JSON.stringify(config)]);
    if (r.rows[0]) return r.rows[0];
    return (await this.db.query(`SELECT * FROM flow_sources WHERE tenant_id=$1 AND workspace_id=$2 AND url=$3`, [s.tenant_id, s.workspace_id, url])).rows[0];
  }

  /** Revokes a source: scheduled windows stop (scheduler requires active), queued jobs are cancelled, running ones fenced out. */
  async revokeSource(s: Scope, id: string, reason = "revoked") {
    return this.db.tx(async (q) => {
      const r = await q.query(`UPDATE flow_sources SET status='revoked', revoked_reason=$4 WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3 RETURNING id`, [s.tenant_id, s.workspace_id, id, reason.slice(0, 200)]);
      if (!r.rows[0]) throw new HttpError(404, "source not found");
      const c = await q.query(`UPDATE flow_collection_runs SET status='cancelled', finished_at=now(), lease_owner=NULL, fencing_token=fencing_token+1, error='source revoked'
          WHERE tenant_id=$1 AND source_id=$2 AND status IN ('queued','leased','running','retry_wait','cancel_requested')`, [s.tenant_id, id]);
      return { source_id: id, status: "revoked", cancelled_runs: affected(c) };
    });
  }

  /** Connector acceptance records (real small-sample collections; mode=fixture for test doubles). */
  async listConnectorSamples(s: Scope) {
    return (await this.db.query(`SELECT * FROM flow_connector_samples WHERE tenant_id=$1 AND workspace_id=$2 ORDER BY recorded_at DESC LIMIT 100`, [s.tenant_id, s.workspace_id])).rows;
  }

  /** On-demand main-text extraction for a document URL; a changed text becomes a new revision (old citations keep theirs). */
  private async currentDocument(s: Scope, id: string) {
    const d = (await this.db.query(`SELECT d.*, r.content_hash, r.body FROM flow_documents d JOIN flow_document_revisions r ON r.document_id=d.id AND r.revision=d.current_revision
        WHERE d.tenant_id=$1 AND d.workspace_id=$2 AND d.id=$3`, [s.tenant_id, s.workspace_id, id])).rows[0];
    if (!d) throw new HttpError(404, "document not found");
    return d;
  }

  /** Writes title+text as a new revision (old revisions kept) and re-indexes; unchanged content is a no-op. */
  private async addRevision(d: any, title: string, text: string, tag: string, extra: Record<string, unknown> = {}) {
    const hash = sha(`${title}\n${text}`);
    if (hash === d.content_hash) return { document_id: d.id, revision: d.current_revision, changed: false, chars: text.length, ...extra };
    return this.db.tx(async (q) => {
      // Fenced on the revision we read: a concurrent enrichment loses instead of overwriting.
      const rev = d.current_revision + 1;
      if (affected(await q.query(`UPDATE flow_documents SET current_revision=$2 WHERE id=$1 AND current_revision=$3`, [d.id, rev, d.current_revision])) !== 1) throw new HttpError(409, "document changed concurrently");
      await q.query(`INSERT INTO flow_document_revisions (document_id, revision, tenant_id, content_hash, title, body, collected_by_run) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
        [d.id, rev, d.tenant_id, hash, title, text, `${tag}:${d.id}:${rev}`]);
      await this.indexChunks(q, { tenant_id: d.tenant_id, workspace_id: d.workspace_id }, d.source_id, d.id, rev, `${title}\n\n${text}`, d.published_at ? new Date(d.published_at).toISOString() : null);
      return { document_id: d.id, revision: rev, changed: true, chars: text.length, ...extra };
    });
  }

  /** On-demand full text. via "html" (default): guarded fetch + main-text extraction. via "reader": upstream article reader (opt-in, host allowlist). */
  async extractDocument(s: Scope, o: { document_id: string; via?: "html" | "reader" }) {
    const d = await this.currentDocument(s, o.document_id);
    if (!/^https?:\/\//.test(d.url)) throw new HttpError(422, "document has no http(s) url to extract");
    const via = o.via ?? "html";
    if (via !== "html" && via !== "reader") throw new HttpError(400, "via must be html or reader");
    let title: string, text: string;
    if (via === "reader") {
      try { const a = await this.articleReader(d.url); title = a.title; text = a.body.slice(0, 200_000); }
      catch (e: any) { const code = String(e?.code ?? e?.message ?? e); const c = /^article_[a-z_]+$/.test(code) ? code : "article_unavailable"; throw new HttpError(enrichmentStatus(c), c); }
    } else {
      let html: string;
      try { html = await this.extractor(d.url); } catch (e: any) { throw new HttpError(502, `extraction fetch failed: ${String(e?.message ?? e).slice(0, 200)}`); }
      ({ title, text } = extractMainText(html));
    }
    if (text.length < 40 || text.length <= String(d.body).length) return { document_id: d.id, revision: d.current_revision, changed: false, chars: text.length, via };
    return this.addRevision(d, title || d.title, text, via === "reader" ? "reader" : "extract", { via });
  }

  /** YouTube subtitles via the upstream subtitle worker; appended to the body as a new revision. */
  async transcribeDocument(s: Scope, o: { document_id: string; language?: string }) {
    const language = o.language ?? "en";
    if (!isSubtitleLanguage(language)) throw new HttpError(400, "language must be en, zh-Hans or zh-Hant");
    const d = await this.currentDocument(s, o.document_id);
    let t;
    try { t = await this.subtitles(documentVideoId(d), language); }
    catch (e: any) { const code = String(e?.code ?? e?.message ?? e); const c = /^subtitle_[a-z_]+$/.test(code) ? code : "subtitle_unavailable"; throw new HttpError(enrichmentStatus(c), c); }
    if (t.status === "no_subtitles") return { document_id: d.id, revision: d.current_revision, changed: false, status: "no_subtitles", language };
    const marker = `[字幕: ${language}; ${t.kind}]`;
    const base = String(d.body);
    if (base.includes(marker)) return { document_id: d.id, revision: d.current_revision, changed: false, status: "ok", language, kind: t.kind };
    return this.addRevision(d, d.title, `${base}\n\n${marker}\n${t.text}`.slice(0, 400_000), "transcript", { status: "ok", language, kind: t.kind, digest: t.digest });
  }

  /**
   * §6.4 retry of attachments not stored during a run: only failed/timeout telegram:// targets with attempts left;
   * text and cursor are untouched. The media JSON update is optimistic, so a concurrent retry cannot clobber it.
   */
  async retryMedia(limit = 20) {
    const r = await this.db.query(`SELECT d.id, d.tenant_id, d.workspace_id, d.media FROM flow_documents d JOIN flow_sources s ON s.id=d.source_id AND s.tenant_id=d.tenant_id
        WHERE s.kind='telegram' AND s.status='active' AND EXISTS (SELECT 1 FROM jsonb_array_elements(d.media) m
          WHERE m->>'status' IN ('failed','timeout') AND coalesce(m->>'ref', m->>'url') LIKE 'telegram://%' AND coalesce((m->>'attempts')::int, 1) < $1)
        ORDER BY d.created_at DESC LIMIT $2`, [MEDIA_MAX_ATTEMPTS, limit]);
    let stored = 0, failed = 0;
    for (const d of r.rows as any[]) {
      const before = JSON.stringify(typeof d.media === "string" ? JSON.parse(d.media) : d.media);
      const media: MediaRef[] = JSON.parse(before);
      const retry = media.filter((m) => MEDIA_RETRYABLE.has(m.status) && /^telegram:\/\//.test(m.ref ?? m.url) && (m.attempts ?? 1) < MEDIA_MAX_ATTEMPTS);
      for (const m of retry) { m.ref = m.ref ?? m.url; m.attempts = m.attempts ?? 1; m.reason = "pending"; }
      await storeTelegramMedia(this.connectorEnv, [{ external_id: d.id, title: "", body: "", url: "", published_at: null, media }], Date.now, { tenant_id: d.tenant_id, workspace_id: d.workspace_id });
      for (const m of retry) m.status === "stored" ? stored++ : failed++;
      const u = await this.db.query(`UPDATE flow_documents SET media=$2::jsonb WHERE id=$1 AND tenant_id=$3 AND media=$4::jsonb`, [d.id, JSON.stringify(media), d.tenant_id, before]);
      if (affected(u) && retry.some((m) => m.status === "stored")) {
        await this.db.tx((q) => this.emit(q, d, `media:${d.id}`, d.id, "media.stored", { document_id: d.id, stored: retry.filter((m) => m.status === "stored").map((m) => m.url) }));
        if (process.env.OCR_ENABLED === "true") await this.enqueueOcr(d, media);
      }
    }
    return { documents: r.rows.length, stored, failed };
  }

  private async enqueueOcr(d: { id: string; tenant_id: string; workspace_id: string }, media: MediaRef[]) {
    let n = 0;
    for (const m of media) {
      if (m.status !== "stored" || !isImage(m) || !SCOPED_MEDIA_KEY.test(m.url)) continue;
      n += affected(await this.db.query(`INSERT INTO flow_media_ocr (tenant_id, workspace_id, document_id, object_key) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
        [d.tenant_id, d.workspace_id, d.id, m.url]));
    }
    return n;
  }

  /** OCR one target: text is appended as a new revision (marker per object, so a re-run is a no-op); usage.recorded ocr_pages. */
  private async runOcr(t: { tenant_id: string; workspace_id: string; document_id: string; object_key: string; attempts: number }) {
    const sc = { tenant_id: t.tenant_id, workspace_id: t.workspace_id };
    try {
      const out = await this.ocr(t.object_key, sc);
      const d = await this.currentDocument(sc, t.document_id);
      const marker = `[OCR: ${t.object_key}]`;
      let rev = d.current_revision;
      if (out.text.trim() && !String(d.body).includes(marker)) rev = (await this.addRevision(d, d.title, `${d.body}\n\n${marker}\n${out.text.trim()}`.slice(0, 400_000), "ocr")).revision;
      await this.db.tx(async (q) => {
        await q.query(`UPDATE flow_media_ocr SET status='succeeded', attempts=attempts+1, pages=$4, text_digest=$5, revision=$6, error=NULL, updated_at=now()
            WHERE tenant_id=$1 AND document_id=$2 AND object_key=$3`, [t.tenant_id, t.document_id, t.object_key, out.pages, out.digest, rev]);
        await this.emit(q, sc, `ocr:${t.document_id}`, t.document_id, "usage.recorded", { usage_type: "ocr_pages", quantity: out.pages, document_id: t.document_id, object_key: t.object_key });
      });
      return { object_key: t.object_key, status: "succeeded", pages: out.pages, revision: rev };
    } catch (e: any) {
      const code = String(e?.code ?? e?.message ?? e).slice(0, 200);
      const status = /_(unsupported)$/.test(code) ? "unsupported" : "failed";
      await this.db.query(`UPDATE flow_media_ocr SET status=$4, attempts=attempts+1, error=$5, updated_at=now() WHERE tenant_id=$1 AND document_id=$2 AND object_key=$3`,
        [t.tenant_id, t.document_id, t.object_key, status, code]);
      return { object_key: t.object_key, status, error: code };
    }
  }

  /** On-demand OCR of a document's stored images (REST/agent). Already-succeeded targets are not re-run. */
  async ocrDocument(s: Scope, o: { document_id: string }) {
    const d = await this.currentDocument(s, o.document_id);
    const media: MediaRef[] = typeof d.media === "string" ? JSON.parse(d.media) : (d.media ?? []);
    if (!media.some((m) => m.status === "stored" && isImage(m))) throw new HttpError(422, "ocr_unsupported: document has no stored image attachment");
    if (!media.some((m) => m.status === "stored" && isImage(m) && SCOPED_MEDIA_KEY.test(m.url))) throw new HttpError(422, "ocr_unsupported: attachment uses a legacy unscoped object key; run the media key migration first");
    await this.enqueueOcr({ id: d.id, tenant_id: s.tenant_id, workspace_id: s.workspace_id }, media);
    const due = (await this.db.query(`SELECT * FROM flow_media_ocr WHERE tenant_id=$1 AND workspace_id=$2 AND document_id=$3 AND status IN ('queued','failed') AND attempts < $4 ORDER BY object_key`,
      [s.tenant_id, s.workspace_id, d.id, OCR_MAX_ATTEMPTS])).rows;
    const results = [];
    for (const t of due as any[]) results.push(await this.runOcr(t));
    const all = (await this.db.query(`SELECT object_key, status, attempts, pages, revision, error FROM flow_media_ocr WHERE tenant_id=$1 AND workspace_id=$2 AND document_id=$3 ORDER BY object_key`,
      [s.tenant_id, s.workspace_id, d.id])).rows;
    return { document_id: d.id, processed: results, targets: all };
  }

  /** Worker: queued and failed OCR targets with attempts left (system context). */
  async ocrTick(limit = 10) {
    const due = (await this.db.query(`SELECT * FROM flow_media_ocr WHERE status IN ('queued','failed') AND attempts < $1 ORDER BY updated_at LIMIT $2`, [OCR_MAX_ATTEMPTS, limit])).rows;
    for (const t of due as any[]) await this.runOcr(t);
    return due.length;
  }

  async listSources(s: Scope) {
    return (await this.db.query(`SELECT * FROM flow_sources WHERE tenant_id=$1 AND workspace_id=$2 ORDER BY created_at`, [s.tenant_id, s.workspace_id])).rows;
  }

  /** Sources with health (intel_list_sources): status, failures, backoff, last run and cursor position. */
  async listSourcesHealth(s: Scope) {
    const r = await this.db.query(`SELECT s.id, s.kind, s.url, s.title, s.revision, s.status, s.paused_reason, s.revoked_reason, s.consecutive_failures, s.last_error, s.backoff_until,
        c.last_published_at, c.updated_at AS cursor_updated_at,
        (SELECT row_to_json(x) FROM (SELECT r.id AS run_id, r.status, r.items_new, r.items_revised, r.error, r.finished_at FROM flow_collection_runs r
           WHERE r.tenant_id=s.tenant_id AND r.source_id=s.id ORDER BY r.created_at DESC LIMIT 1) x) AS last_run
        FROM flow_sources s LEFT JOIN flow_source_cursors c ON c.source_id=s.id AND c.tenant_id=s.tenant_id
        WHERE s.tenant_id=$1 AND s.workspace_id=$2 ORDER BY s.created_at`, [s.tenant_id, s.workspace_id]);
    const now = this.now().getTime();
    return r.rows.map((x: any) => {
      const lr = typeof x.last_run === "string" ? JSON.parse(x.last_run) : x.last_run;
      const health = x.status !== "active" ? x.status : x.backoff_until && new Date(x.backoff_until).getTime() > now ? "backoff"
        : Number(x.consecutive_failures) > 0 ? "degraded" : lr ? "healthy" : "never_run";
      return { ...x, last_run: lr ?? null, health };
    });
  }

  /** §11 PATCH /sources/{id}: optimistic update on expected_revision; a replay of the applied change is idempotent. */
  async updateSource(s: Scope, id: string, o: { expected_revision?: number; title?: string; status?: string; config?: Record<string, unknown> }) {
    const exp = Number(o.expected_revision);
    if (!Number.isInteger(exp) || exp < 1) throw new HttpError(400, "expected_revision required");
    if (o.status !== undefined && o.status !== "active" && o.status !== "paused") throw new HttpError(400, "status must be active or paused");
    if (o.title !== undefined && typeof o.title !== "string") throw new HttpError(400, "title must be a string");
    return this.db.tx(async (q) => {
      const cur = (await q.query(`SELECT * FROM flow_sources WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3 FOR UPDATE`, [s.tenant_id, s.workspace_id, id])).rows[0];
      if (!cur) throw new HttpError(404, "source not found");
      if (cur.status === "revoked") throw new HttpError(409, "source revoked");
      const cfg = typeof cur.config === "string" ? JSON.parse(cur.config) : (cur.config ?? {});
      // Only connector-neutral settings are editable; the identity (handle/feed/url) needs a new source.
      const patchCfg: Record<string, unknown> = {};
      for (const [k, v] of Object.entries(o.config ?? {})) {
        if (["handle", "gateway_source_id"].includes(k)) throw new HttpError(400, `config.${k} cannot change; create a new source`);
        patchCfg[k] = v;
      }
      const next = { title: o.title ?? cur.title, status: o.status ?? cur.status, config: { ...cfg, ...patchCfg } };
      const same = next.title === cur.title && next.status === cur.status && JSON.stringify(next.config) === JSON.stringify(cfg);
      if (Number(cur.revision) !== exp) {
        // Same change already applied at expected+1 ⇒ replay; anything else is a conflict.
        if (same && Number(cur.revision) === exp + 1) return { ...cur, replayed: true };
        throw new HttpError(409, `revision conflict: current revision is ${cur.revision}`);
      }
      if (same) return { ...cur, replayed: false, changed: false };
      const u = (await q.query(`UPDATE flow_sources SET title=$4, status=$5, config=$6, revision=revision+1, updated_at=now(),
          paused_reason = CASE WHEN $5='paused' THEN coalesce(paused_reason,'operator') ELSE NULL END,
          backoff_until = CASE WHEN $5='active' THEN NULL ELSE backoff_until END,
          consecutive_failures = CASE WHEN $5='active' AND status<>'active' THEN 0 ELSE consecutive_failures END
          WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3 RETURNING *`, [s.tenant_id, s.workspace_id, id, next.title, next.status, JSON.stringify(next.config)])).rows[0];
      if (next.status === "paused") {
        await q.query(`UPDATE flow_collection_runs SET status='cancelled', finished_at=now(), lease_owner=NULL, fencing_token=fencing_token+1, error='source paused'
            WHERE tenant_id=$1 AND source_id=$2 AND status IN ('queued','retry_wait')`, [s.tenant_id, id]);
      }
      await this.emit(q, s, `src:${id}`, `src:${id}:${u.revision}`, "source.updated", { source_id: id, revision: Number(u.revision), status: u.status });
      return { ...u, replayed: false, changed: true };
    });
  }

  /** Revision list of a document (newest first) with evidence counts. */
  async listRevisions(s: Scope, docId: string) {
    const d = (await this.db.query(`SELECT id, current_revision FROM flow_documents WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3`, [s.tenant_id, s.workspace_id, docId])).rows[0];
    if (!d) throw new HttpError(404, "document not found");
    const r = await this.db.query(`SELECT r.revision, r.title, r.content_hash, r.created_at AS collected_at, r.collected_by_run, length(r.body) AS chars,
        (SELECT count(*)::int FROM flow_evidence e WHERE e.tenant_id=r.tenant_id AND e.document_id=r.document_id AND e.revision=r.revision) AS evidence_count
        FROM flow_document_revisions r WHERE r.document_id=$1 AND r.tenant_id=$2 ORDER BY r.revision DESC`, [docId, s.tenant_id]);
    return { document_id: docId, current_revision: d.current_revision, revisions: r.rows };
  }

  /** §7.3 effective term groups for a workspace: built-in dictionary + workspace aliases. */
  async termGroups(s: Scope) {
    const rows = (await this.db.query(`SELECT term, alias FROM flow_term_aliases WHERE tenant_id=$1 AND workspace_id=$2 ORDER BY created_at`, [s.tenant_id, s.workspace_id])).rows;
    return mergeAliases(rows);
  }

  async listTerms(s: Scope) {
    const custom = (await this.db.query(`SELECT term, alias, created_by, created_at FROM flow_term_aliases WHERE tenant_id=$1 AND workspace_id=$2 ORDER BY created_at`, [s.tenant_id, s.workspace_id])).rows;
    return { builtin_version: BUILTIN_TERMS_VERSION, builtin_groups: BUILTIN_TERM_GROUPS.length, builtin: BUILTIN_TERM_GROUPS, custom };
  }

  async addTermAliases(s: Scope, actor: string, o: { items?: { term: string; alias: string }[] }) {
    const items = (o.items ?? []).slice(0, 100).map((i) => ({ term: String(i?.term ?? "").trim(), alias: String(i?.alias ?? "").trim() }));
    if (!items.length || items.some((i) => !i.term || !i.alias || i.term.length > 64 || i.alias.length > 64 || i.term.toLowerCase() === i.alias.toLowerCase()))
      throw new HttpError(400, "items[{term, alias}] required (1-64 chars, distinct)");
    let added = 0;
    for (const i of items) {
      added += affected(await this.db.query(`INSERT INTO flow_term_aliases (tenant_id, workspace_id, term, alias, created_by) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
        [s.tenant_id, s.workspace_id, i.term, i.alias, actor.slice(0, 200)]));
    }
    return { added };
  }

  async deleteTermAlias(s: Scope, o: { term: string; alias: string }) {
    return { deleted: affected(await this.db.query(`DELETE FROM flow_term_aliases WHERE tenant_id=$1 AND workspace_id=$2 AND term=$3 AND alias=$4`, [s.tenant_id, s.workspace_id, String(o.term ?? ""), String(o.alias ?? "")])) };
  }

  private async ownedSources(q: Q, s: Scope, ids: string[]) {
    if (!ids.length) throw new HttpError(400, "source_ids required");
    const r = await q.query(`SELECT id FROM flow_sources WHERE tenant_id=$1 AND workspace_id=$2 AND id = ANY($3::text[])`, [s.tenant_id, s.workspace_id, ids]);
    // Foreign or guessed IDs are indistinguishable from missing ones.
    if (r.rows.length !== new Set(ids).size) throw new HttpError(404, "source not found");
  }

  /** Manual collection. Same key+params replays; same key, different params → 409. */
  async requestCollection(s: Scope, idemKey: string, input: { source_ids: string[] }) {
    if (!idemKey) throw new HttpError(400, "X-Idempotency-Key required");
    const ids = [...new Set(input.source_ids ?? [])].sort();
    const hash = sha(JSON.stringify({ ids, ws: s.workspace_id }));
    return this.db.tx(async (q) => {
      const prior = await q.query(`SELECT * FROM flow_collection_runs WHERE tenant_id=$1 AND idempotency_key=$2 ORDER BY id`, [s.tenant_id, idemKey]);
      if (prior.rows.length) {
        if (prior.rows[0].request_hash !== hash) throw new HttpError(409, "idempotency key reused with different parameters");
        return { operation_id: prior.rows[0].operation_id, job_ids: prior.rows.map((r: any) => r.id), replayed: true };
      }
      await this.ownedSources(q, s, ids);
      const op = newId("op_");
      const jobs: string[] = [];
      for (const sid of ids) {
        const job = newId("job_");
        await q.query(`INSERT INTO flow_collection_runs (id, operation_id, tenant_id, workspace_id, source_id, idempotency_key, request_hash)
                       VALUES ($1,$2,$3,$4,$5,$6,$7)`, [job, op, s.tenant_id, s.workspace_id, sid, idemKey, hash]);
        jobs.push(job);
      }
      await this.emit(q, s, op, jobs[0], "collection.accepted", { job_ids: jobs });
      return { operation_id: op, job_ids: jobs, replayed: false };
    });
  }

  /** Validates a schedule spec (PRD §6.1): interval or 5-field cron in an IANA timezone, plus misfire_policy. */
  scheduleSpec(input: { interval_seconds?: number; cron?: string; timezone?: string; misfire_policy?: string }) {
    const tz = input.timezone || "Asia/Shanghai";
    checkTimezone(tz);
    const misfire = input.misfire_policy || "coalesce";
    if (!(MISFIRE_POLICIES as readonly string[]).includes(misfire)) throw new HttpError(400, `misfire_policy must be one of ${MISFIRE_POLICIES.join("|")}`);
    if (input.cron) {
      if (input.interval_seconds != null) throw new HttpError(400, "give either cron or interval_seconds, not both");
      nextCron(input.cron, tz, this.now()); // validates
      return { kind: "cron", cron: String(input.cron).trim(), interval_seconds: null, timezone: tz, misfire_policy: misfire };
    }
    if (!(Number(input.interval_seconds) >= 60)) throw new HttpError(400, "interval_seconds >= 60");
    return { kind: "interval", cron: null, interval_seconds: Number(input.interval_seconds), timezone: tz, misfire_policy: misfire };
  }

  async createSchedule(s: Scope, actor: string, policy: string, input: { source_ids: string[]; interval_seconds?: number; cron?: string; timezone?: string; misfire_policy?: string; start_at?: string }) {
    const ids = [...new Set(input.source_ids ?? [])].sort();
    const spec = this.scheduleSpec(input);
    return this.db.tx(async (q) => {
      await this.ownedSources(q, s, ids);
      const id = newId("sch_");
      const base = input.start_at ? new Date(input.start_at) : this.now();
      const start = spec.kind === "cron" ? nextCron(spec.cron!, spec.timezone, new Date(base.getTime() - 60_000)) : base;
      await q.query(`INSERT INTO flow_schedules (id, tenant_id, workspace_id, source_ids, interval_seconds, next_run_at, grant_actor, policy_version, kind, cron, timezone, misfire_policy)
                     VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
        [id, s.tenant_id, s.workspace_id, ids, spec.interval_seconds, start.toISOString(), actor, policy, spec.kind, spec.cron, spec.timezone, spec.misfire_policy]);
      return (await q.query(`SELECT * FROM flow_schedules WHERE id=$1`, [id])).rows[0];
    });
  }

  /**
   * Due windows of a schedule at `now` and the next fire time after the last one.
   * misfire_policy: coalesce (default) → one catch-up run for the latest missed window;
   * skip → missed windows are dropped, only a window at most MISFIRE_GRACE_MS late runs;
   * catch_up → the latest MAX_CATCH_UP missed windows each run once.
   */
  dueWindows(sc: any, now: Date): { run: Date[]; next: Date } {
    const all: Date[] = [];
    let at = new Date(sc.next_run_at);
    if (sc.kind === "cron") {
      // Never enumerate more than 2 days of backlog: only the latest windows can run anyway.
      const floor = new Date(now.getTime() - 2 * 86400_000);
      if (at < floor) at = nextCron(sc.cron, sc.timezone, floor);
      while (at <= now && all.length < 5000) { all.push(at); at = nextCron(sc.cron, sc.timezone, at); }
    } else {
      const step = sc.interval_seconds * 1000;
      const behind = Math.max(0, Math.floor((now.getTime() - at.getTime()) / step));
      const first = Math.max(0, behind - MAX_CATCH_UP + 1);
      for (let k = first; k <= behind; k++) all.push(new Date(at.getTime() + k * step));
      at = new Date(at.getTime() + (behind + 1) * step);
    }
    const last = all[all.length - 1];
    let run: Date[] = [];
    if (last) {
      if (sc.misfire_policy === "catch_up") run = all.slice(-MAX_CATCH_UP);
      else if (sc.misfire_policy === "skip") run = now.getTime() - last.getTime() <= MISFIRE_GRACE_MS ? [last] : [];
      else run = [last];
    }
    return { run, next: at };
  }

  async listSchedules(s: Scope) {
    return (await this.db.query(`SELECT * FROM flow_schedules WHERE tenant_id=$1 AND workspace_id=$2 ORDER BY created_at`, [s.tenant_id, s.workspace_id])).rows;
  }

  /**
   * Single scheduler tick: claims due schedules (SKIP LOCKED), creates one run per
   * source keyed by tenant_id+schedule_id+scheduled_at, and advances next_run_at
   * in the same transaction. Missed windows collapse to the latest due window.
   */
  async schedulerTick(limit = 50): Promise<number> {
    const now = this.now();
    if (this.authorize) {
      // Re-check the grant actor's ongoing authorization (outside the scan tx: it is a network call).
      const due = await this.db.query(`SELECT id, tenant_id, workspace_id, grant_actor FROM flow_schedules WHERE enabled AND next_run_at <= $1 ORDER BY next_run_at LIMIT $2`, [now.toISOString(), limit]);
      for (const sc of due.rows as any[]) {
        if (!(await this.authorize(sc, "intel.collect"))) {
          await this.db.query(`UPDATE flow_schedules SET enabled=false, disabled_reason='grant_revoked' WHERE id=$1 AND tenant_id=$2`, [sc.id, sc.tenant_id]);
        }
      }
    }
    return this.db.tx(async (q) => {
      // Single scheduler: only the holder of this xact lock scans due windows.
      const lock = await q.query(`SELECT pg_try_advisory_xact_lock(7314001) AS ok`);
      if (!lock.rows[0]?.ok) return 0;
      const due = await q.query(`SELECT * FROM flow_schedules WHERE enabled AND next_run_at <= $1 ORDER BY next_run_at LIMIT $2 FOR UPDATE SKIP LOCKED`, [now.toISOString(), limit]);
      let created = 0;
      for (const sc of due.rows as any[]) {
        const { run: windows, next } = this.dueWindows(sc, now);
        for (const w of windows) {
          const scheduledAt = w.toISOString();
          const key = `${sc.tenant_id}:${sc.id}:${scheduledAt}`;
          const op = newId("op_");
          for (const sid of sc.source_ids as string[]) {
            const r = await q.query(`INSERT INTO flow_collection_runs (id, operation_id, tenant_id, workspace_id, source_id, idempotency_key, request_hash, schedule_id, scheduled_at)
               SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9 WHERE EXISTS (SELECT 1 FROM flow_sources WHERE id=$5 AND tenant_id=$3 AND status='active' AND (backoff_until IS NULL OR backoff_until <= $10))
               ON CONFLICT DO NOTHING`, [newId("job_"), op, sc.tenant_id, sc.workspace_id, sid, key, sha(key), sc.id, scheduledAt, now.toISOString()]);
            created += affected(r);
          }
        }
        await q.query(`UPDATE flow_schedules SET next_run_at=$2 WHERE id=$1`, [sc.id, next.toISOString()]);
      }
      return created;
    });
  }

  /**
   * Claims one job: queued, retry_wait whose retry_at has come, or a leased/running job whose
   * lease expired (crash recovery). queued/retry_wait → leased, with a new fencing token.
   * cancel_requested jobs whose worker died are finalized to cancelled here.
   */
  async claim(owner: string, leaseMs = 60_000) {
    const now = this.now();
    return this.db.tx(async (q) => {
      await q.query(`UPDATE flow_collection_runs SET status='cancelled', finished_at=$1, lease_owner=NULL, fencing_token=fencing_token+1
          WHERE status='cancel_requested' AND (lease_expires_at IS NULL OR lease_expires_at < $1)`, [now.toISOString()]);
      const r = await q.query(`SELECT id FROM flow_collection_runs
          WHERE status='queued' OR (status='retry_wait' AND retry_at <= $1) OR (status IN ('leased','running') AND lease_expires_at < $1)
          ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, [now.toISOString()]);
      if (!r.rows[0]) return null;
      const u = await q.query(`UPDATE flow_collection_runs SET status='leased', lease_owner=$2, lease_expires_at=$3, heartbeat_at=$4, retry_at=NULL,
          fencing_token=fencing_token+1, attempts=attempts+1 WHERE id=$1 RETURNING *`,
        [r.rows[0].id, owner, new Date(now.getTime() + leaseMs).toISOString(), now.toISOString()]);
      return u.rows[0];
    });
  }

  /**
   * leased → running, or a heartbeat on a running job. Must carry the latest fencing token.
   * Returns "cancel" when the job was asked to stop (the worker then finalizes via execute/stop).
   */
  async heartbeat(run: any, leaseMs = 60_000): Promise<"ok" | "cancel" | "stale"> {
    const now = this.now();
    return this.db.tx(async (q) => {
      const cur = (await q.query(`SELECT status, fencing_token FROM flow_collection_runs WHERE id=$1 FOR UPDATE`, [run.id])).rows[0];
      if (!cur || Number(cur.fencing_token) !== Number(run.fencing_token)) return "stale";
      if (cur.status === "cancel_requested") return "cancel";
      if (cur.status !== "leased" && cur.status !== "running") return "stale";
      await q.query(`UPDATE flow_collection_runs SET status='running', heartbeat_at=$2, lease_expires_at=$3 WHERE id=$1`,
        [run.id, now.toISOString(), new Date(now.getTime() + leaseMs).toISOString()]);
      return "ok";
    });
  }

  /** cancel_requested → cancelled, confirmed by the fenced worker that stopped. */
  private async confirmCancel(run: any) {
    return this.db.tx(async (q) => {
      const r = await q.query(`UPDATE flow_collection_runs SET status='cancelled', finished_at=now(), lease_owner=NULL
          WHERE id=$1 AND fencing_token=$2 AND status='cancel_requested'`, [run.id, run.fencing_token]);
      if (affected(r)) await this.emit(q, run, run.operation_id, run.id, "operation.cancelled", { cancelled: 1, confirmed_by_worker: true });
      return { status: affected(r) ? "cancelled" : "stale" };
    });
  }

  /**
   * Resolves reconciliation_required jobs: the commit either landed (its collection.completed
   * outbox event exists → succeeded) or did not (→ queued again, cursor untouched).
   */
  async reconcile(limit = 20) {
    const rows = (await this.db.query(`SELECT id, tenant_id, workspace_id, operation_id FROM flow_collection_runs WHERE status='reconciliation_required' ORDER BY created_at LIMIT $1`, [limit])).rows as any[];
    const out: { id: string; status: string }[] = [];
    for (const r of rows) {
      await this.db.tx(async (q) => {
        const done = (await q.query(`SELECT 1 FROM flow_outbox WHERE job_id=$1 AND type='collection.completed' LIMIT 1`, [r.id])).rows.length > 0;
        const st = done ? "succeeded" : "queued";
        const u = await q.query(`UPDATE flow_collection_runs SET status=$2, lease_owner=NULL, finished_at=CASE WHEN $2='succeeded' THEN coalesce(finished_at, now()) ELSE NULL END
            WHERE id=$1 AND status='reconciliation_required'`, [r.id, st]);
        if (affected(u)) out.push({ id: r.id, status: st });
      });
    }
    return out;
  }

  /** Executes a claimed run. All writes + cursor + outbox commit atomically, guarded by fencing token. */
  async execute(run: any, hooks: { beforeCommit?: () => void | Promise<void> } = {}) {
    const hb = await this.heartbeat(run);
    if (hb === "cancel") return this.confirmCancel(run);
    if (hb === "stale") throw new HttpError(409, "stale lease (fencing token mismatch)");
    const src = (await this.db.query(`SELECT * FROM flow_sources WHERE id=$1 AND tenant_id=$2`, [run.source_id, run.tenant_id])).rows[0];
    if (!src || src.status !== "active") return this.fail(run, "source revoked");
    const cursor = (await this.db.query(`SELECT last_external_id, feed_etag, feed_last_modified, feed_validator_identity FROM flow_source_cursors WHERE source_id=$1 AND tenant_id=$2`, [src.id, run.tenant_id])).rows[0];
    const feedValidators = cursor?.feed_validator_identity ? { etag: cursor.feed_etag ?? "", last_modified: cursor.feed_last_modified ?? "", identity: cursor.feed_validator_identity } : null;
    let items: ConnectorItem[]; let meta: { collector: string; provider?: string; mode: string } = { collector: "rss", mode: "live" };
    let feed: ConnectorBatch | null = null;
    try {
      const got = await this.fetcher(src.url, src.id, { ...src, config: typeof src.config === "string" ? JSON.parse(src.config) : (src.config ?? {}), last_external_id: cursor?.last_external_id ?? null, feed_validators: feedValidators });
      if (Array.isArray(got)) items = got;
      else { items = got.items; meta = { collector: got.collector, provider: got.provider, mode: got.mode }; feed = got; }
    } catch (e: any) {
      return this.fail(run, String(e?.message ?? e));
    }
    return this.db.tx(async (q) => {
      const cur = await q.query(`SELECT fencing_token, status FROM flow_collection_runs WHERE id=$1 FOR UPDATE`, [run.id]);
      if (cur.rows[0]?.status === "cancel_requested" && Number(cur.rows[0].fencing_token) === Number(run.fencing_token)) throw new CancelRequested();
      if (cur.rows[0]?.status !== "running" || Number(cur.rows[0].fencing_token) !== Number(run.fencing_token)) {
        throw new HttpError(409, "stale lease (fencing token mismatch)");
      }
      const live = (await q.query(`SELECT status FROM flow_sources WHERE id=$1 FOR SHARE`, [src.id])).rows[0];
      if (live?.status !== "active") throw new HttpError(409, "source revoked during collection");
      let maxMsg = src.kind === "telegram" ? telegramMessageId(cursor?.last_external_id ?? "") : NaN;
      let mediaUnsupported = 0;
      let created = 0, revised = 0, maxPub: string | null = null;
      for (const it of items) {
        if (!it.external_id) continue;
        const hash = sha(`${it.title}\n${it.body}`);
        if (src.kind === "telegram") {
          const mid = telegramMessageId(it.external_id);
          if (Number.isFinite(mid) && (!Number.isFinite(maxMsg) || mid > maxMsg)) maxMsg = mid;
        }
        mediaUnsupported += (it.media ?? []).filter((m) => m.status !== "stored").length;
        const ins = await q.query(`INSERT INTO flow_documents (id, tenant_id, workspace_id, source_id, external_id, url, title, published_at, media)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (tenant_id, source_id, external_id) DO NOTHING RETURNING id`,
          [newId("doc_"), run.tenant_id, run.workspace_id, src.id, it.external_id, it.url ?? "", it.title ?? "", it.published_at, JSON.stringify(it.media ?? [])]);
        let docId: string, rev = 1;
        if (ins.rows[0]) { docId = ins.rows[0].id; created++; }
        else {
          const d = (await q.query(`SELECT d.id, d.current_revision, r.content_hash FROM flow_documents d
              JOIN flow_document_revisions r ON r.document_id=d.id AND r.revision=d.current_revision
              WHERE d.tenant_id=$1 AND d.source_id=$2 AND d.external_id=$3`, [run.tenant_id, src.id, it.external_id])).rows[0];
          if (!d || d.content_hash === hash) continue; // dedup: unchanged
          docId = d.id; rev = d.current_revision + 1; revised++;
          await q.query(`UPDATE flow_documents SET current_revision=$2, title=$3 WHERE id=$1`, [docId, rev, it.title ?? ""]);
        }
        await q.query(`INSERT INTO flow_document_revisions (document_id, revision, tenant_id, content_hash, title, body, collected_by_run)
            VALUES ($1,$2,$3,$4,$5,$6,$7)`, [docId, rev, run.tenant_id, hash, it.title ?? "", it.body ?? "", run.id]);
        await this.indexChunks(q, run, src.id, docId, rev, `${it.title ?? ""}\n\n${it.body ?? ""}`, it.published_at);
        if (it.published_at && (!maxPub || it.published_at > maxPub)) maxPub = it.published_at;
      }
      // Cursor advances only inside the same commit as the data.
      // Telegram message-id cursor only moves forward, and only in this commit.
      const lastExt = Number.isFinite(maxMsg) ? String(maxMsg) : (cursor?.last_external_id ?? null);
      await q.query(`INSERT INTO flow_source_cursors (source_id, tenant_id, last_published_at, last_run_id, last_external_id) VALUES ($1,$2,$3,$4,$5)
          ON CONFLICT (source_id) DO UPDATE SET last_published_at = GREATEST(flow_source_cursors.last_published_at, EXCLUDED.last_published_at),
          last_run_id = EXCLUDED.last_run_id, last_external_id = EXCLUDED.last_external_id, updated_at = now()`, [src.id, run.tenant_id, maxPub, run.id, lastExt]);
      if (feed?.feed_validators) {
        // Validators commit with the cursor; a failed run keeps the previous ones.
        await q.query(`UPDATE flow_source_cursors SET feed_etag=$3, feed_last_modified=$4, feed_validator_identity=$5 WHERE source_id=$1 AND tenant_id=$2`,
          [src.id, run.tenant_id, feed.feed_validators.etag.slice(0, 1024), feed.feed_validators.last_modified.slice(0, 1024), feed.feed_validators.identity]);
      }
      // partial: the batch committed but some of it was not fully processed (rejected entries or
      // attachments not stored; those are retried by their own media/OCR jobs).
      const reasons = [(feed?.rejected_entries ?? 0) > 0 ? `rejected_entries=${feed!.rejected_entries}` : "", mediaUnsupported > 0 ? `media_not_stored=${mediaUnsupported}` : ""].filter(Boolean);
      const final = reasons.length ? "partial" : "succeeded";
      await q.query(`UPDATE flow_collection_runs SET status=$8, items_new=$2, items_revised=$3, finished_at=now(), lease_owner=NULL, collector=$4, provider=$5,
          items_rejected=$6, not_modified=$7, partial_reason=$9 WHERE id=$1`,
        [run.id, created, revised, meta.collector, meta.provider ?? null, feed?.rejected_entries ?? 0, feed?.not_modified ?? false, final, reasons.join(",") || null]);
      if (src.kind !== "rss" && items.length) {
        await q.query(`INSERT INTO flow_connector_samples (id, tenant_id, workspace_id, kind, source_id, run_id, collector, provider, mode, items, media_unsupported, sample)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (run_id) DO NOTHING`,
          [newId("smp_"), run.tenant_id, run.workspace_id, src.kind, src.id, run.id, meta.collector, meta.provider ?? null, meta.mode, items.length, mediaUnsupported,
           JSON.stringify(items.slice(0, 3).map((i) => ({ external_id: i.external_id, title: i.title.slice(0, 120), published_at: i.published_at })))]);
      }
      await q.query(`UPDATE flow_sources SET consecutive_failures=0, last_error=NULL, backoff_until=NULL WHERE id=$1`, [src.id]);
      await this.emit(q, run, run.operation_id, run.id, "collection.completed", { source_id: src.id, items_new: created, items_revised: revised });
      if (hooks.beforeCommit) await hooks.beforeCommit();
      return { status: final, items_new: created, items_revised: revised };
    }).catch(async (err) => {
      if (err instanceof CancelRequested) return this.confirmCancel(run);
      if (isUnknownOutcome(err)) {
        // We cannot tell whether COMMIT landed: park the job; reconcile() decides from the outbox.
        await this.db.query(`UPDATE flow_collection_runs SET status='reconciliation_required', error=$3 WHERE id=$1 AND fencing_token=$2 AND status IN ('leased','running')`,
          [run.id, run.fencing_token, String(err?.message ?? err).slice(0, 500)]);
        return { status: "reconciliation_required" };
      }
      throw err;
    });
  }

  private async fail(run: any, error: string) {
    return this.db.tx(async (q) => {
      const cls = classifyFailure(error);
      const cur = (await q.query(`SELECT status, attempts, max_attempts FROM flow_collection_runs WHERE id=$1 AND fencing_token=$2 FOR UPDATE`, [run.id, run.fencing_token])).rows[0];
      if (cur?.status === "cancel_requested") {
        await q.query(`UPDATE flow_collection_runs SET status='cancelled', finished_at=now(), lease_owner=NULL WHERE id=$1`, [run.id]);
        await this.emit(q, run, run.operation_id, run.id, "operation.cancelled", { cancelled: 1, confirmed_by_worker: true });
        return { status: "cancelled" };
      }
      if (!cur || (cur.status !== "running" && cur.status !== "leased")) return { status: "stale" };
      const retryable = (cls === "rate_limited" || cls === "transient") && Number(cur.attempts) < Number(cur.max_attempts);
      if (retryable) {
        // Retryable: retry_wait until retry_at, then the job is claimable again (→ leased).
        await q.query(`UPDATE flow_collection_runs SET status='retry_wait', error=$2, retry_at=$3, lease_owner=NULL WHERE id=$1`,
          [run.id, error.slice(0, 500), new Date(this.now().getTime() + retryDelayMs(Number(cur.attempts))).toISOString()]);
      } else {
        await q.query(`UPDATE flow_collection_runs SET status='failed', error=$2, finished_at=now(), lease_owner=NULL WHERE id=$1`, [run.id, error.slice(0, 500)]);
      }
      await q.query(`UPDATE flow_sources SET consecutive_failures=consecutive_failures+1, last_error=$2 WHERE id=$1`, [run.source_id, error.slice(0, 500)]);
      if (cls === "auth") {
        // Credential invalid/revoked: pause until an operator fixes it (resume = re-activate source).
        await q.query(`UPDATE flow_sources SET status='paused', paused_reason='credential_invalid' WHERE id=$1 AND status='active'`, [run.source_id]);
      } else if (cls === "rate_limited" || cls === "transient") {
        // Exponential backoff: 1m·2^(n-1), capped at 6h; after 8 consecutive failures, pause.
        await q.query(`UPDATE flow_sources SET backoff_until = now() + LEAST(interval '6 hours', interval '1 minute' * power(2, LEAST(consecutive_failures, 12) - 1)),
            status = CASE WHEN consecutive_failures >= 8 THEN 'paused' ELSE status END,
            paused_reason = CASE WHEN consecutive_failures >= 8 THEN 'too_many_failures' ELSE paused_reason END WHERE id=$1`, [run.source_id]);
      }
      if (retryable) {
        await this.emit(q, run, run.operation_id, run.id, "operation.retrying", { error: error.slice(0, 500), class: cls, attempt: Number(cur.attempts) });
        return { status: "retry_wait", error };
      }
      await this.emit(q, run, run.operation_id, run.id, "operation.failed", { error: error.slice(0, 500), class: cls });
      return { status: "failed", error };
    });
  }

  /** Splits a revision into ~CHUNK chars windows (paragraph aware) and indexes them for FTS. */
  async indexChunks(q: Q, run: Scope, sourceId: string, docId: string, rev: number, text: string, pub: string | null) {
    let start = 0, i = 0;
    while (start < text.length) {
      let end = Math.min(text.length, start + CHUNK);
      if (end < text.length) { const br = text.lastIndexOf("\n", end); if (br > start + CHUNK / 2) end = br + 1; }
      const piece = text.slice(start, end);
      await q.query(`INSERT INTO flow_document_chunks (document_id, revision, chunk_index, tenant_id, workspace_id, source_id, char_start, char_end, text, published_at, tsv)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10, to_tsvector('simple', $9))`, [docId, rev, i++, run.tenant_id, run.workspace_id, sourceId, start, end, piece, pub]);
      start = end;
    }
  }

  /** Full-text search over current revisions; filters by time window and sources. */
  async searchDocuments(s: Scope, o: { query: string; source_ids?: string[]; from?: string; to?: string; limit?: number; offset?: number }) {
    if (!o.query?.trim()) throw new HttpError(400, "query required");
    const limit = Math.min(Math.max(Number(o.limit) || 10, 1), 50), offset = Math.max(Number(o.offset) || 0, 0);
    // §7.3: expand with industry dictionary + workspace aliases. The original query keeps FTS-or-substring;
    // alias variants that are Latin-only match through FTS tokens (no loose substring like "AI" in "said").
    const exp = expandQuery(o.query, await this.termGroups(s));
    const fts = exp.variants, sub = exp.variants.filter((v, i) => i === 0 || /[^\x00-\x7f]/.test(v));
    const r = await this.db.query(`SELECT c.document_id, c.revision, c.chunk_index, c.char_start, c.char_end, c.source_id, c.published_at, d.title, d.url,
          (SELECT max(ts_rank(c.tsv, plainto_tsquery('simple', v))) FROM unnest($3::text[]) v) AS rank, left(c.text, 300) AS snippet
        FROM flow_document_chunks c JOIN flow_documents d ON d.id=c.document_id AND d.current_revision=c.revision
        WHERE c.tenant_id=$1 AND c.workspace_id=$2 AND (EXISTS (SELECT 1 FROM unnest($3::text[]) v WHERE c.tsv @@ plainto_tsquery('simple', v))
          OR EXISTS (SELECT 1 FROM unnest($9::text[]) v WHERE c.text ILIKE '%'||v||'%'))
          AND ($4::text[] IS NULL OR c.source_id = ANY($4::text[]))
          AND ($5::timestamptz IS NULL OR c.published_at >= $5) AND ($6::timestamptz IS NULL OR c.published_at < $6)
        ORDER BY rank DESC, c.published_at DESC NULLS LAST, c.document_id, c.chunk_index LIMIT $7 OFFSET $8`,
      [s.tenant_id, s.workspace_id, fts, o.source_ids?.length ? o.source_ids : null, o.from || null, o.to || null, limit + 1, offset, sub]);
    return { items: r.rows.slice(0, limit), next_offset: r.rows.length > limit ? offset + limit : null, expanded_terms: exp.matched };
  }

  /** Chunked read with coverage info so the agent knows what it has not seen. */
  async readDocument(s: Scope, o: { document_id: string; revision?: number; offset?: number; max_chars?: number }) {
    const d = (await this.db.query(`SELECT * FROM flow_documents WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3`, [s.tenant_id, s.workspace_id, o.document_id])).rows[0];
    if (!d) throw new HttpError(404, "document not found");
    const rev = Number(o.revision) || d.current_revision;
    const r = (await this.db.query(`SELECT title, body FROM flow_document_revisions WHERE document_id=$1 AND revision=$2 AND tenant_id=$3`, [d.id, rev, s.tenant_id])).rows[0];
    if (!r) throw new HttpError(404, "revision not found");
    const full = `${r.title}\n\n${r.body}`, off = Math.max(Number(o.offset) || 0, 0), max = Math.min(Math.max(Number(o.max_chars) || 4000, 1), 20000);
    const text = full.slice(off, off + max);
    return { document_id: d.id, revision: rev, title: d.title, url: d.url, offset: off, text,
      coverage: { total_chars: full.length, returned_chars: text.length, truncated: off + text.length < full.length, next_offset: off + text.length < full.length ? off + text.length : null } };
  }

  async previewMonitor(s: Scope, o: { name?: string; keywords: string[]; source_ids: string[]; interval_seconds: number; timezone?: string; misfire_policy?: string }) {
    const ids = [...new Set(o.source_ids ?? [])].sort();
    if (!o.keywords?.length) throw new HttpError(400, "keywords required");
    if (!(o.interval_seconds >= 300)) throw new HttpError(400, "interval_seconds >= 300");
    await this.ownedSources(this.db, s, ids);
    const tz = o.timezone || "UTC";
    try { new Intl.DateTimeFormat("en", { timeZone: tz }); } catch { throw new HttpError(400, "invalid timezone"); }
    const misfire = this.scheduleSpec({ interval_seconds: o.interval_seconds, timezone: tz, misfire_policy: o.misfire_policy }).misfire_policy;
    const spec = { name: o.name || o.keywords.join(", "), keywords: o.keywords, source_ids: ids, interval_seconds: o.interval_seconds, timezone: tz, misfire_policy: misfire };
    const id = newId("mpv_"), now = this.now();
    await this.db.query(`INSERT INTO flow_monitor_previews (id, tenant_id, workspace_id, spec, expires_at) VALUES ($1,$2,$3,$4,$5)`,
      [id, s.tenant_id, s.workspace_id, JSON.stringify(spec), new Date(now.getTime() + 3600_000).toISOString()]);
    return { preview_id: id, ...spec, next_run_at: new Date(now.getTime() + o.interval_seconds * 1000).toISOString(),
      runs_per_day: Math.floor(86400 / o.interval_seconds) * ids.length, expires_at: new Date(now.getTime() + 3600_000).toISOString() };
  }

  /** Applies an approved preview: creates monitor + schedule atomically; idempotent on key. */
  async applyMonitor(s: Scope, actor: string, policy: string, o: { preview_id: string; approval_id: string; idempotency_key: string }) {
    if (!o.idempotency_key || !o.approval_id) throw new HttpError(400, "approval_id and idempotency_key required");
    return this.db.tx(async (q) => {
      const prior = (await q.query(`SELECT * FROM flow_monitors WHERE tenant_id=$1 AND idempotency_key=$2`, [s.tenant_id, o.idempotency_key])).rows[0];
      const pv = (await q.query(`SELECT * FROM flow_monitor_previews WHERE id=$1 AND tenant_id=$2 AND workspace_id=$3 FOR UPDATE`, [o.preview_id, s.tenant_id, s.workspace_id])).rows[0];
      if (!pv) throw new HttpError(404, "preview not found");
      if (prior) {
        if (pv.applied_monitor_id !== prior.id) throw new HttpError(409, "idempotency key reused with different preview");
        return { monitor: prior, replayed: true };
      }
      if (pv.applied_monitor_id) throw new HttpError(409, "preview already applied");
      if (new Date(pv.expires_at) < this.now()) throw new HttpError(410, "preview expired");
      const spec = typeof pv.spec === "string" ? JSON.parse(pv.spec) : pv.spec;
      await this.ownedSources(q, s, spec.source_ids);
      const schId = newId("sch_"), monId = newId("mon_");
      await q.query(`INSERT INTO flow_schedules (id, tenant_id, workspace_id, source_ids, interval_seconds, next_run_at, grant_actor, policy_version, timezone, misfire_policy)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, [schId, s.tenant_id, s.workspace_id, spec.source_ids, spec.interval_seconds, this.now().toISOString(), actor, policy,
          spec.timezone, spec.misfire_policy ?? "coalesce"]);
      const m = (await q.query(`INSERT INTO flow_monitors (id, tenant_id, workspace_id, name, keywords, source_ids, interval_seconds, timezone, schedule_id, approval_id, idempotency_key)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING *`,
        [monId, s.tenant_id, s.workspace_id, spec.name, spec.keywords, spec.source_ids, spec.interval_seconds, spec.timezone, schId, o.approval_id, o.idempotency_key])).rows[0];
      await q.query(`UPDATE flow_monitor_previews SET applied_monitor_id=$2 WHERE id=$1`, [pv.id, monId]);
      return { monitor: m, replayed: false };
    });
  }

  async listMonitors(s: Scope) {
    return (await this.db.query(`SELECT * FROM flow_monitors WHERE tenant_id=$1 AND workspace_id=$2 ORDER BY created_at`, [s.tenant_id, s.workspace_id])).rows;
  }

  /** Cancels queued jobs immediately; running jobs are flagged and fenced out on commit. */
  async cancelOperation(s: Scope, op: string) {
    return this.db.tx(async (q) => {
      const r = await q.query(`SELECT id, status FROM flow_collection_runs WHERE tenant_id=$1 AND workspace_id=$2 AND operation_id=$3 FOR UPDATE`, [s.tenant_id, s.workspace_id, op]);
      if (!r.rows.length) throw new HttpError(404, "operation not found");
      // Not yet owned by a worker → cancelled at once (fencing bumped so a late claim cannot run it).
      const now1 = await q.query(`UPDATE flow_collection_runs SET status='cancelled', finished_at=now(), lease_owner=NULL, fencing_token=fencing_token+1
          WHERE operation_id=$1 AND tenant_id=$2 AND status IN ('queued','retry_wait')`, [op, s.tenant_id]);
      // Owned by a worker → cancel_requested; the worker confirms the stop (→ cancelled) and its writes never commit.
      const req = await q.query(`UPDATE flow_collection_runs SET status='cancel_requested'
          WHERE operation_id=$1 AND tenant_id=$2 AND status IN ('leased','running')`, [op, s.tenant_id]);
      const cancelled = affected(now1), cancel_requested = affected(req);
      if (cancelled || cancel_requested) await this.emit(q, s, op, r.rows[0].id, cancel_requested ? "operation.cancel_requested" : "operation.cancelled", { cancelled, cancel_requested });
      return { operation_id: op, cancelled: cancelled + cancel_requested, cancel_requested };
    });
  }

  /** Records evidence spans for documents in scope; idempotent per key+index. Foreign docs are skipped. */
  async createEvidence(s: Scope, key: string, o: { items: { document_id: string; revision?: number; char_start?: number; char_end?: number; quote?: string }[]; created_by?: string }) {
    if (!key) throw new HttpError(400, "X-Idempotency-Key required");
    const items = (o.items ?? []).slice(0, 50);
    return this.db.tx(async (q) => {
      let created = 0;
      for (const [i, it] of items.entries()) {
        const d = (await q.query(`SELECT current_revision FROM flow_documents WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3`, [s.tenant_id, s.workspace_id, it.document_id])).rows[0];
        if (!d) continue;
        const r = await q.query(`INSERT INTO flow_evidence (id, tenant_id, workspace_id, document_id, revision, char_start, char_end, quote, created_by, idempotency_key)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (tenant_id, idempotency_key) DO NOTHING`,
          [newId("ev_"), s.tenant_id, s.workspace_id, it.document_id, Number(it.revision) || d.current_revision, Math.max(0, Number(it.char_start) || 0),
           Math.max(0, Number(it.char_end) || 0), String(it.quote ?? "").slice(0, 1000), String(o.created_by ?? "agent").slice(0, 200), `${key}:${i}`]);
        created += affected(r);
      }
      return { created };
    });
  }

  async listEvidence(s: Scope, documentId?: string) {
    return (await this.db.query(`SELECT * FROM flow_evidence WHERE tenant_id=$1 AND workspace_id=$2 AND ($3::text IS NULL OR document_id=$3) ORDER BY created_at DESC LIMIT 200`,
      [s.tenant_id, s.workspace_id, documentId || null])).rows;
  }

  async workOnce(owner: string) {
    const run = await this.claim(owner);
    if (!run) return null;
    return { run_id: run.id, ...(await this.execute(run)) };
  }

  async emit(q: Q, s: Scope, op: string, job: string, type: string, payload: object) {
    await q.query(`INSERT INTO flow_outbox (event_id, tenant_id, workspace_id, operation_id, job_id, type, payload) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
      [newId("evt_"), s.tenant_id, s.workspace_id, op, job, type, JSON.stringify(payload)]);
  }

  async getOperation(s: Scope, op: string) {
    const r = await this.db.query(`SELECT id AS job_id, source_id, status, items_new, items_revised, error, attempts, max_attempts, retry_at, partial_reason, created_at, finished_at
        FROM flow_collection_runs WHERE tenant_id=$1 AND workspace_id=$2 AND operation_id=$3 ORDER BY id`, [s.tenant_id, s.workspace_id, op]);
    if (!r.rows.length) throw new HttpError(404, "operation not found");
    const st = r.rows.map((x: any) => x.status);
    return { operation_id: op, status: operationStatus(st), jobs: r.rows };
  }

  async listDocuments(s: Scope, opts: { q?: string; limit?: number; source_id?: string }) {
    const limit = Math.min(Math.max(Number(opts.limit) || 20, 1), 100);
    return (await this.db.query(`SELECT d.id, d.source_id, d.title, d.url, d.published_at, d.current_revision FROM flow_documents d
        WHERE d.tenant_id=$1 AND d.workspace_id=$2 AND ($3::text IS NULL OR d.title ILIKE '%'||$3||'%') AND ($4::text IS NULL OR d.source_id=$4)
        ORDER BY d.published_at DESC NULLS LAST, d.id LIMIT $5`, [s.tenant_id, s.workspace_id, opts.q || null, opts.source_id || null, limit])).rows;
  }

  async getRevision(s: Scope, docId: string, rev: number) {
    const r = await this.db.query(`SELECT r.* FROM flow_document_revisions r JOIN flow_documents d ON d.id=r.document_id
        WHERE d.tenant_id=$1 AND d.workspace_id=$2 AND d.id=$3 AND r.revision=$4`, [s.tenant_id, s.workspace_id, docId, rev]);
    if (!r.rows[0]) throw new HttpError(404, "document not found");
    // §11: a revision comes with the evidence recorded against it.
    const ev = await this.db.query(`SELECT id, char_start, char_end, quote, created_by, created_at FROM flow_evidence WHERE tenant_id=$1 AND workspace_id=$2 AND document_id=$3 AND revision=$4 ORDER BY created_at`,
      [s.tenant_id, s.workspace_id, docId, rev]);
    return { ...r.rows[0], evidence: ev.rows };
  }

  /** Delivers pending outbox events in sequence order; at-least-once (platform dedupes). */
  async dispatchOutbox(send: (ev: any) => Promise<void>, limit = 100) {
    const r = await this.db.query(`SELECT * FROM flow_outbox WHERE delivered_at IS NULL ORDER BY sequence LIMIT $1`, [limit]);
    let n = 0;
    for (const e of r.rows as any[]) {
      const ev = { event_id: e.event_id, operation_id: e.operation_id, job_id: e.job_id, tenant_id: e.tenant_id, workspace_id: e.workspace_id,
        sequence: Number(e.sequence), type: e.type, occurred_at: new Date(e.occurred_at).toISOString(), payload_version: 1, trace_id: e.event_id,
        payload: typeof e.payload === "string" ? JSON.parse(e.payload) : e.payload };
      try { await send(ev); } catch {
        await this.db.query(`UPDATE flow_outbox SET attempts=attempts+1 WHERE event_id=$1`, [e.event_id]);
        break; // preserve order
      }
      await this.db.query(`UPDATE flow_outbox SET delivered_at=now() WHERE event_id=$1`, [e.event_id]);
      n++;
    }
    return n;
  }
}
