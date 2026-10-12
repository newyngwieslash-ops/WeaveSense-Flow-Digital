// Loom Engine P1 core: tenant-scoped sources, idempotent collection requests,
// leased/fenced job execution, cursor advance only after commit, outbox events.
import { createHash, randomBytes } from "node:crypto";
import { affected, type Db, type Q } from "./db.ts";
import type { ExecutionContext } from "./auth.ts";

export type FeedItem = { external_id: string; title: string; body: string; url: string; published_at: string | null };
export type Fetcher = (url: string, sourceId: string) => Promise<FeedItem[]>;

export const defaultFetcher: Fetcher = async (url, sourceId) => {
  // Reuse WeaveSense's outbound-policy guarded fetch + RSS/Atom parser.
  const { fetchRss } = await import("../src/lib/server/rss.ts");
  return fetchRss(url, sourceId, "rss");
};

export class HttpError extends Error {
  status: number;
  constructor(status: number, msg: string) { super(msg); this.status = status; }
}

export const newId = (p: string) => p + randomBytes(10).toString("hex");
const sha = (s: string) => createHash("sha256").update(s).digest("hex");
type Scope = Pick<ExecutionContext, "tenant_id" | "workspace_id">;

export class Engine {
  db: Db; fetcher: Fetcher; now: () => Date;
  constructor(db: Db, fetcher: Fetcher = defaultFetcher, now: () => Date = () => new Date()) {
    this.db = db; this.fetcher = fetcher; this.now = now;
  }

  async createSource(s: Scope, input: { url: string; title?: string }) {
    let u: URL;
    try { u = new URL(input.url); } catch { throw new HttpError(400, "invalid url"); }
    if (!/^https?:$/.test(u.protocol)) throw new HttpError(400, "http(s) only");
    const id = newId("src_");
    const r = await this.db.query(
      `INSERT INTO flow_sources (id, tenant_id, workspace_id, kind, url, title) VALUES ($1,$2,$3,'rss',$4,$5)
       ON CONFLICT (tenant_id, workspace_id, url) DO NOTHING RETURNING *`,
      [id, s.tenant_id, s.workspace_id, u.toString(), input.title ?? ""]);
    if (r.rows[0]) return r.rows[0];
    return (await this.db.query(`SELECT * FROM flow_sources WHERE tenant_id=$1 AND workspace_id=$2 AND url=$3`, [s.tenant_id, s.workspace_id, u.toString()])).rows[0];
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
             SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9 WHERE EXISTS (SELECT 1 FROM flow_sources WHERE id=$5 AND tenant_id=$3 AND status='active')
             ON CONFLICT DO NOTHING`, [newId("job_"), op, sc.tenant_id, sc.workspace_id, sid, key, sha(key), sc.id, scheduledAt]);
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
    let items: FeedItem[];
    try {
      items = await this.fetcher(src.url, src.id);
    } catch (e: any) {
      return this.fail(run, String(e?.message ?? e));
    }
    return this.db.tx(async (q) => {
      const cur = await q.query(`SELECT fencing_token, status FROM flow_collection_runs WHERE id=$1 FOR UPDATE`, [run.id]);
      if (cur.rows[0]?.status !== "running" || Number(cur.rows[0].fencing_token) !== Number(run.fencing_token)) {
        throw new HttpError(409, "stale lease (fencing token mismatch)");
      }
      let created = 0, revised = 0, maxPub: string | null = null;
      for (const it of items) {
        if (!it.external_id) continue;
        const hash = sha(`${it.title}\n${it.body}`);
        const ins = await q.query(`INSERT INTO flow_documents (id, tenant_id, workspace_id, source_id, external_id, url, title, published_at)
            VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (tenant_id, source_id, external_id) DO NOTHING RETURNING id`,
          [newId("doc_"), run.tenant_id, run.workspace_id, src.id, it.external_id, it.url ?? "", it.title ?? "", it.published_at]);
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
        if (it.published_at && (!maxPub || it.published_at > maxPub)) maxPub = it.published_at;
      }
      // Cursor advances only inside the same commit as the data.
      await q.query(`INSERT INTO flow_source_cursors (source_id, tenant_id, last_published_at, last_run_id) VALUES ($1,$2,$3,$4)
          ON CONFLICT (source_id) DO UPDATE SET last_published_at = GREATEST(flow_source_cursors.last_published_at, EXCLUDED.last_published_at),
          last_run_id = EXCLUDED.last_run_id, updated_at = now()`, [src.id, run.tenant_id, maxPub, run.id]);
      await q.query(`UPDATE flow_collection_runs SET status='succeeded', items_new=$2, items_revised=$3, finished_at=now(), lease_owner=NULL WHERE id=$1`, [run.id, created, revised]);
      await q.query(`UPDATE flow_sources SET consecutive_failures=0, last_error=NULL WHERE id=$1`, [src.id]);
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
      await this.emit(q, run, run.operation_id, run.id, "operation.failed", { error: error.slice(0, 500) });
      return { status: "failed", error };
    });
  }

  async workOnce(owner: string) {
    const run = await this.claim(owner);
    if (!run) return null;
    return { run_id: run.id, ...(await this.execute(run)) };
  }

  private async emit(q: Q, s: Scope, op: string, job: string, type: string, payload: object) {
    await q.query(`INSERT INTO flow_outbox (event_id, tenant_id, workspace_id, operation_id, job_id, type, payload) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
      [newId("evt_"), s.tenant_id, s.workspace_id, op, job, type, JSON.stringify(payload)]);
  }

  async getOperation(s: Scope, op: string) {
    const r = await this.db.query(`SELECT id AS job_id, source_id, status, items_new, items_revised, error, attempts, created_at, finished_at
        FROM flow_collection_runs WHERE tenant_id=$1 AND workspace_id=$2 AND operation_id=$3 ORDER BY id`, [s.tenant_id, s.workspace_id, op]);
    if (!r.rows.length) throw new HttpError(404, "operation not found");
    const st = r.rows.map((x: any) => x.status);
    const status = st.every((x: string) => x === "succeeded") ? "succeeded" : st.some((x: string) => x === "queued" || x === "running") ? "running" : "failed";
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
