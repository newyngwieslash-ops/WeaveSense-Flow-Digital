import { test } from "node:test";
import assert from "node:assert/strict";
import { currentTenant, type Db } from "./db.ts";
import { mergeTenantMetrics, metricsSnapshot } from "./metrics-snapshot.ts";

test("metrics merge sums counts but takes max age and process uptime", () => {
  const result = mergeTenantMetrics([
    "# TYPE flow_outbox_pending gauge\nflow_outbox_pending 2\nflow_outbox_oldest_pending_seconds 8\nflow_engine_up 1\nflow_engine_uptime_seconds 20\n",
    "# TYPE flow_outbox_pending gauge\nflow_outbox_pending 3\nflow_outbox_oldest_pending_seconds 4\nflow_engine_up 1\nflow_engine_uptime_seconds 21\n",
  ]);
  assert.match(result, /^flow_outbox_pending 5$/m);
  assert.match(result, /^flow_outbox_oldest_pending_seconds 8$/m);
  assert.match(result, /^flow_engine_up 1$/m);
  assert.match(result, /^flow_engine_uptime_seconds 21$/m);
  for (const bad of ["arbitrary 1", "flow_engine_up NaN", "flow_engine_up Infinity", "flow_engine_up 9999999999999999999"]) {
    assert.throws(() => mergeTenantMetrics([bad]));
  }
});

test("metrics snapshot is atomic, scoped, unavailable before success or after error and expires", async () => {
  let clock = 1000;
  let fail = false;
  let slow = false;
  const tenants: string[] = [];
  const db: Db = {
    async query(sql) {
      tenants.push(currentTenant());
      if (fail) throw new Error("fixture failure");
      if (slow) clock += 31_000;
      return { rows: (sql.includes("GROUP BY") ? [] : [{ n: 1, age: 2 }]) as any };
    },
    async tx(fn) { return fn(db); }, async close() {},
  };
  let scans = 0;
  const snapshot = metricsSnapshot(db, { async scan() { scans++; return ["a", "b"]; }, async close() {} }, () => clock);
  assert.throws(snapshot.read, (e: any) => e.status === 503);
  await snapshot.refresh();
  assert.match(snapshot.read(), /^flow_outbox_pending 2$/m);
  assert.ok(tenants.includes("a") && tenants.includes("b") && !tenants.includes(""));
  snapshot.read(); snapshot.read();
  assert.equal(scans, 1, "HTTP reads must not dispatch system scans");
  clock += 60_001;
  assert.throws(snapshot.read, (e: any) => e.status === 503);
  await snapshot.refresh();
  fail = true;
  await assert.rejects(snapshot.refresh());
  assert.throws(snapshot.read, (e: any) => e.status === 503);
  fail = false; slow = true;
  await assert.rejects(snapshot.refresh(), /deadline/);
  assert.throws(snapshot.read, (e: any) => e.status === 503);
});
