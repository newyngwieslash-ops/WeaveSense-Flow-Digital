// Loom Engine (WeaveSense-Flow) entrypoint. LOOM_ROLE=api|scheduler|all.
//  api:       internal HTTP API + collection worker + outbox dispatcher
//  scheduler: the single due-window scanner (guarded by a PG advisory lock)
import { pgDb, migrate } from "./db.ts";
import { Engine } from "./engine.ts";
import { createApiServer } from "./server.ts";
import { sign } from "./auth.ts";
import { hostname } from "node:os";

const env = (k: string, d?: string) => {
  const v = process.env[k]?.trim();
  if (v) return v;
  if (d !== undefined) return d;
  throw new Error(`missing env ${k}`);
};
const role = env("LOOM_ROLE", "api");
const db = await pgDb(env("DATABASE_URL"));
if (env("LOOM_AUTO_MIGRATE", "true") === "true") console.log("[loom] migrations applied:", await migrate(db));
const engine = new Engine(db);
const key = env("FLOW_INTERNAL_JWT_SIGNING_KEY");
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
let stopping = false;
process.on("SIGTERM", () => { stopping = true; });

async function loop(name: string, ms: number, fn: () => Promise<unknown>) {
  while (!stopping) {
    try { await fn(); } catch (e) { console.error(`[loom:${name}]`, e); }
    await sleep(ms);
  }
}

if (role === "api" || role === "all") {
  const port = Number(env("LOOM_PORT", "8080"));
  createApiServer(engine, { signingKey: key, audience: env("FLOW_INTERNAL_JWT_AUDIENCE", "loom-engine") }).listen(port, () => console.log(`[loom] api on :${port}`));
  const owner = `${hostname()}:${process.pid}`;
  void loop("worker", 1000, async () => { while (!stopping && (await engine.workOnce(owner))); });
  const platform = env("FLOW_PLATFORM_EVENTS_URL", "");
  if (platform) {
    const iss = env("FLOW_INTERNAL_JWT_ISSUER", "flow-platform");
    void loop("outbox", 2000, () => engine.dispatchOutbox(async (ev) => {
      const tok = sign(key, { iss: "loom-engine", aud: iss, tenant_id: ev.tenant_id, workspace_id: ev.workspace_id,
        actor: { type: "service_principal", id: "loom-engine" }, scopes: ["flow.events.write"], policy_version: "policy_v1" });
      const r = await fetch(platform, { method: "POST", headers: { authorization: `Bearer ${tok}`, "content-type": "application/json" }, body: JSON.stringify(ev) });
      if (!r.ok) throw new Error(`platform ${r.status}`);
    }));
  }
}
if (role === "scheduler" || role === "all") {
  // schedulerTick takes a transaction-scoped advisory lock: one active scanner only.
  void loop("scheduler", Number(env("LOOM_SCHEDULER_TICK_MS", "15000")), () => engine.schedulerTick());
}
