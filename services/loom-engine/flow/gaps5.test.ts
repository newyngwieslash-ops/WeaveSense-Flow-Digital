// PRD §6.1/§6.2: collection job state machine (leased, running, retry_wait, partial,
// reconciliation_required, cancel_requested) and schedule misfire_policy + cron.
import { test } from "node:test";
import assert from "node:assert/strict";
import { pgliteDb, migrate } from "./db.ts";
import { Engine, operationStatus, prdJobState, JOB_STATES, type FetchResult } from "./engine.ts";

const A = { tenant_id: "tn_a", workspace_id: "ws_a" };
const item = (id: string) => ({ external_id: id, title: "t" + id, body: "b" + id, url: "u" + id, published_at: "2026-10-12T00:00:00Z" });

async function setup(fetch: () => Promise<FetchResult> = async () => [item("1")]) {
  const db = await pgliteDb();
  await migrate(db);
  const clock = { now: new Date("2026-10-13T00:00:00Z") };
  const eng = new Engine(db, fetch as any, () => clock.now);
  const src = await eng.createSource(A, { url: "https://example.com/feed.xml" });
  return { db, eng, clock, src };
}
const status = async (db: any, id: string) => (await db.query(`SELECT status FROM flow_collection_runs WHERE id=$1`, [id])).rows[0].status;
const docs = async (db: any) => (await db.query(`SELECT count(*)::int n FROM flow_documents`)).rows[0].n;

test("schema accepts exactly the PRD job states", async () => {
  const { db, eng, src } = await setup();
  const op = await eng.requestCollection(A, "k", { source_ids: [src.id] });
  for (const st of JOB_STATES) await db.query(`UPDATE flow_collection_runs SET status=$2 WHERE operation_id=$1`, [op.operation_id, st]);
  await assert.rejects(db.query(`UPDATE flow_collection_runs SET status='done' WHERE operation_id=$1`, [op.operation_id]));
});

test("queued → leased (claim) → running (heartbeat) → succeeded, fenced", async () => {
  const { db, eng, src } = await setup();
  await eng.requestCollection(A, "k", { source_ids: [src.id] });
  const run = await eng.claim("w1");
  assert.equal(run.status, "leased");
  assert.equal(await eng.heartbeat(run), "ok");
  assert.equal(await status(db, run.id), "running");
  assert.equal(await eng.heartbeat({ ...run, fencing_token: Number(run.fencing_token) - 1 }), "stale", "old token is rejected");
  assert.equal((await eng.execute(run)).status, "succeeded");
  assert.equal(await eng.heartbeat(run), "stale", "no heartbeats after completion");
});

test("cancel: queued → cancelled at once; running → cancel_requested → cancelled by the worker, nothing committed", async () => {
  let release!: () => void;
  const gate = new Promise<void>((r) => (release = r));
  const { db, eng, src } = await setup(async () => { await gate; return [item("1")]; });
  const q1 = await eng.requestCollection(A, "q1", { source_ids: [src.id] });
  const c1 = await eng.cancelOperation(A, q1.operation_id);
  assert.deepEqual([c1.cancelled, c1.cancel_requested], [1, 0]);
  assert.equal(await status(db, q1.job_ids[0]), "cancelled");
  assert.equal(await eng.claim("w"), null, "a cancelled job is never claimed");

  const q2 = await eng.requestCollection(A, "q2", { source_ids: [src.id] });
  const run = await eng.claim("w");
  const exec = eng.execute(run); // blocks inside the fetch
  await new Promise((r) => setTimeout(r, 20));
  const c2 = await eng.cancelOperation(A, q2.operation_id);
  assert.equal(c2.cancel_requested, 1);
  assert.equal(await status(db, run.id), "cancel_requested");
  assert.equal((await eng.getOperation(A, q2.operation_id)).status, "cancel_requested");
  release();
  assert.equal((await exec).status, "cancelled");
  assert.equal(await status(db, run.id), "cancelled");
  assert.equal(await docs(db), 0, "the cancelled worker's batch never commits");
  assert.equal((await db.query(`SELECT count(*)::int n FROM flow_source_cursors`)).rows[0].n, 0);
});

test("cancel_requested whose worker died is finalized to cancelled after the lease", async () => {
  const { db, eng, clock, src } = await setup();
  const op = await eng.requestCollection(A, "k", { source_ids: [src.id] });
  const run = await eng.claim("w", 60_000);
  await eng.cancelOperation(A, op.operation_id);
  assert.equal(await status(db, run.id), "cancel_requested");
  clock.now = new Date(clock.now.getTime() + 61_000);
  assert.equal(await eng.claim("w2"), null);
  assert.equal(await status(db, run.id), "cancelled");
});

test("unknown commit outcome → reconciliation_required → reconcile re-queues (no commit) or settles succeeded", async () => {
  const { db, eng, src } = await setup();
  await eng.requestCollection(A, "k", { source_ids: [src.id] });
  const run = await eng.claim("w");
  const r = await eng.execute(run, { beforeCommit: () => { throw new Error("Connection terminated unexpectedly"); } });
  assert.equal(r.status, "reconciliation_required");
  assert.equal(await status(db, run.id), "reconciliation_required");
  assert.equal(await eng.claim("w"), null, "not re-run before it is reconciled");
  assert.deepEqual(await eng.reconcile(), [{ id: run.id, status: "queued" }]);
  const again = await eng.claim("w");
  assert.equal(again.id, run.id);
  assert.equal((await eng.execute(again)).status, "succeeded");
  assert.equal(await docs(db), 1);
  // Commit landed but the reply was lost: the outbox proves it → succeeded, never re-run.
  await db.query(`UPDATE flow_collection_runs SET status='reconciliation_required' WHERE id=$1`, [run.id]);
  assert.deepEqual(await eng.reconcile(), [{ id: run.id, status: "succeeded" }]);
});

test("partial: rejected entries / unstored media mark the job partial but still commit", async () => {
  const { db, eng, src } = await setup(async () => ({ items: [item("1")], collector: "rss", mode: "live", rejected_entries: 2 }) as any);
  const op = await eng.requestCollection(A, "k", { source_ids: [src.id] });
  assert.equal((await eng.workOnce("w"))!.status, "partial");
  const job = (await eng.getOperation(A, op.operation_id)).jobs[0];
  assert.equal(job.status, "partial"); assert.equal(job.partial_reason, "rejected_entries=2");
  assert.equal((await eng.getOperation(A, op.operation_id)).status, "partial");
  assert.equal(await docs(db), 1);
});

test("permanent failure goes straight to failed (no retry_wait)", async () => {
  const { eng, src } = await setup(async () => { throw new Error("invalid feed XML"); });
  await eng.requestCollection(A, "k", { source_ids: [src.id] });
  assert.equal((await eng.workOnce("w"))!.status, "failed");
});

test("operationStatus aggregation", () => {
  assert.equal(operationStatus(["succeeded", "partial"]), "partial");
  assert.equal(operationStatus(["succeeded", "failed"]), "partial");
  assert.equal(operationStatus(["retry_wait", "succeeded"]), "retrying");
  assert.equal(operationStatus(["leased"]), "running");
  assert.equal(operationStatus(["failed"]), "failed");
});

const windows = async (db: any) => (await db.query(`SELECT scheduled_at FROM flow_collection_runs WHERE schedule_id IS NOT NULL ORDER BY scheduled_at`)).rows.map((r: any) => new Date(r.scheduled_at).toISOString());

test("misfire_policy: coalesce (default) runs one catch-up, skip drops late windows, catch_up replays at most 3", async () => {
  for (const [policy, expected] of [
    ["coalesce", ["2026-10-13T05:00:00.000Z"]],
    ["skip", []],
    ["catch_up", ["2026-10-13T03:00:00.000Z", "2026-10-13T04:00:00.000Z", "2026-10-13T05:00:00.000Z"]],
  ] as const) {
    const { db, eng, clock, src } = await setup();
    const sc = await eng.createSchedule(A, "u", "p", { source_ids: [src.id], interval_seconds: 3600, misfire_policy: policy });
    assert.equal(sc.misfire_policy, policy);
    clock.now = new Date("2026-10-13T05:30:00Z"); // windows 00:00 … 05:00 were missed
    await eng.schedulerTick();
    assert.deepEqual(await windows(db), expected, policy);
    assert.equal(new Date((await eng.listSchedules(A))[0].next_run_at).toISOString(), "2026-10-13T06:00:00.000Z", policy);
  }
  // skip still runs a window that is on time.
  const { db, eng, clock, src } = await setup();
  await eng.createSchedule(A, "u", "p", { source_ids: [src.id], interval_seconds: 3600, misfire_policy: "skip" });
  clock.now = new Date("2026-10-13T00:01:00Z");
  await eng.schedulerTick();
  assert.deepEqual(await windows(db), ["2026-10-13T00:00:00.000Z"]);
  await assert.rejects(eng.createSchedule(A, "u", "p", { source_ids: [src.id], interval_seconds: 3600, misfire_policy: "all" }), (e: any) => e.status === 400);
});

test("cron collection schedule in an IANA timezone, idempotent per window", async () => {
  const { db, eng, clock, src } = await setup();
  const sc = await eng.createSchedule(A, "u", "p", { source_ids: [src.id], cron: "0 9,18 * * *", timezone: "Asia/Shanghai" });
  assert.equal(sc.kind, "cron"); assert.equal(sc.interval_seconds, null);
  assert.equal(new Date(sc.next_run_at).toISOString(), "2026-10-13T01:00:00.000Z"); // 09:00 Asia/Shanghai
  clock.now = new Date("2026-10-13T01:00:20Z");
  assert.equal(await eng.schedulerTick(), 1);
  await db.query(`UPDATE flow_schedules SET next_run_at='2026-10-13T01:00:00Z'`); // duplicate tick, same window
  assert.equal(await eng.schedulerTick(), 0);
  assert.equal(new Date((await eng.listSchedules(A))[0].next_run_at).toISOString(), "2026-10-13T10:00:00.000Z"); // 18:00
  // Down for two days with coalesce: one run for the latest missed window only.
  clock.now = new Date("2026-10-15T12:00:00Z");
  assert.equal(await eng.schedulerTick(), 1);
  assert.equal((await windows(db)).at(-1), "2026-10-15T10:00:00.000Z");
  for (const bad of [{ cron: "61 * * * *" }, { cron: "0 9 * * *", timezone: "Mars/Base" }, { cron: "0 9 * * *", interval_seconds: 3600 }, { interval_seconds: 10 }]) {
    await assert.rejects(eng.createSchedule(A, "u", "p", { source_ids: [src.id], ...bad } as any), (e: any) => e.status === 400, JSON.stringify(bad));
  }
});

test("PRD §6.2 name: a waiting (queued) job is reported as job_state=pending", async () => {
  const { eng, src } = await setup();
  const op = await eng.requestCollection(A, "k-pending", { source_ids: [src.id] });
  const view = await eng.getOperation(A, op.operation_id);
  assert.equal(view.status, "queued"); // §5.2 user-facing operation status
  assert.equal(view.jobs[0].status, "queued");
  assert.equal(view.jobs[0].job_state, "pending");
  assert.equal(prdJobState("retry_wait"), "retry_wait");
});
