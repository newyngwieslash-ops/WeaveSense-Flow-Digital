// P3: research runs (single depth, ≤3 per parent run, fixed scope/budget/output),
// reports with citation maps + claims, periodic brief schedules (cron + timezone).
import { affected, type Q } from "./db.ts";
import { Engine, HttpError, newId } from "./engine.ts";
import type { ExecutionContext } from "./auth.ts";
import { BUILTIN_TERM_GROUPS, termPattern } from "./terms.ts";

type Scope = Pick<ExecutionContext, "tenant_id" | "workspace_id">;
export const MAX_RESEARCH_CHILDREN = 3;
export const RESEARCH_BUDGET = Object.freeze({ max_documents: 8, max_quote_chars: 400, max_claims: 8 });
export const CLAIM_KINDS = ["source_statement", "cross_source_verified", "model_inference"] as const;
const json = (v: any) => (typeof v === "string" ? JSON.parse(v) : v);

export { parseCron, checkTimezone, nextCron } from "./cron.ts";
import { nextCron, checkTimezone } from "./cron.ts";

export class Reports {
  e: Engine;
  /** Platform summarization hook (Model Gateway + citation validation + billing). Throw {status:403} when the grant is revoked. */
  /** Platform Model Gateway. Non-blocking: `{pending, task_id}` while the model task runs. */
  /** Brief windows parked on an in-flight model task (key → task id). */
  readonly pendingSummaries = new Map<string, string>();
  summarize?: (input: any) => Promise<{ body: string; claims: any[] } | { pending: true; task_id?: string } | null>;
  constructor(e: Engine) { this.e = e; }
  private get db() { return this.e.db; }

  /** Resolves evidence ids visible to the caller with revision/chunk/truncation facts (citation validation input). */
  async resolveEvidence(s: Scope, ids: string[], q?: Q) {
    const r = await (q ?? this.db).query(`SELECT e.id AS evidence_id, e.document_id, e.revision, e.char_start, e.char_end, e.quote, d.source_id, d.current_revision, d.title,
          c.chunk_index, c.char_end AS chunk_end
        FROM flow_evidence e JOIN flow_documents d ON d.id=e.document_id AND d.tenant_id=e.tenant_id
        LEFT JOIN LATERAL (SELECT chunk_index, char_end FROM flow_document_chunks k WHERE k.document_id=e.document_id AND k.revision=e.revision
            AND k.char_start <= e.char_start ORDER BY k.char_start DESC LIMIT 1) c ON true
        WHERE e.tenant_id=$1 AND e.workspace_id=$2 AND e.id = ANY($3::text[])`, [s.tenant_id, s.workspace_id, [...new Set(ids ?? [])].slice(0, 200)]);
    return r.rows.map((x: any) => ({ evidence_id: x.evidence_id, document_id: x.document_id, source_id: x.source_id, title: x.title, revision: x.revision,
      current_revision: x.current_revision, chunk_index: x.chunk_index ?? 0, char_start: x.char_start, char_end: x.char_end, quote: x.quote,
      truncated: (x.chunk_end != null && x.char_end > x.chunk_end) || x.quote.length < x.char_end - x.char_start }));
  }

  /**
   * Persists a report whose citations were already validated by the platform.
   * Defense in depth: every evidence id must still exist in this tenant/workspace at the cited revision.
   */
  async saveReport(s: Scope, actor: string, key: string, o: any, hooks: { q?: Q; brief?: { schedule_id: string; scheduled_at: string } } = {}) {
    if (!key) throw new HttpError(400, "X-Idempotency-Key required");
    const claims = Array.isArray(o.claims) ? o.claims.slice(0, 100) : [];
    if (!String(o.title ?? "").trim()) throw new HttpError(400, "title required");
    const ids = claims.flatMap((c: any) => (c.citations ?? []).map((x: any) => String(x.evidence_id)));
    const known = new Map((await this.resolveEvidence(s, ids, hooks.q)).map((x) => [x.evidence_id, x]));
    for (const c of claims) {
      if (!CLAIM_KINDS.includes(c.kind)) throw new HttpError(400, "invalid claim kind");
      for (const x of c.citations ?? []) {
        const ev = known.get(String(x.evidence_id));
        if (!ev || Number(x.revision) !== ev.revision) throw new HttpError(422, "citation evidence not found at cited revision");
      }
    }
    const run = async (q: Q) => {
      const prior = (await q.query(`SELECT id FROM flow_reports WHERE tenant_id=$1 AND idempotency_key=$2`, [s.tenant_id, key])).rows[0];
      if (prior) return { report_id: prior.id, replayed: true };
      const id = newId("rpt_");
      const ins = await q.query(`INSERT INTO flow_reports (id, tenant_id, workspace_id, kind, title, body, skill, material_scope, coverage, research_run_id, brief_schedule_id, scheduled_at, created_by, idempotency_key)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT DO NOTHING`,
        [id, s.tenant_id, s.workspace_id, hooks.brief ? "brief" : "report", String(o.title).slice(0, 300), String(o.body ?? "").slice(0, 100_000),
         JSON.stringify(o.skill ?? {}), JSON.stringify(o.material_scope ?? {}), JSON.stringify(o.coverage ?? {}), o.research_run_id ?? null,
         hooks.brief?.schedule_id ?? null, hooks.brief?.scheduled_at ?? null, actor, key]);
      if (!affected(ins)) return { report_id: null, replayed: true }; // brief window already produced
      for (const [i, c] of claims.entries()) {
        const cites = (c.citations ?? []).map((x: any) => { const ev = known.get(String(x.evidence_id))!; return { ...ev, current_revision: undefined }; });
        await q.query(`INSERT INTO flow_claims (id, tenant_id, report_id, idx, text, kind, citations) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
          [newId("clm_"), s.tenant_id, id, i, String(c.text).slice(0, 4000), c.kind, JSON.stringify(cites)]);
      }
      await this.e.emit(q, s, id, id, "report.completed", { report_id: id, title: String(o.title).slice(0, 300), kind: hooks.brief ? "brief" : "report", claims: claims.length });
      return { report_id: id, replayed: false };
    };
    return hooks.q ? run(hooks.q) : this.db.tx(run);
  }

  async listReports(s: Scope) {
    return (await this.db.query(`SELECT r.id, r.kind, r.title, r.coverage, r.brief_schedule_id, r.scheduled_at, r.created_at,
        (SELECT count(*)::int FROM flow_claims c WHERE c.report_id=r.id) AS claims
        FROM flow_reports r WHERE r.tenant_id=$1 AND r.workspace_id=$2 ORDER BY r.created_at DESC LIMIT 200`, [s.tenant_id, s.workspace_id])).rows
      .map((r: any) => ({ ...r, coverage: json(r.coverage) }));
  }

  /** Report detail. Citations resolve against the cited (original) revision, so later document updates don't change old reports. */
  async getReport(s: Scope, id: string) {
    const r = (await this.db.query(`SELECT * FROM flow_reports WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3`, [s.tenant_id, s.workspace_id, id])).rows[0];
    if (!r) throw new HttpError(404, "report not found");
    const claims = (await this.db.query(`SELECT idx, text, kind, citations FROM flow_claims WHERE report_id=$1 AND tenant_id=$2 ORDER BY idx`, [id, s.tenant_id])).rows;
    for (const c of claims as any[]) {
      c.citations = json(c.citations);
      for (const x of c.citations) {
        const k = (await this.db.query(`SELECT text FROM flow_document_chunks WHERE tenant_id=$1 AND document_id=$2 AND revision=$3 AND chunk_index=$4`,
          [s.tenant_id, x.document_id, x.revision, x.chunk_index])).rows[0];
        x.chunk_text = k?.text ?? null;
      }
    }
    return { ...r, skill: json(r.skill), material_scope: json(r.material_scope), coverage: json(r.coverage), claims };
  }

  /** Builds extractive claims + coverage from a fixed evidence scope (no model calls). */
  private async gather(s: Scope, q: Q | null, key: string, o: { query: string; source_ids?: string[]; document_ids?: string[]; from?: string; to?: string }, createdBy: string) {
    const hits = (await this.e.searchDocuments(s, { query: o.query, source_ids: o.source_ids, from: o.from, to: o.to, limit: 50 })).items
      .filter((h: any) => !o.document_ids?.length || o.document_ids.includes(h.document_id));
    const seen = new Set<string>(); const picked: any[] = [];
    for (const h of hits) { if (!seen.has(h.document_id) && picked.length < RESEARCH_BUDGET.max_documents) { seen.add(h.document_id); picked.push(h); } }
    const db = q ?? this.db;
    const claims: any[] = []; const evidence: string[] = [];
    for (const [i, h] of picked.entries()) {
      const quote = String(h.snippet ?? "").slice(0, RESEARCH_BUDGET.max_quote_chars);
      const ev = (await db.query(`INSERT INTO flow_evidence (id, tenant_id, workspace_id, document_id, revision, char_start, char_end, quote, created_by, idempotency_key)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (tenant_id, idempotency_key) DO UPDATE SET quote=flow_evidence.quote RETURNING id`,
        [newId("ev_"), s.tenant_id, s.workspace_id, h.document_id, h.revision, h.char_start, h.char_start + quote.length, quote, createdBy, `${key}:ev:${i}`])).rows[0].id;
      evidence.push(ev);
      if (claims.length < RESEARCH_BUDGET.max_claims) claims.push({ text: `${h.title}: ${quote.replace(/\s+/g, " ").trim()}`, kind: "source_statement", citations: [{ evidence_id: ev, revision: h.revision }], source_id: h.source_id });
    }
    const bySource = new Map<string, any>();
    for (const c of claims) if (!bySource.has(c.source_id)) bySource.set(c.source_id, c);
    if (bySource.size >= 2) {
      claims.push({ text: `${bySource.size} independent sources report on "${o.query}".`, kind: "cross_source_verified",
        citations: [...bySource.values()].slice(0, 3).map((c) => c.citations[0]) });
    }
    // Coverage: truncation, partial source failures and conflicting (revised) evidence surface in the report.
    const scopeSources = o.source_ids?.length ? o.source_ids : [...new Set(hits.map((h: any) => h.source_id))];
    const failed = scopeSources.length ? (await db.query(`SELECT DISTINCT ON (source_id) source_id, status, error FROM flow_collection_runs
        WHERE tenant_id=$1 AND workspace_id=$2 AND source_id = ANY($3::text[]) ORDER BY source_id, created_at DESC`, [s.tenant_id, s.workspace_id, scopeSources])).rows
      .filter((r: any) => r.status === "failed" || r.status === "retry_wait").map((r: any) => ({ source_id: r.source_id, error: r.error, status: r.status })) : [];
    const revised = picked.filter((h) => h.revision > 1).map((h) => ({ document_id: h.document_id, revision: h.revision,
      note: "document changed after first collection; earlier revision may conflict" }));
    const coverage = { documents_considered: hits.length, documents_used: picked.length, truncated: hits.length > picked.length || picked.some((h) => String(h.snippet ?? "").length >= 300),
      source_failures: failed, conflicting_evidence: revised, budget: RESEARCH_BUDGET };
    return { claims: claims.map(({ source_id, ...c }) => c), evidence_ids: evidence, coverage };
  }

  /**
   * §8.3 intel_compare_evidence: structured, read-only comparison of evidence items (no model call).
   * Groups by source, extracts numeric facts, flags stale revisions, shared terms and numeric divergences.
   */
  async compareEvidence(s: Scope, o: { evidence_ids?: string[] }) {
    const ids = [...new Set((o.evidence_ids ?? []).map(String))];
    if (ids.length < 2 || ids.length > 20) throw new HttpError(400, "evidence_ids: 2 to 20 ids required");
    const found = await this.resolveEvidence(s, ids);
    const pub = found.length ? (await this.db.query(`SELECT id, published_at, url FROM flow_documents WHERE tenant_id=$1 AND workspace_id=$2 AND id = ANY($3::text[])`,
      [s.tenant_id, s.workspace_id, [...new Set(found.map((f) => f.document_id))]])).rows : [];
    const meta = new Map(pub.map((r: any) => [r.id, r]));
    const items = found.map((f) => ({ ...f, published_at: meta.get(f.document_id)?.published_at ?? null, url: meta.get(f.document_id)?.url ?? "",
      stale: f.current_revision > f.revision, numbers: numericFacts(f.quote) }));
    const termHits = new Map<string, Set<string>>();
    for (const it of items) {
      for (const g of BUILTIN_TERM_GROUPS) {
        if (g.terms.some((t) => { const re = termPattern(t); return re ? re.test(it.quote) : it.quote.includes(t); })) {
          const k = g.terms[0]; if (!termHits.has(k)) termHits.set(k, new Set()); termHits.get(k)!.add(it.evidence_id);
        }
      }
      for (const w of new Set((it.quote.toLowerCase().match(/[a-z][a-z0-9-]{3,}/g) ?? []))) {
        if (!termHits.has(w)) termHits.set(w, new Set()); termHits.get(w)!.add(it.evidence_id);
      }
    }
    const shared_terms = [...termHits.entries()].filter(([, v]) => v.size >= 2).map(([term, v]) => ({ term, evidence_ids: [...v] })).slice(0, 30);
    const divergences: any[] = [];
    for (let i = 0; i < items.length; i++) for (let j = i + 1; j < items.length; j++) {
      const a = items[i], b = items[j];
      if (a.source_id === b.source_id) continue;
      for (const na of a.numbers) for (const nb of b.numbers) {
        if (na.unit && na.unit === nb.unit && na.value !== nb.value) divergences.push({ unit: na.unit, a: { evidence_id: a.evidence_id, value: na.value, text: na.text }, b: { evidence_id: b.evidence_id, value: nb.value, text: nb.text } });
      }
    }
    const sources = [...new Set(items.map((i) => i.source_id))];
    return { items, not_found: ids.filter((id) => !found.some((f) => f.evidence_id === id)), sources: sources.length,
      by_source: sources.map((sid) => ({ source_id: sid, evidence_ids: items.filter((i) => i.source_id === sid).map((i) => i.evidence_id) })),
      shared_terms, divergences: divergences.slice(0, 20), stale_evidence: items.filter((i) => i.stale).map((i) => i.evidence_id),
      note: "structural comparison only: shared terms and numeric divergences are lexical signals, not verified conclusions" };
  }

  /** §11 REST: research run status (no model calls). */
  async getResearch(s: Scope, id: string) {
    const r = (await this.db.query(`SELECT * FROM flow_research_runs WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3`, [s.tenant_id, s.workspace_id, id])).rows[0];
    if (!r) throw new HttpError(404, "research run not found");
    return { ...r, output: r.output == null ? null : json(r.output), skill: json(r.skill), evidence_scope: json(r.evidence_scope), budget: json(r.budget) };
  }

  /** §11 REST: cancel a running research run; a late completion cannot overwrite the cancellation. */
  async cancelResearch(s: Scope, id: string, reason = "cancelled") {
    const r = await this.db.query(`UPDATE flow_research_runs SET status='cancelled', cancel_reason=$4, finished_at=now()
        WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3 AND status IN ('queued','running') RETURNING id`, [s.tenant_id, s.workspace_id, id, reason.slice(0, 200)]);
    if (!r.rows[0]) { const cur = await this.getResearch(s, id); return { id, status: cur.status, cancelled: false }; }
    return { id, status: "cancelled", cancelled: true };
  }

  /** In-flight async research (REST 202 path); tests await settle(). */
  readonly inflight = new Map<string, Promise<unknown>>();
  async settle(id: string) { await this.inflight.get(id); }

  /** Single-depth child research for an agent run: fixed scope, budget and output shape; runs in-process. */
  async startResearch(s: Scope, key: string, o: { parent_run_id: string; question: string; evidence_scope?: { source_ids?: string[]; document_ids?: string[]; from?: string; to?: string }; skill?: any }, opts: { async?: boolean } = {}) {
    if (!key) throw new HttpError(400, "X-Idempotency-Key required");
    if (!o.parent_run_id || !String(o.question ?? "").trim()) throw new HttpError(400, "parent_run_id and question required");
    const prior = (await this.db.query(`SELECT * FROM flow_research_runs WHERE tenant_id=$1 AND idempotency_key=$2`, [s.tenant_id, key])).rows[0];
    if (prior) return { ...prior, output: json(prior.output), replayed: true };
    const scope = o.evidence_scope ?? {};
    const id = await this.db.tx(async (q) => {
      if ((await q.query(`SELECT 1 FROM flow_research_runs WHERE id=$1`, [o.parent_run_id])).rows.length) throw new HttpError(400, "research runs cannot start nested research (single depth)");
      await q.query(`SELECT pg_advisory_xact_lock(hashtext($1))`, [`${s.tenant_id}:${o.parent_run_id}`]);
      const n = (await q.query(`SELECT count(*)::int AS n FROM flow_research_runs WHERE tenant_id=$1 AND parent_run_id=$2`, [s.tenant_id, o.parent_run_id])).rows[0].n;
      if (n >= MAX_RESEARCH_CHILDREN) throw new HttpError(429, `research child limit (${MAX_RESEARCH_CHILDREN}) reached for this run`);
      if (scope.source_ids?.length) {
        const own = await q.query(`SELECT id FROM flow_sources WHERE tenant_id=$1 AND workspace_id=$2 AND id = ANY($3::text[])`, [s.tenant_id, s.workspace_id, scope.source_ids]);
        if (own.rows.length !== new Set(scope.source_ids).size) throw new HttpError(404, "source not found");
      }
      const rid = newId("rr_");
      await q.query(`INSERT INTO flow_research_runs (id, tenant_id, workspace_id, parent_run_id, kind, question, skill, evidence_scope, budget, idempotency_key)
          VALUES ($1,$2,$3,$4,'research',$5,$6,$7,$8,$9)`, [rid, s.tenant_id, s.workspace_id, o.parent_run_id, String(o.question).slice(0, 1000),
        JSON.stringify(o.skill ?? {}), JSON.stringify(scope), JSON.stringify(RESEARCH_BUDGET), key]);
      return rid;
    });
    const exec = async () => {
      try {
        const out = await this.gather(s, null, `research:${id}`, { query: o.question, ...scope }, `research:${id}`);
        const u = await this.db.query(`UPDATE flow_research_runs SET status='succeeded', output=$2, finished_at=now() WHERE id=$1 AND status='running'`, [id, JSON.stringify(out)]);
        if (!affected(u)) return { id, status: "cancelled", replayed: false };
        return { id, status: "succeeded", output: out, replayed: false };
      } catch (err: any) {
        await this.db.query(`UPDATE flow_research_runs SET status='failed', error=$2, finished_at=now() WHERE id=$1 AND status='running'`, [id, String(err?.message ?? err).slice(0, 500)]);
        return { id, status: "failed", error: String(err?.message ?? err), replayed: false };
      }
    };
    if (opts.async) {
      // REST contract: 202 + run id; the caller polls GET /research-runs/{id} or cancels it.
      const p = exec().finally(() => this.inflight.delete(id));
      this.inflight.set(id, p);
      return { id, status: "running", replayed: false, accepted: true };
    }
    return exec();
  }

  async previewBrief(s: Scope, o: { name?: string; query: string; source_ids: string[]; cron: string; timezone: string; window_hours?: number; skill?: any }) {
    if (!String(o.query ?? "").trim()) throw new HttpError(400, "query required");
    const ids = [...new Set(o.source_ids ?? [])].sort();
    if (!ids.length) throw new HttpError(400, "source_ids required");
    const own = await this.db.query(`SELECT id FROM flow_sources WHERE tenant_id=$1 AND workspace_id=$2 AND id = ANY($3::text[])`, [s.tenant_id, s.workspace_id, ids]);
    if (own.rows.length !== ids.length) throw new HttpError(404, "source not found");
    const tz = o.timezone || "UTC";
    const fires: string[] = []; let t = this.e.now();
    for (let i = 0; i < 3; i++) { t = nextCron(o.cron, tz, t); fires.push(t.toISOString()); }
    const spec = { name: String(o.name || o.query).slice(0, 120), query: o.query, source_ids: ids, cron: o.cron.trim(), timezone: tz,
      window_hours: Math.min(Math.max(Number(o.window_hours) || 24, 1), 24 * 31), skill: o.skill ?? {} };
    const id = newId("bpv_");
    await this.db.query(`INSERT INTO flow_brief_previews (id, tenant_id, workspace_id, spec, expires_at) VALUES ($1,$2,$3,$4,$5)`,
      [id, s.tenant_id, s.workspace_id, JSON.stringify(spec), new Date(this.e.now().getTime() + 30 * 60_000).toISOString()]);
    return { preview_id: id, spec, next_runs: fires };
  }

  async applyBrief(s: Scope, actor: string, policy: string, o: { preview_id: string; approval_id?: string; idempotency_key: string }) {
    if (!o.idempotency_key) throw new HttpError(400, "X-Idempotency-Key required");
    if (!o.approval_id) throw new HttpError(403, "approval required");
    return this.db.tx(async (q) => {
      const prior = (await q.query(`SELECT * FROM flow_brief_schedules WHERE tenant_id=$1 AND idempotency_key=$2`, [s.tenant_id, o.idempotency_key])).rows[0];
      const pv = (await q.query(`SELECT * FROM flow_brief_previews WHERE id=$1 AND tenant_id=$2 AND workspace_id=$3 FOR UPDATE`, [o.preview_id, s.tenant_id, s.workspace_id])).rows[0];
      if (!pv) throw new HttpError(404, "preview not found");
      if (prior) {
        if (pv.applied_schedule_id !== prior.id) throw new HttpError(409, "idempotency key reused with different preview");
        return { schedule: prior, replayed: true };
      }
      if (pv.applied_schedule_id) throw new HttpError(409, "preview already applied");
      if (new Date(pv.expires_at) < this.e.now()) throw new HttpError(410, "preview expired");
      const sp = json(pv.spec);
      const row = (await q.query(`INSERT INTO flow_brief_schedules (id, tenant_id, workspace_id, name, query, source_ids, cron, timezone, window_hours, skill, next_run_at, grant_actor, policy_version, approval_id, idempotency_key)
          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15) RETURNING *`,
        [newId("brf_"), s.tenant_id, s.workspace_id, sp.name, sp.query, sp.source_ids, sp.cron, sp.timezone, sp.window_hours, JSON.stringify(sp.skill),
         nextCron(sp.cron, sp.timezone, this.e.now()).toISOString(), actor, policy, o.approval_id, o.idempotency_key])).rows[0];
      await q.query(`UPDATE flow_brief_previews SET applied_schedule_id=$2 WHERE id=$1`, [pv.id, row.id]);
      return { schedule: row, replayed: false };
    });
  }

  async listBriefSchedules(s: Scope) {
    return (await this.db.query(`SELECT * FROM flow_brief_schedules WHERE tenant_id=$1 AND workspace_id=$2 ORDER BY created_at`, [s.tenant_id, s.workspace_id])).rows;
  }

  /**
   * Generates due briefs. Each window is idempotent by (tenant, schedule, scheduled_at):
   * duplicate ticks / concurrent schedulers produce one report. Missed windows collapse to the latest.
   */
  async briefTick(limit = 20, only?: { tenant_id: string; schedule_id: string }): Promise<number> {
    const now = this.e.now();
    const due = (only
      ? await this.db.query(`SELECT * FROM flow_brief_schedules WHERE enabled AND next_run_at <= $1 AND tenant_id=$3 AND id=$4 ORDER BY next_run_at LIMIT $2`, [now.toISOString(), limit, only.tenant_id, only.schedule_id])
      : await this.db.query(`SELECT * FROM flow_brief_schedules WHERE enabled AND next_run_at <= $1 ORDER BY next_run_at LIMIT $2`, [now.toISOString(), limit])).rows as any[];
    let made = 0;
    for (const b of due) {
      // Collapse missed windows: the brief covers the latest due fire time.
      let at = new Date(b.next_run_at);
      for (let n = nextCron(b.cron, b.timezone, at); n <= now; n = nextCron(b.cron, b.timezone, n)) at = n;
      const scheduledAt = at.toISOString();
      const s = { tenant_id: b.tenant_id, workspace_id: b.workspace_id };
      const key = `brief:${b.id}:${scheduledAt}`;
      const exists = (await this.db.query(`SELECT 1 FROM flow_reports WHERE tenant_id=$1 AND brief_schedule_id=$2 AND scheduled_at=$3`, [b.tenant_id, b.id, scheduledAt])).rows.length;
      if (exists) {
        await this.advanceBrief(null, b, at);
        continue;
      }
      if (this.e.authorize && !(await this.e.authorize(b, "intel.reports.write"))) {
        await this.db.query(`UPDATE flow_brief_schedules SET enabled=false, disabled_reason='grant_revoked' WHERE id=$1 AND tenant_id=$2`, [b.id, b.tenant_id]);
        continue;
      }
      // Generate outside the transaction (may call the platform Model Gateway), then
      // persist the report and advance the schedule atomically.
      const from = new Date(at.getTime() - b.window_hours * 3600_000).toISOString();
      const g = await this.gather(s, null, key, { query: b.query, source_ids: b.source_ids, from, to: scheduledAt }, `brief:${b.id}`);
      const notes = [g.coverage.truncated ? "Coverage truncated by budget." : "", g.coverage.source_failures.length ? `${g.coverage.source_failures.length} source(s) failed to collect.` : "",
        g.coverage.conflicting_evidence.length ? `${g.coverage.conflicting_evidence.length} document(s) were revised; check for conflicting statements.` : "",
        g.claims.length ? "" : "No matching material in this window."].filter(Boolean);
      let claims = g.claims;
      let body = [...g.claims.map((c: any, i: number) => `${i + 1}. ${c.text} [${c.kind}]`), ...notes].join("\n");
      let summarizedBy = "extractive";
      if (this.summarize && g.evidence_ids.length) {
        try {
          const evidence = (await this.resolveEvidence(s, g.evidence_ids)).map(({ current_revision, char_start, char_end, ...x }: any) => x);
          const out = await this.summarize({ grant_actor: b.grant_actor, workspace_id: b.workspace_id, tenant_id: b.tenant_id, key: key, topic: b.query,
            scope: { source_ids: b.source_ids, from, to: scheduledAt }, evidence });
          if (out && "pending" in out) {
            // Model task queued/running: park this window (not saved, schedule not
            // advanced); the next tick re-submits the same idempotency key.
            this.pendingSummaries.set(key, out.task_id ?? "");
            continue;
          }
          this.pendingSummaries.delete(key);
          if (out && Array.isArray(out.claims) && out.claims.length) {
            claims = out.claims;
            body = [out.body, ...notes].filter(Boolean).join("\n");
            summarizedBy = "model_gateway";
          }
        } catch (err: any) {
          if (err?.status === 403) {
            await this.db.query(`UPDATE flow_brief_schedules SET enabled=false, disabled_reason='grant_revoked' WHERE id=$1 AND tenant_id=$2`, [b.id, b.tenant_id]);
            continue;
          }
          // Gateway/budget failure: fall back to the extractive brief (noted in coverage).
          g.coverage.summarization_error = String(err?.message ?? err).slice(0, 200);
        }
      }
      g.coverage.summarized_by = summarizedBy;
      const r = await this.db.tx(async (q) => {
        const saved = await this.saveReport(s, `schedule:${b.id}`, key, {
          title: `${b.name} — ${scheduledAt.slice(0, 16).replace("T", " ")} UTC`, body, claims, coverage: g.coverage,
          skill: json(b.skill), material_scope: { source_ids: b.source_ids, from, to: scheduledAt } }, { q, brief: { schedule_id: b.id, scheduled_at: scheduledAt } });
        await this.advanceBrief(q, b, at);
        return saved;
      });
      if (r.report_id && !r.replayed) made++;
    }
    return made;
  }

  private async advanceBrief(q: Q | null, b: any, at: Date) {
    await (q ?? this.db).query(`UPDATE flow_brief_schedules SET next_run_at=$3 WHERE id=$1 AND next_run_at=$2`, [b.id, b.next_run_at, nextCron(b.cron, b.timezone, at).toISOString()]);
  }
}

/** Numeric facts in a quote: value + unit (%, 亿元, 万吨, GWh, …). Lexical, for comparison signals only. */
export function numericFacts(text: string) {
  const out: { value: number; unit: string; text: string }[] = [];
  const re = /(-?\d+(?:[.,]\d+)?)\s*(%|％|percent|亿元|万元|元|亿美元|美元|万吨|吨|GWh|MWh|kWh|GW|MW|万辆|辆|亿|万)?/gi;
  for (const m of String(text ?? "").matchAll(re)) {
    const value = Number(m[1].replace(",", ""));
    if (!Number.isFinite(value)) continue;
    const unit = (m[2] ?? "").toLowerCase().replace("％", "%").replace("percent", "%");
    if (!unit) continue;
    out.push({ value, unit, text: m[0].trim() });
    if (out.length >= 10) break;
  }
  return out;
}
