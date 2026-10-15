import type { CollectionJob, CollectionSchedule, SourceHealth } from "@/services/api/intelligence";

/**
 * PRD §5.1 / §12.2: an empty material list must say why. Reasons, most actionable first:
 *  - no_sources: nothing to collect from yet
 *  - permission_invalid: a source's credential/authorization failed, or a schedule is blocked by a revoked grant
 *  - source_failed: every collecting source is failing (backoff/degraded) and the last runs failed
 *  - queued: collection is accepted but has not finished (queued/leased/running/retrying)
 *  - budget_blocked: the schedule window was denied by budget
 *  - zero_new: collection ran successfully and found nothing new
 *  - not_run: sources exist but nothing ran yet
 */
export type EmptyReason = "no_sources" | "permission_invalid" | "source_failed" | "queued" | "budget_blocked" | "zero_new" | "not_run";

const ACTIVE = new Set(["queued", "pending", "leased", "running", "retry_wait", "reconciliation_required", "cancel_requested"]);

export function classifyEmpty(input: { sources: SourceHealth[]; jobs: CollectionJob[]; schedules?: CollectionSchedule[] }): EmptyReason {
    const { sources, jobs, schedules = [] } = input;
    const live = sources.filter((s) => s.status !== "revoked");
    if (live.length === 0) return "no_sources";
    if (live.some((s) => s.paused_reason === "credential_invalid") || schedules.some((s) => s.state === "blocked" && s.blocked_reason === "grant_revoked")) return "permission_invalid";
    if (jobs.some((j) => ACTIVE.has(j.status))) return "queued";
    if (schedules.some((s) => s.state === "blocked" && s.blocked_reason === "budget_insufficient")) return "budget_blocked";
    const active = live.filter((s) => s.status === "active");
    if (active.length > 0 && active.every((s) => s.health === "backoff" || s.health === "degraded" || s.last_run?.status === "failed")) return "source_failed";
    if (jobs.some((j) => j.status === "succeeded" || j.status === "partial") || live.some((s) => s.last_run && (s.last_run.status === "succeeded" || s.last_run.status === "partial"))) return "zero_new";
    return "not_run";
}

export const EMPTY_REASON_TEXT: Record<EmptyReason, { title: string; detail: string }> = {
    no_sources: { title: "还没有信源", detail: "先在「信源」里添加 RSS、Telegram 或公众号来源。" },
    permission_invalid: { title: "权限已失效", detail: "有信源的凭据失效或计划的授权已被撤销，修复后才会继续采集。" },
    source_failed: { title: "来源采集失败", detail: "所有在用信源最近都采集失败，正在退避重试；查看「任务」里的失败原因。" },
    queued: { title: "采集排队中", detail: "采集任务已受理，还在排队或执行，完成后材料会出现在这里。" },
    budget_blocked: { title: "预算不足，计划受阻", detail: "本时间窗的预算申请被拒绝，下个窗口会重新申请。" },
    zero_new: { title: "没有新增内容", detail: "最近的采集都成功完成，但信源没有发布新内容。" },
    not_run: { title: "尚未采集", detail: "信源已添加，还没有执行过采集。" },
};
