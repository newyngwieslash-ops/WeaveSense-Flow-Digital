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
    [{ value: (await q(`SELECT count(*)::int n FROM flow_collection_runs WHERE status='running' AND lease_expires_at < now()`))[0]?.n ?? 0 }]);
  const ob = (await q(`SELECT count(*)::int n, coalesce(extract(epoch FROM now() - min(occurred_at)), 0)::float8 age FROM flow_outbox WHERE delivered_at IS NULL`))[0] ?? {};
  gauge("flow_outbox_pending", "Outbox events not yet delivered to the platform.", [{ value: ob.n ?? 0 }]);
  gauge("flow_outbox_oldest_pending_seconds", "Age of the oldest undelivered outbox event.", [{ value: Math.round(ob.age ?? 0) }]);
  gauge("flow_sources", "Sources by kind and status.", (await q(`SELECT kind, status, count(*)::int n FROM flow_sources GROUP BY kind, status`)).map((r) => ({ labels: { kind: r.kind, status: r.status }, value: r.n })));
  gauge("flow_media_failed", "Telegram attachments currently failed or timed out (retry pending or exhausted).",
    [{ value: (await q(`SELECT count(*)::int n FROM flow_documents d, jsonb_array_elements(d.media) m WHERE m->>'status' IN ('failed','timeout')`))[0]?.n ?? 0 }]);
  gauge("flow_ocr_jobs", "OCR jobs by status.", (await q(`SELECT status, count(*)::int n FROM flow_media_ocr GROUP BY status`)).map((r) => ({ labels: { status: r.status }, value: r.n })));
  gauge("flow_research_runs", "Research runs by status.", (await q(`SELECT status, count(*)::int n FROM flow_research_runs GROUP BY status`)).map((r) => ({ labels: { status: r.status }, value: r.n })));
  return out.join("\n") + "\n";
}
