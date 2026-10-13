import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, type FeedItem } from "./engine.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const B = { tenant_id: "tn_b", workspace_id: "ws_b" };
let feed: FeedItem[] = [];
async function setup() {
  const db = await pgliteDb(); await migrate(db);
  const eng = new Engine(db, async () => feed, () => new Date("2026-10-13T00:00:00Z"));
  const sa = await eng.createSource(A, { url: "https://e.com/f" });
  const sb = await eng.createSource(B, { url: "https://e.com/f" });
  return { db, eng, sa, sb };
}
const long = "Lithium supply agreement signed. ".repeat(100) + "\nSecond part about battery policy subsidies.";

test("FTS search with source/time filter, tenant isolated, chunked read with coverage", async () => {
  const { eng, sa, sb } = await setup();
  feed = [{ external_id: "1", title: "Acme lithium deal", body: long, url: "u1", published_at: "2026-10-12T00:00:00Z" },
          { external_id: "2", title: "Other news", body: "weather today", url: "u2", published_at: "2026-09-01T00:00:00Z" }];
  for (const [s, src] of [[A, sa], [B, sb]] as const) { await eng.requestCollection(s, "k", { source_ids: [src.id] }); await eng.workOnce("w"); }
  const hit = await eng.searchDocuments(A, { query: "lithium" });
  assert.ok(hit.items.length >= 1);
  assert.ok(hit.items.every((x: any) => x.source_id === sa.id));
  assert.equal((await eng.searchDocuments(A, { query: "lithium", from: "2026-10-13T00:00:00Z" })).items.length, 0);
  assert.equal((await eng.searchDocuments(A, { query: "lithium", source_ids: [sb.id] })).items.length, 0);
  const docId = hit.items[0].document_id;
  const r1 = await eng.readDocument(A, { document_id: docId, max_chars: 1000 });
  assert.equal(r1.coverage.truncated, true); assert.equal(r1.coverage.next_offset, 1000);
  const r2 = await eng.readDocument(A, { document_id: docId, offset: r1.coverage.next_offset!, max_chars: 20000 });
  assert.equal(r2.coverage.truncated, false);
  await assert.rejects(eng.readDocument(B, { document_id: docId }), (e: any) => e.status === 404);
});

test("monitor preview → apply (approved) creates monitor+schedule; replay idempotent; cross-tenant rejected", async () => {
  const { eng, db, sa, sb } = await setup();
  await assert.rejects(eng.previewMonitor(A, { keywords: ["acme"], source_ids: [sb.id], interval_seconds: 3600 }), (e: any) => e.status === 404);
  const pv = await eng.previewMonitor(A, { keywords: ["acme"], source_ids: [sa.id], interval_seconds: 3600, timezone: "Asia/Shanghai" });
  assert.equal(pv.runs_per_day, 24);
  await assert.rejects(eng.applyMonitor(B, "u", "p", { preview_id: pv.preview_id, approval_id: "ap1", idempotency_key: "i1" }), (e: any) => e.status === 404);
  const a1 = await eng.applyMonitor(A, "user:u", "policy_v1", { preview_id: pv.preview_id, approval_id: "ap1", idempotency_key: "i1" });
  const a2 = await eng.applyMonitor(A, "user:u", "policy_v1", { preview_id: pv.preview_id, approval_id: "ap1", idempotency_key: "i1" });
  assert.equal(a2.replayed, true); assert.equal(a2.monitor.id, a1.monitor.id);
  const mons = await eng.listMonitors(A);
  assert.equal(mons.length, 1); assert.equal(mons[0].timezone, "Asia/Shanghai");
  assert.equal((await eng.listMonitors(B)).length, 0);
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_schedules WHERE id=$1`, [a1.monitor.schedule_id])).rows[0].n, 1);
  await assert.rejects(eng.applyMonitor(A, "u", "p", { preview_id: pv.preview_id, approval_id: "ap1", idempotency_key: "i2" }), (e: any) => e.status === 409);
});

test("cancel operation: queued job cancelled, not executed; foreign tenant 404", async () => {
  const { eng, sa } = await setup();
  const op = await eng.requestCollection(A, "k", { source_ids: [sa.id] });
  await assert.rejects(eng.cancelOperation(B, op.operation_id), (e: any) => e.status === 404);
  assert.equal((await eng.cancelOperation(A, op.operation_id)).cancelled, 1);
  assert.equal(await eng.workOnce("w"), null);
  assert.equal((await eng.getOperation(A, op.operation_id)).status, "cancelled");
});
