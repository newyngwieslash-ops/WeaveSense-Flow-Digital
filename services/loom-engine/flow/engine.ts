// Loom Engine P1 core: tenant-scoped sources, idempotent collection requests,
// leased/fenced job execution, cursor advance only after commit, outbox events.
import { createHash, randomBytes } from "node:crypto";
import { affected, type Db, type Q } from "./db.ts";
import type { ExecutionContext } from "./auth.ts";
import { connectorEnvFromProcess, extractMainText, fetchTelegram, fetchWechat, normalizeHandle, telegramMessageId,
  type ConnectorBatch, type ConnectorEnv, type ConnectorItem, type SourceRow } from "./connectors.ts";

export type FeedItem = { external_id: string; title: string; body: string; url: string; published_at: string | null };
export type FetchResult = FeedItem[] | ConnectorBatch;
export type Fetcher = (url: string, sourceId: string, src?: SourceRow) => Promise<FetchResult>;

/** Dispatches by source kind: RSS (WeaveSense fetchRss), Telegram, WeChat (connectors.ts). */
export const connectorFetcher = (env: ConnectorEnv): Fetcher => async (url, sourceId, src) => {
  if (src?.kind === "telegram") return fetchTelegram(env, src);
  if (src?.kind === "wechat") return fetchWechat(env, src);
  // Reuse WeaveSense's outbound-policy guarded fetch + RSS/Atom parser.
  const { fetchRss } = await import("../src/lib/server/rss.ts");
  return fetchRss(url, sourceId, "rss");
};
/** A13 failure classes: auth ⇒ pause; rate_limited/transient ⇒ backoff; permanent ⇒ record only. */
export function classifyFailure(error: string): "auth" | "rate_limited" | "transient" | "permanent" {
  if (/\b(401|403)\b|unauthori[sz]ed|forbidden|credential|auth_key|session (expired|revoked)/i.test(error)) return "auth";
  if (/\b429\b|rate.?limit|too many requests|flood/i.test(error)) return "rate_limited";
  if (/\b5\d\d\b|timeout|timed out|ECONN|ENOTFOUND|EAI_AGAIN|socket/i.test(error)) return "transient";
  return "permanent";
}

export const defaultFetcher: Fetcher = (url, sourceId, src) => connectorFetcher(connectorEnvFromProcess())(url, sourceId, src);
export type Extractor = (url: string) => Promise<string>;
const defaultExtractor: Extractor = async (url) => (await import("../src/lib/server/http.ts")).fetchText(url, 15000);

export class HttpError extends Error {
  status: number;
  constructor(status: number, msg: string) { super(msg); this.status = status; }
}

export const newId = (p: string) => p + randomBytes(10).toString("hex");
const CHUNK = 1200;
const sha = (s: string) => createHash("sha256").update(s).digest("hex");
type Scope = Pick<ExecutionContext, "tenant_id" | "workspace_id">;

export class Engine {
  db: Db; fetcher: Fetcher; now: () => Date; extractor: Extractor = defaultExtractor;
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
          WHERE tenant_id=$1 AND source_id=$2 AND status IN ('queued','running')`, [s.tenant_id, id]);
      return { source_id: id, status: "revoked", cancelled_runs: affected(c) };
    });
  }

  /** Connector acceptance records (real small-sample collections; mode=fixture for test doubles). */
  async listConnectorSamples(s: Scope) {
    return (await this.db.query(`SELECT * FROM flow_connector_samples WHERE tenant_id=$1 AND workspace_id=$2 ORDER BY recorded_at DESC LIMIT 100`, [s.tenant_id, s.workspace_id])).rows;
  }

  /** On-demand main-text extraction for a document URL; a changed text becomes a new revision (old citations keep theirs). */
  async extractDocument(s: Scope, o: { document_id: string }) {
    const d = (await this.db.query(`SELECT d.*, r.content_hash, r.body FROM flow_documents d JOIN flow_document_revisions r ON r.document_id=d.id AND r.revision=d.current_revision
        WHERE d.tenant_id=$1 AND d.workspace_id=$2 AND d.id=$3`, [s.tenant_id, s.workspace_id, o.document_id])).rows[0];
    if (!d) throw new HttpError(404, "document not found");
    if (!/^https?:\/\//.test(d.url)) throw new HttpError(422, "document has no http(s) url to extract");
    let html: string;
    try { html = await this.extractor(d.url); } catch (e: any) { throw new HttpError(502, `extraction fetch failed: ${String(e?.message ?? e).slice(0, 200)}`); }
    const { title, text } = extractMainText(html);
    if (text.length < 40 || text.length <= String(d.body).length) return { document_id: d.id, revision: d.current_revision, changed: false, chars: text.length };
    const t = title || d.title; const hash = sha(`${t}\n${text}`);
    if (hash === d.content_hash) return { document_id: d.id, revision: d.current_revision, changed: false, chars: text.length };
    return this.db.tx(async (q) => {
      const rev = d.current_revision + 1;
      await q.query(`UPDATE flow_documents SET current_revision=$2 WHERE id=$1`, [d.id, rev]);
      await q.query(`INSERT INTO flow_document_revisions (document_id, revision, tenant_id, content_hash, title, body, collected_by_run) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
        [d.id, rev, d.tenant_id, hash, t, text, `extract:${d.id}:${rev}`]);
      await this.indexChunks(q, { tenant_id: d.tenant_id, workspace_id: d.workspace_id }, d.source_id, d.id, rev, `${t}\n\n${text}`, d.published_at ? new Date(d.published_at).toISOString() : null);
      return { document_id: d.id, revision: rev, changed: true, chars: text.length };
    });
  }

  async listSources(s: Scope) {
    return (await this.db.query(`SELECT * FROM flow_sources WHERE tenant_id=$1 AND workspace_id=$2 ORDER BY created_at`, [s.tenant_id, s.workspace_id])).rows;
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

  async createSchedule(s: Scope, actor: string, policy: string, input: { source_ids: string[]; interval_seconds: number; start_at?: string }) {
    const ids = [...new Set(input.source_ids ?? [])].sort();
    if (!(input.interval_seconds >= 60)) throw new HttpError(400, "interval_seconds >= 60");
    return this.db.tx(async (q) => {
      await this.ownedSources(q, s, ids);
      const id = newId("sch_");
      const start = input.start_at ? new Date(input.start_at) : this.now();
      await q.query(`INSERT INTO flow_schedules (id, tenant_id, workspace_id, source_ids, interval_seconds, next_run_at, grant_actor, policy_version)
                     VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, [id, s.tenant_id, s.workspace_id, ids, input.interval_seconds, start.toISOString(), actor, policy]);
      return (await q.query(`SELECT * FROM flow_schedules WHERE id=$1`, [id])).rows[0];
    });
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
        const step = sc.interval_seconds * 1000;
        let at = new Date(sc.next_run_at).getTime();
        const behind = Math.floor((now.getTime() - at) / step);
        if (behind > 0) at += behind * step;
        const scheduledAt = new Date(at).toISOString();
        const key = `${sc.tenant_id}:${sc.id}:${scheduledAt}`;
        const op = newId("op_");
        for (const sid of sc.source_ids as string[]) {
          const r = await q.query(`INSERT INTO flow_collection_runs (id, operation_id, tenant_id, workspace_id, source_id, idempotency_key, request_hash, schedule_id, scheduled_at)
             SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9 WHERE EXISTS (SELECT 1 FROM flow_sources WHERE id=$5 AND tenant_id=$3 AND status='active' AND (backoff_until IS NULL OR backoff_until <= $10))
             ON CONFLICT DO NOTHING`, [newId("job_"), op, sc.tenant_id, sc.workspace_id, sid, key, sha(key), sc.id, scheduledAt, now.toISOString()]);
          created += affected(r);
        }
        await q.query(`UPDATE flow_schedules SET next_run_at=$2 WHERE id=$1`, [sc.id, new Date(at + step).toISOString()]);
      }
      return created;
    });
  }

  /** Claims one queued run or a run whose lease expired (crash recovery). */
  async claim(owner: string, leaseMs = 60_000) {
    const now = this.now();
    return this.db.tx(async (q) => {
      const r = await q.query(`SELECT id FROM flow_collection_runs
          WHERE status='queued' OR (status='running' AND lease_expires_at < $1)
          ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`, [now.toISOString()]);
      if (!r.rows[0]) return null;
      const u = await q.query(`UPDATE flow_collection_runs SET status='running', lease_owner=$2, lease_expires_at=$3,
          fencing_token=fencing_token+1, attempts=attempts+1 WHERE id=$1 RETURNING *`,
        [r.rows[0].id, owner, new Date(now.getTime() + leaseMs).toISOString()]);
      return u.rows[0];
    });
  }

  /** Executes a claimed run. All writes + cursor + outbox commit atomically, guarded by fencing token. */
  async execute(run: any, hooks: { beforeCommit?: () => void | Promise<void> } = {}) {
    const src = (await this.db.query(`SELECT * FROM flow_sources WHERE id=$1 AND tenant_id=$2`, [run.source_id, run.tenant_id])).rows[0];
    if (!src || src.status !== "active") return this.fail(run, "source revoked");
    const cursor = (await this.db.query(`SELECT last_external_id FROM flow_source_cursors WHERE source_id=$1`, [src.id])).rows[0];
    let items: ConnectorItem[]; let meta: { collector: string; provider?: string; mode: string } = { collector: "rss", mode: "live" };
    try {
      const got = await this.fetcher(src.url, src.id, { ...src, config: typeof src.config === "string" ? JSON.parse(src.config) : (src.config ?? {}), last_external_id: cursor?.last_external_id ?? null });
      if (Array.isArray(got)) items = got;
      else { items = got.items; meta = { collector: got.collector, provider: got.provider, mode: got.mode }; }
    } catch (e: any) {
      return this.fail(run, String(e?.message ?? e));
    }
    return this.db.tx(async (q) => {
      const cur = await q.query(`SELECT fencing_token, status FROM flow_collection_runs WHERE id=$1 FOR UPDATE`, [run.id]);
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
      await q.query(`UPDATE flow_collection_runs SET status='succeeded', items_new=$2, items_revised=$3, finished_at=now(), lease_owner=NULL, collector=$4, provider=$5 WHERE id=$1`,
        [run.id, created, revised, meta.collector, meta.provider ?? null]);
      if (src.kind !== "rss" && items.length) {
        await q.query(`INSERT INTO flow_connector_samples (id, tenant_id, workspace_id, kind, source_id, run_id, collector, provider, mode, items, media_unsupported, sample)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT (run_id) DO NOTHING`,
          [newId("smp_"), run.tenant_id, run.workspace_id, src.kind, src.id, run.id, meta.collector, meta.provider ?? null, meta.mode, items.length, mediaUnsupported,
           JSON.stringify(items.slice(0, 3).map((i) => ({ external_id: i.external_id, title: i.title.slice(0, 120), published_at: i.published_at })))]);
      }
      await q.query(`UPDATE flow_sources SET consecutive_failures=0, last_error=NULL, backoff_until=NULL WHERE id=$1`, [src.id]);
      await this.emit(q, run, run.operation_id, run.id, "collection.completed", { source_id: src.id, items_new: created, items_revised: revised });
      if (hooks.beforeCommit) await hooks.beforeCommit();
      return { status: "succeeded", items_new: created, items_revised: revised };
    });
  }

  private async fail(run: any, error: string) {
    return this.db.tx(async (q) => {
      const r = await q.query(`UPDATE flow_collection_runs SET status='failed', error=$3, finished_at=now(), lease_owner=NULL
          WHERE id=$1 AND fencing_token=$2 AND status='running'`, [run.id, run.fencing_token, error.slice(0, 500)]);
      if (!affected(r)) return { status: "stale" };
      await q.query(`UPDATE flow_sources SET consecutive_failures=consecutive_failures+1, last_error=$2 WHERE id=$1`, [run.source_id, error.slice(0, 500)]);
      const cls = classifyFailure(error);
      if (cls === "auth") {
        // Credential invalid/revoked: pause until an operator fixes it (resume = re-activate source).
        await q.query(`UPDATE flow_sources SET status='paused', paused_reason='credential_invalid' WHERE id=$1 AND status='active'`, [run.source_id]);
      } else if (cls === "rate_limited" || cls === "transient") {
        // Exponential backoff: 1m·2^(n-1), capped at 6h; after 8 consecutive failures, pause.
        await q.query(`UPDATE flow_sources SET backoff_until = now() + LEAST(interval '6 hours', interval '1 minute' * power(2, LEAST(consecutive_failures, 12) - 1)),
            status = CASE WHEN consecutive_failures >= 8 THEN 'paused' ELSE status END,
            paused_reason = CASE WHEN consecutive_failures >= 8 THEN 'too_many_failures' ELSE paused_reason END WHERE id=$1`, [run.source_id]);
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
    const r = await this.db.query(`SELECT c.document_id, c.revision, c.chunk_index, c.char_start, c.char_end, c.source_id, c.published_at, d.title, d.url,
          ts_rank(c.tsv, plainto_tsquery('simple', $3)) AS rank, left(c.text, 300) AS snippet
        FROM flow_document_chunks c JOIN flow_documents d ON d.id=c.document_id AND d.current_revision=c.revision
        WHERE c.tenant_id=$1 AND c.workspace_id=$2 AND (c.tsv @@ plainto_tsquery('simple', $3) OR c.text ILIKE '%'||$3||'%')
          AND ($4::text[] IS NULL OR c.source_id = ANY($4::text[]))
          AND ($5::timestamptz IS NULL OR c.published_at >= $5) AND ($6::timestamptz IS NULL OR c.published_at < $6)
        ORDER BY rank DESC, c.published_at DESC NULLS LAST, c.document_id, c.chunk_index LIMIT $7 OFFSET $8`,
      [s.tenant_id, s.workspace_id, o.query, o.source_ids?.length ? o.source_ids : null, o.from || null, o.to || null, limit + 1, offset]);
    return { items: r.rows.slice(0, limit), next_offset: r.rows.length > limit ? offset + limit : null };
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

  async previewMonitor(s: Scope, o: { name?: string; keywords: string[]; source_ids: string[]; interval_seconds: number; timezone?: string }) {
    const ids = [...new Set(o.source_ids ?? [])].sort();
    if (!o.keywords?.length) throw new HttpError(400, "keywords required");
    if (!(o.interval_seconds >= 300)) throw new HttpError(400, "interval_seconds >= 300");
    await this.ownedSources(this.db, s, ids);
    const tz = o.timezone || "UTC";
    try { new Intl.DateTimeFormat("en", { timeZone: tz }); } catch { throw new HttpError(400, "invalid timezone"); }
    const spec = { name: o.name || o.keywords.join(", "), keywords: o.keywords, source_ids: ids, interval_seconds: o.interval_seconds, timezone: tz };
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
      await q.query(`INSERT INTO flow_schedules (id, tenant_id, workspace_id, source_ids, interval_seconds, next_run_at, grant_actor, policy_version)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, [schId, s.tenant_id, s.workspace_id, spec.source_ids, spec.interval_seconds, this.now().toISOString(), actor, policy]);
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
      await q.query(`UPDATE flow_collection_runs SET status='cancelled', finished_at=now(), lease_owner=NULL, fencing_token=fencing_token+1
          WHERE operation_id=$1 AND tenant_id=$2 AND status IN ('queued','running')`, [op, s.tenant_id]);
      const cancelled = r.rows.filter((x: any) => x.status === "queued" || x.status === "running").length;
      if (cancelled) await this.emit(q, s, op, r.rows[0].id, "operation.cancelled", { cancelled });
      return { operation_id: op, cancelled };
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
    const r = await this.db.query(`SELECT id AS job_id, source_id, status, items_new, items_revised, error, attempts, created_at, finished_at
        FROM flow_collection_runs WHERE tenant_id=$1 AND workspace_id=$2 AND operation_id=$3 ORDER BY id`, [s.tenant_id, s.workspace_id, op]);
    if (!r.rows.length) throw new HttpError(404, "operation not found");
    const st = r.rows.map((x: any) => x.status);
    const status = st.every((x: string) => x === "succeeded") ? "succeeded" : st.every((x: string) => x === "cancelled") ? "cancelled" : st.some((x: string) => x === "queued" || x === "running") ? "running" : "failed";
    return { operation_id: op, status, jobs: r.rows };
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
    return r.rows[0];
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
