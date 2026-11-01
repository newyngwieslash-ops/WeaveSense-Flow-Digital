import { withTenant, type Db } from "./db.ts";
import { HttpError } from "./errors.ts";
import { metricsText } from "./metrics.ts";
import type { TenantScanner } from "./tenant-dispatch.ts";

/** Merge only the engine's fixed gauge protocol; content/tenant IDs are never labels. */
export function mergeTenantMetrics(texts: string[]): string {
  const metadata = new Set<string>();
  const values = new Map<string, number>();
  const allowed = new Set([
    "flow_engine_up", "flow_engine_uptime_seconds", "flow_collection_runs", "flow_collection_runs_lease_expired",
    "flow_outbox_pending", "flow_outbox_oldest_pending_seconds", "flow_sources", "flow_media_failed", "flow_ocr_jobs",
    "flow_research_runs", "flow_collection_oldest_queued_seconds", "flow_research_oldest_queued_seconds",
    "flow_outbox_redelivery_attempts", "flow_schedules", "flow_review_rejected_claims",
  ]);
  for (const text of texts) for (const line of text.trimEnd().split("\n")) {
    if (line.startsWith("# HELP ") || line.startsWith("# TYPE ")) { metadata.add(line); continue; }
    const match = /^(flow_[a-z_]+)(\{[^\r\n]*\})? ([0-9]+(?:\.[0-9]+)?)$/.exec(line);
    if (!match || !allowed.has(match[1])) throw new Error("invalid metrics projection");
    const value = Number(match[3]);
    if (!Number.isSafeInteger(Math.trunc(value))) throw new Error("metrics bound exceeded");
    const key = match[1] + (match[2] ?? "");
    const previous = values.get(key) ?? 0;
    // Ages are max, not sum; process uptime/up are also shared across tenants.
    const total = match[1].endsWith("_seconds") || match[1] === "flow_engine_up" ? Math.max(previous, value) : previous + value;
    if (!Number.isSafeInteger(Math.trunc(total))) throw new Error("metrics bound exceeded");
    values.set(key, total);
  }
  if (!texts.length) { metadata.add("# TYPE flow_engine_up gauge"); values.set("flow_engine_up", 1); }
  return [...metadata, ...[...values].map(([key, value]) => `${key} ${value}`)].join("\n") + "\n";
}

/** Main's private background sampler. HTTP only receives read(), never scanner/refresh.
 * Incomplete/failed/old samples are unavailable, not zero or indefinitely fresh.
 */
export function metricsSnapshot(db: Db, scanner: TenantScanner, now = Date.now) {
  let snapshot: { text: string; completed: number } | undefined;
  let running = false;
  return {
    async refresh() {
      if (running) throw new Error("metrics sample already running");
      running = true;
      const started = now();
      try {
        let cursor = "";
        const texts: string[] = [];
        for (let page = 0; page < 1000; page++) {
          const tenants = await scanner.scan(cursor, 100);
          for (const tenant of tenants) {
            if (now() - started > 30_000) throw new Error("metrics sample deadline exceeded");
            texts.push(await withTenant(tenant, () => metricsText(db)));
          }
          if (now() - started > 30_000) throw new Error("metrics sample deadline exceeded");
          if (tenants.length < 100) {
            snapshot = { text: mergeTenantMetrics(texts), completed: now() };
            return;
          }
          cursor = tenants.at(-1)!;
        }
        throw new Error("metrics sample page bound exceeded");
      } catch (error) { snapshot = undefined; throw error; }
      finally { running = false; }
    },
    read() {
      if (!snapshot || now() - snapshot.completed > 60_000) throw new HttpError(503, "metrics snapshot unavailable");
      return snapshot.text + `# TYPE flow_metrics_snapshot_age_seconds gauge\nflow_metrics_snapshot_age_seconds ${Math.max(0, Math.floor((now() - snapshot.completed) / 1000))}\n`;
    },
  };
}
