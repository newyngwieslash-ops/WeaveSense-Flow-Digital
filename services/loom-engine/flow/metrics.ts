// Prometheus text exposition for the Loom Engine (§14 monitoring). System context
// (no tenant set), aggregate counts only: no tenant ids, titles or content leave here.
import type { Db } from "./db.ts";

const started = Date.now();
const esc = (v: string) => v.replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\n/g, "\\n");

export async function metricsText(db: Db): Promise<string> {
  const out: string[] = [];
  const gauge = (name: string, help: string, rows: { labels?: Record<string, string>; value: number }[]) => {
    out.push(`# HELP ${name} ${help}`, `# TYPE ${name} gauge`);
    for (const r of rows) {
      const l = r.labels && Object.keys(r.labels).length ? `{${Object.entries(r.labels).map(([k, v]) => `${k}="${esc(v)}"`).join(",")}}` : "";
      out.push(`${name}${l} ${Number(r.value) || 0}`);
    }
  };
  const q = async (sql: string) => (await db.query(sql)).rows as any[];
  gauge("flow_engine_up", "Engine process is serving.", [{ value: 1 }]);
  gauge("flow_engine_uptime_seconds", "Seconds since process start.", [{ value: Math.round((Date.now() - started) / 1000) }]);
  gauge("flow_collection_runs", "Collection runs by status.", (await q(`SELECT status, count(*)::int n FROM flow_collection_runs GROUP BY status`)).map((r) => ({ labels: { status: r.status }, value: r.n })));
  gauge("flow_collection_runs_lease_expired", "Running collection runs whose lease expired (stuck worker).",
    [{ value: (await q(`SELECT count(*)::int n FROM flow_collection_runs WHERE status IN ('leased','running') AND lease_expires_at < now()`))[0]?.n ?? 0 }]);
  const ob = (await q(`SELECT count(*)::int n, coalesce(extract(epoch FROM now() - min(occurred_at)), 0)::float8 age FROM flow_outbox WHERE delivered_at IS NULL`))[0] ?? {};
  gauge("flow_outbox_pending", "Outbox events not yet delivered to the platform.", [{ value: ob.n ?? 0 }]);
  gauge("flow_outbox_oldest_pending_seconds", "Age of the oldest undelivered outbox event.", [{ value: Math.round(ob.age ?? 0) }]);
  gauge("flow_sources", "Sources by kind and status.", (await q(`SELECT kind, status, count(*)::int n FROM flow_sources GROUP BY kind, status`)).map((r) => ({ labels: { kind: r.kind, status: r.status }, value: r.n })));
  gauge("flow_media_failed", "Telegram attachments currently failed or timed out (retry pending or exhausted).",
    [{ value: (await q(`SELECT count(*)::int n FROM flow_documents d, jsonb_array_elements(d.media) m WHERE m->>'status' IN ('failed','timeout')`))[0]?.n ?? 0 }]);
  gauge("flow_ocr_jobs", "OCR jobs by status.", (await q(`SELECT status, count(*)::int n FROM flow_media_ocr GROUP BY status`)).map((r) => ({ labels: { status: r.status }, value: r.n })));
  gauge("flow_research_runs", "Research runs by status.", (await q(`SELECT status, count(*)::int n FROM flow_research_runs GROUP BY status`)).map((r) => ({ labels: { status: r.status }, value: r.n })));
  // §14: oldest queued job, redelivered events, blocked schedules (budget/grant), Reviewer rejections.
  const oq = (await q(`SELECT coalesce(extract(epoch FROM now() - min(created_at)), 0)::float8 age FROM flow_collection_runs WHERE status IN ('queued','retry_wait')`))[0] ?? {};
  gauge("flow_collection_oldest_queued_seconds", "Age of the oldest queued/retrying collection run.", [{ value: Math.round(oq.age ?? 0) }]);
  const rq = (await q(`SELECT coalesce(extract(epoch FROM now() - min(created_at)), 0)::float8 age FROM flow_research_runs WHERE status = 'queued'`))[0] ?? {};
  gauge("flow_research_oldest_queued_seconds", "Age of the oldest queued research run.", [{ value: Math.round(rq.age ?? 0) }]);
  gauge("flow_outbox_redelivery_attempts", "Outbox events delivered more than once (attempts > 1): duplicate deliveries the platform inbox must dedupe.",
    [{ value: (await q(`SELECT count(*)::int n FROM flow_outbox WHERE attempts > 1`))[0]?.n ?? 0 }]);
  gauge("flow_schedules", "Collection schedules by state (blocked = budget denied or grant revoked).",
    (await q(`SELECT state, coalesce(blocked_reason, '') reason, count(*)::int n FROM flow_schedules GROUP BY state, coalesce(blocked_reason, '')`))
      .map((r) => ({ labels: { state: r.state, reason: String(r.reason).split(":")[0].slice(0, 40) }, value: r.n })));
  gauge("flow_review_rejected_claims", "Claims rejected by the host Reviewer / citation checks in research outputs.",
    [{ value: (await q(`SELECT coalesce(sum(jsonb_array_length(output->'review'->'rejected')), 0)::int n FROM flow_research_runs WHERE jsonb_typeof(output->'review'->'rejected') = 'array'`))[0]?.n ?? 0 }]);
  return out.join("\n") + "\n";
}
