// §14 monitoring: metrics behind the oldest-queued, duplicate-delivery, budget
// (blocked schedules) and citation-review alerts are emitted by the engine.
import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { metricsText } from "./metrics.ts";

test("engine metrics expose §14 alert signals", async () => {
  const db = await pgliteDb(); await migrate(db);
  await db.query(`INSERT INTO flow_outbox (event_id, tenant_id, workspace_id, operation_id, job_id, type, payload, attempts) VALUES ('e1','t','w','o','j','x','{}',3)`);
  await db.query(`INSERT INTO flow_research_runs (id, tenant_id, workspace_id, parent_run_id, kind, question, status, idempotency_key, created_at)
      VALUES ('rq','t','w','p','research','q','queued','k1', now() - interval '1 hour')`);
  await db.query(`INSERT INTO flow_research_runs (id, tenant_id, workspace_id, parent_run_id, kind, question, status, idempotency_key, output)
      VALUES ('rs','t','w','p','research','q','succeeded','k2', '{"review":{"rejected":[{"idx":0},{"idx":2}]}}')`);
  const m = await metricsText(db);
  assert.match(m, /^flow_outbox_redelivery_attempts 1$/m);
  assert.match(m, /^flow_research_oldest_queued_seconds 3[56]\d\d$/m);
  assert.match(m, /^flow_collection_oldest_queued_seconds 0$/m);
  assert.match(m, /^flow_review_rejected_claims 2$/m);
  assert.match(m, /# TYPE flow_schedules gauge/);
});
