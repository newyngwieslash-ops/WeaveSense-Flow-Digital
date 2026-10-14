// Internal HTTP API for the Go platform. Every /v1 call needs a verified execution context.
import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import { verify, requireScope, AuthError, type ExecutionContext } from "./auth.ts";
import { Engine, HttpError } from "./engine.ts";
import { Reports } from "./reports.ts";
import { withTenant } from "./db.ts";

export interface ServerConfig {
  signingKey: string;
  audience: string;
  /** Platform Model Gateway hook so a pushed brief wake-up can complete the window here. */
  summarize?: Reports["summarize"];
}

async function readJson(req: IncomingMessage): Promise<any> {
  let size = 0; const chunks: Buffer[] = [];
  for await (const c of req) { size += c.length; if (size > 1 << 20) throw new HttpError(413, "body too large"); chunks.push(c); }
  if (!chunks.length) return {};
  try { return JSON.parse(Buffer.concat(chunks).toString("utf8")); } catch { throw new HttpError(400, "invalid json"); }
}

function send(res: ServerResponse, status: number, data: unknown, msg = "ok") {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify({ code: status >= 400 ? status : 0, data, msg }));
}

export function handler(engine: Engine, cfg: ServerConfig) {
  const rp = new Reports(engine);
  rp.summarize = cfg.summarize;
  return async (req: IncomingMessage, res: ServerResponse) => {
    try {
      const url = new URL(req.url ?? "/", "http://engine");
      if (url.pathname === "/healthz") return send(res, 200, { ok: true });
      const auth = req.headers.authorization ?? "";
      const ctx: ExecutionContext = verify(cfg.signingKey, auth.replace(/^Bearer /, ""), cfg.audience);
      // Postgres RLS second layer: every query of this request runs with app.tenant_id = token tenant.
      return await withTenant(ctx.tenant_id, () => route(req, res, url, ctx));
    } catch (e: any) {
      if (e instanceof AuthError || e instanceof HttpError) return send(res, e.status, null, e.message);
      console.error("[loom-engine]", e);
      return send(res, 500, null, "internal error");
    }
  };
  async function route(req: IncomingMessage, res: ServerResponse, url: URL, ctx: ExecutionContext) {
    {
      const p = url.pathname, m = req.method;
      let mm: RegExpMatchArray | null;
      if (m === "GET" && p === "/v1/sources") { requireScope(ctx, "intel.sources.read"); return send(res, 200, await engine.listSources(ctx)); }
      if (m === "POST" && p === "/v1/sources") { requireScope(ctx, "intel.sources.write"); return send(res, 201, await engine.createSource(ctx, await readJson(req))); }
      if (m === "GET" && p === "/v1/schedules") { requireScope(ctx, "intel.sources.read"); return send(res, 200, await engine.listSchedules(ctx)); }
      if (m === "POST" && p === "/v1/schedules") {
        requireScope(ctx, "intel.schedules.write");
        return send(res, 201, await engine.createSchedule(ctx, `${ctx.actor.type}:${ctx.actor.id}`, ctx.policy_version, await readJson(req)));
      }
      if (m === "POST" && p === "/v1/collection-runs") {
        requireScope(ctx, "intel.collect");
        const r = await engine.requestCollection(ctx, String(req.headers["x-idempotency-key"] ?? ""), await readJson(req));
        return send(res, 202, r, "accepted");
      }
      if (m === "GET" && p === "/v1/documents") {
        requireScope(ctx, "intel.documents.read");
        return send(res, 200, await engine.listDocuments(ctx, { q: url.searchParams.get("q") ?? undefined, limit: Number(url.searchParams.get("limit")), source_id: url.searchParams.get("source_id") ?? undefined }));
      }
      if (m === "GET" && (mm = p.match(/^\/v1\/documents\/([\w-]+)\/revisions\/(\d+)$/))) {
        requireScope(ctx, "intel.documents.read"); return send(res, 200, await engine.getRevision(ctx, mm[1], Number(mm[2])));
      }
      if (m === "GET" && (mm = p.match(/^\/v1\/operations\/([\w-]+)$/))) {
        requireScope(ctx, "intel.operations.read"); return send(res, 200, await engine.getOperation(ctx, mm[1]));
      }
      if (m === "POST" && p === "/v1/documents/search") { requireScope(ctx, "intel.documents.read"); return send(res, 200, await engine.searchDocuments(ctx, await readJson(req))); }
      if (m === "POST" && p === "/v1/documents/read") { requireScope(ctx, "intel.documents.read"); return send(res, 200, await engine.readDocument(ctx, await readJson(req))); }
      if (m === "POST" && p === "/v1/evidence") { requireScope(ctx, "intel.documents.read"); return send(res, 201, await engine.createEvidence(ctx, String(req.headers["x-idempotency-key"] ?? ""), await readJson(req))); }
      if (m === "GET" && p === "/v1/evidence") { requireScope(ctx, "intel.documents.read"); return send(res, 200, await engine.listEvidence(ctx, url.searchParams.get("document_id") ?? undefined)); }
      if (m === "GET" && p === "/v1/monitors") { requireScope(ctx, "intel.sources.read"); return send(res, 200, await engine.listMonitors(ctx)); }
      if (m === "POST" && p === "/v1/monitors/preview") { requireScope(ctx, "intel.sources.read"); return send(res, 200, await engine.previewMonitor(ctx, await readJson(req))); }
      if (m === "POST" && p === "/v1/monitors") {
        requireScope(ctx, "intel.monitors.write");
        const b = await readJson(req);
        return send(res, 201, await engine.applyMonitor(ctx, `${ctx.actor.type}:${ctx.actor.id}`, ctx.policy_version,
          { ...b, idempotency_key: String(req.headers["x-idempotency-key"] ?? b.idempotency_key ?? "") }));
      }
      if (m === "POST" && (mm = p.match(/^\/v1\/operations\/([\w-]+)\/cancel$/))) { requireScope(ctx, "intel.collect"); return send(res, 200, await engine.cancelOperation(ctx, mm[1])); }
      const idem = () => String(req.headers["x-idempotency-key"] ?? "");
      const actor = `${ctx.actor.type}:${ctx.actor.id}`;
      if (m === "POST" && p === "/v1/evidence/resolve") { requireScope(ctx, "intel.documents.read"); return send(res, 200, await rp.resolveEvidence(ctx, (await readJson(req)).evidence_ids ?? [])); }
      if (m === "GET" && p === "/v1/reports") { requireScope(ctx, "intel.documents.read"); return send(res, 200, await rp.listReports(ctx)); }
      if (m === "GET" && (mm = p.match(/^\/v1\/reports\/([\w-]+)$/))) { requireScope(ctx, "intel.documents.read"); return send(res, 200, await rp.getReport(ctx, mm[1])); }
      if (m === "POST" && p === "/v1/reports") { requireScope(ctx, "intel.reports.write"); return send(res, 201, await rp.saveReport(ctx, actor, idem(), await readJson(req))); }
      if (m === "POST" && p === "/v1/research-runs") { requireScope(ctx, "intel.research"); return send(res, 201, await rp.startResearch(ctx, idem(), await readJson(req))); }
      if (m === "POST" && p === "/v1/briefs/wake") {
        // Push from the platform: the model task of a parked brief window finished.
        // Completes just that schedule's due window now (idempotent with the scheduler tick).
        requireScope(ctx, "intel.reports.write");
        const key = String((await readJson(req)).key ?? "");
        const scheduleId = /^brief:([\w-]+):/.exec(key)?.[1];
        if (!scheduleId) throw new HttpError(400, "invalid brief key");
        return send(res, 200, { key, made: await rp.briefTick(5, { tenant_id: ctx.tenant_id, schedule_id: scheduleId }) });
      }
      if (m === "GET" && p === "/v1/brief-schedules") { requireScope(ctx, "intel.sources.read"); return send(res, 200, await rp.listBriefSchedules(ctx)); }
      if (m === "POST" && p === "/v1/brief-schedules/preview") { requireScope(ctx, "intel.sources.read"); return send(res, 200, await rp.previewBrief(ctx, await readJson(req))); }
      if (m === "POST" && p === "/v1/brief-schedules") {
        requireScope(ctx, "intel.schedules.write");
        const b = await readJson(req);
        return send(res, 201, await rp.applyBrief(ctx, actor, ctx.policy_version, { ...b, idempotency_key: idem() || String(b.idempotency_key ?? "") }));
      }
      if (m === "POST" && (mm = p.match(/^\/v1\/sources\/([\w-]+)\/revoke$/))) { requireScope(ctx, "intel.sources.write"); return send(res, 200, await engine.revokeSource(ctx, mm[1], String((await readJson(req)).reason ?? "revoked"))); }
      if (m === "GET" && p === "/v1/connector-samples") { requireScope(ctx, "intel.sources.read"); return send(res, 200, await engine.listConnectorSamples(ctx)); }
      if (m === "POST" && p === "/v1/documents/extract") { requireScope(ctx, "intel.collect"); return send(res, 200, await engine.extractDocument(ctx, await readJson(req))); }
      if (m === "POST" && p === "/v1/documents/transcript") { requireScope(ctx, "intel.collect"); return send(res, 200, await engine.transcribeDocument(ctx, await readJson(req))); }
      return send(res, 404, null, "not found");
    }
  }
}

export const createApiServer = (engine: Engine, cfg: ServerConfig) => createServer(handler(engine, cfg));
