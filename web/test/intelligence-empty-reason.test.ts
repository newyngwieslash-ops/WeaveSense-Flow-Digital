import { describe, expect, it } from "bun:test";

import { classifyEmpty, EMPTY_REASON_TEXT, type EmptyReason } from "../src/pages/intelligence/empty-reason";
import type { CollectionJob, CollectionSchedule, SourceHealth } from "../src/services/api/intelligence";

const src = (o: Partial<SourceHealth> = {}): SourceHealth =>
    ({
        id: "s1",
        kind: "rss",
        url: "https://a",
        title: "A",
        status: "active",
        health: "healthy",
        ...o,
    }) as SourceHealth;

const job = (status: string, overrides: Partial<CollectionJob> = {}): CollectionJob => ({
    job_id: "j",
    operation_id: "o",
    source_id: "s1",
    status,
    job_state: status,
    items_new: 0,
    items_revised: 0,
    attempts: 1,
    created_at: "2026-10-15T00:00:00Z",
    ...overrides,
});

const sched = (o: Partial<CollectionSchedule>): CollectionSchedule => ({
    id: "sc",
    source_ids: ["s1"],
    kind: "interval",
    timezone: "Asia/Shanghai",
    next_run_at: "",
    enabled: true,
    ...o,
});

describe("PRD §12.2 empty-list reasons", () => {
    it("distinguishes no sources, permission, queued, budget, failure, zero new and not run", () => {
        expect(classifyEmpty({ sources: [], jobs: [] })).toBe("no_sources");
        expect(classifyEmpty({ sources: [src({ status: "revoked" })], jobs: [] })).toBe("no_sources");
        expect(classifyEmpty({ sources: [src({ status: "paused", paused_reason: "credential_invalid" })], jobs: [] })).toBe("permission_invalid");
        expect(classifyEmpty({ sources: [src()], jobs: [], schedules: [sched({ state: "blocked", blocked_reason: "grant_revoked" })] })).toBe("permission_invalid");
        expect(classifyEmpty({ sources: [src()], jobs: [job("queued")] })).toBe("queued");
        expect(classifyEmpty({ sources: [src()], jobs: [job("retry_wait")] })).toBe("queued");
        expect(classifyEmpty({ sources: [src()], jobs: [], schedules: [sched({ state: "blocked", blocked_reason: "budget_insufficient" })] })).toBe("budget_blocked");
        expect(classifyEmpty({ sources: [src({ health: "backoff" })], jobs: [job("failed")] })).toBe("source_failed");
        expect(classifyEmpty({ sources: [src()], jobs: [job("succeeded")] })).toBe("zero_new");
        expect(classifyEmpty({ sources: [src({ health: "never_run" })], jobs: [] })).toBe("not_run");
    });

    it("recognizes all active job states across both status and job_state fields", () => {
        const activeStates = ["queued", "pending", "leased", "running", "retry_wait", "reconciliation_required", "cancel_requested"];
        for (const st of activeStates) {
            expect(classifyEmpty({ sources: [src()], jobs: [job(st, { job_state: "succeeded" })] })).toBe("queued");
            expect(classifyEmpty({ sources: [src()], jobs: [job("failed", { status: "failed", job_state: st })] })).toBe("queued");
        }
    });

    it("enforces strict priority order when multiple empty conditions co-exist", () => {
        // 1. no_sources beats permission_invalid / queued / budget_blocked
        expect(
            classifyEmpty({
                sources: [src({ status: "revoked", paused_reason: "credential_invalid" })],
                jobs: [job("running")],
                schedules: [sched({ state: "blocked", blocked_reason: "budget_insufficient" })],
            }),
        ).toBe("no_sources");

        // 2. permission_invalid beats queued / budget_blocked / source_failed
        expect(
            classifyEmpty({
                sources: [src({ status: "paused", paused_reason: "credential_invalid", health: "backoff" })],
                jobs: [job("running")],
                schedules: [sched({ state: "blocked", blocked_reason: "budget_insufficient" })],
            }),
        ).toBe("permission_invalid");

        // 3. queued beats budget_blocked / source_failed / zero_new
        expect(
            classifyEmpty({
                sources: [src({ health: "backoff" })],
                jobs: [job("succeeded"), job("leased")],
                schedules: [sched({ state: "blocked", blocked_reason: "budget_insufficient" })],
            }),
        ).toBe("queued");

        // 4. budget_blocked beats source_failed / zero_new
        expect(
            classifyEmpty({
                sources: [src({ health: "degraded" })],
                jobs: [job("succeeded")],
                schedules: [sched({ state: "blocked", blocked_reason: "budget_insufficient" })],
            }),
        ).toBe("budget_blocked");

        // 5. source_failed beats zero_new when all active sources are failing
        expect(
            classifyEmpty({
                sources: [src({ health: "degraded" })],
                jobs: [job("succeeded")],
            }),
        ).toBe("source_failed");
    });

    it("evaluates source_failed and zero_new accurately across mixed active/paused sources and last_run", () => {
        // Paused non-credential source alone without runs is not_run, not source_failed
        expect(classifyEmpty({ sources: [src({ status: "paused", paused_reason: "manual" })], jobs: [] })).toBe("not_run");

        // One healthy active source + one failing active source does not trigger all-sources-failed
        expect(
            classifyEmpty({
                sources: [src({ id: "s1", health: "healthy" }), src({ id: "s2", health: "backoff" })],
                jobs: [],
            }),
        ).toBe("not_run");

        // Active source whose last_run failed triggers source_failed
        expect(
            classifyEmpty({
                sources: [src({ health: "healthy", last_run: { run_id: "r1", status: "failed", items_new: 0, items_revised: 0 } })],
                jobs: [],
            }),
        ).toBe("source_failed");

        // zero_new via source.last_run (partial or succeeded) even when jobs array is empty
        expect(
            classifyEmpty({
                sources: [src({ health: "healthy", last_run: { run_id: "r2", status: "partial", items_new: 0, items_revised: 0 } })],
                jobs: [],
            }),
        ).toBe("zero_new");

        // zero_new via job_state = partial
        expect(
            classifyEmpty({
                sources: [src()],
                jobs: [job("unknown", { status: "unknown", job_state: "partial" })],
            }),
        ).toBe("zero_new");
    });

    it("has user-facing Chinese title and detail text for every reason", () => {
        const allReasons: EmptyReason[] = ["no_sources", "permission_invalid", "source_failed", "queued", "budget_blocked", "zero_new", "not_run"];
        for (const r of allReasons) {
            expect(EMPTY_REASON_TEXT[r].title.length).toBeGreaterThan(0);
            expect(EMPTY_REASON_TEXT[r].detail.length).toBeGreaterThan(0);
        }
    });
});
