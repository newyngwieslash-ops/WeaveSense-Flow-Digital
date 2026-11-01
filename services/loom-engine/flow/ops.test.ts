// §14 monitoring: /metrics exposes aggregate gauges without auth tokens or tenant data.
import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine } from "./engine.ts";
import { createApiServer } from "./server.ts";
import { metricsText } from "./metrics.ts";

test("metrics endpoint serves Prometheus gauges and honours METRICS_TOKEN", async () => {
  const db = await pgliteDb(); await migrate(db);
  await db.query(`INSERT INTO flow_outbox (event_id, tenant_id, workspace_id, operation_id, job_id, type, payload) VALUES ('e1','tn_secret','ws','op','j','x','{}')`);
  const cfg = { signingKey: "k".repeat(32), audience: "loom-engine", metricsToken: "", metrics: () => metricsText(db) };
  const srv = createApiServer(new Engine(db), cfg).listen(0);
  const port = (srv.address() as any).port;
  try {
    let r = await fetch(`http://127.0.0.1:${port}/metrics`);
    assert.equal(r.status, 503, "no public all-tenant aggregate when ops token is absent");
    cfg.metricsToken = "fixture-scrape-token";
    r = await fetch(`http://127.0.0.1:${port}/metrics`);
    assert.equal(r.status, 401);
    r = await fetch(`http://127.0.0.1:${port}/metrics`, { headers: { authorization: "Bearer wrong" } });
    assert.equal(r.status, 401);
    r = await fetch(`http://127.0.0.1:${port}/metrics`, { headers: { authorization: "Bearer fixture-scrape-token" } });
    assert.equal(r.status, 200);
    const body = await r.text();
    assert.match(body, /^flow_outbox_pending 1$/m);
    assert.match(body, /# TYPE flow_collection_runs gauge/);
    assert.ok(!body.includes("tn_secret"), "no tenant ids in metrics");
    assert.equal(r.headers.get("Cache-Control"), "no-store");
  } finally { srv.close(); await db.close(); }
});
