import { describe, expect, it } from "bun:test";

import { classifyEmpty, EMPTY_REASON_TEXT } from "../src/pages/intelligence/empty-reason";
import type { CollectionJob, CollectionSchedule, SourceHealth } from "../src/services/api/intelligence";

const src = (o: Partial<SourceHealth> = {}): SourceHealth => ({ id: "s1", kind: "rss", url: "https://a", title: "A", status: "active", health: "healthy", ...o }) as SourceHealth;
const job = (status: string): CollectionJob => ({ job_id: "j", operation_id: "o", source_id: "s1", status, job_state: status, items_new: 0, items_revised: 0, attempts: 1, created_at: "2026-10-15T00:00:00Z" });
const sched = (o: Partial<CollectionSchedule>): CollectionSchedule => ({ id: "sc", source_ids: ["s1"], kind: "interval", timezone: "Asia/Shanghai", next_run_at: "", enabled: true, ...o });

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

    it("has user-facing text for every reason", () => {
        for (const r of ["no_sources", "permission_invalid", "source_failed", "queued", "budget_blocked", "zero_new", "not_run"] as const) {
            expect(EMPTY_REASON_TEXT[r].title.length).toBeGreaterThan(0);
        }
    });
});
