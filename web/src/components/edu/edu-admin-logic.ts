/**
 * 教师管理与自测题组的纯逻辑，便于单元测试。
 * 所有判断只用于界面提示；是否允许审核、布置、延期都由服务端最终决定。
 */
export type VariantItem = {
    id: string;
    family_id: string;
    params: Record<string, string>;
    unit: string;
    prompt: string;
    answer_format: string;
    change_type?: string;
    transfer?: string;
    method?: string;
    math_state?: string;
    evidence_levels?: string[];
};
export type VariantKey = { id: string; exact: string; plain: string; latex: string };
export type VariantSet = {
    id: string;
    lessonRevisionId: string;
    jobId: string;
    setHash: string;
    familyId: string;
    requested: number;
    itemCount: number;
    state: string;
    blockingErrors: string;
    createdBy: string;
    createdAt: string;
    reviewedAt?: string | null;
    items: VariantItem[];
    answerKeys?: Record<string, VariantKey>;
    dropped?: { id?: string; reason: string; params?: Record<string, string> }[];
    reviews?: { id: string; reviewerId: string; decision: string; comment: string; selfReview: boolean; createdAt: string }[];
    reviewMode?: string;
};

export const VARIANT_STATE_LABEL: Record<string, string> = {
    ready_for_review: "待审核",
    approved: "已审核通过，可布置",
    rejected: "已退回，不可布置",
};
/** 剔除原因：answer_reused、not_verified:<数学状态>，或数学服务错误码。 */
export function dropReasonLabel(reason: string): string {
    if (reason === "answer_reused") return "答案与原题或其他变式重复";
    if (reason.startsWith("not_verified:")) return `未通过独立验证（${reason.slice(13)}）`;
    return `求解失败（${reason}）`;
}

/** 只有待审核、无阻断错误且至少一题的题组才可提交“通过”。 */
export function variantApprovable(s: Pick<VariantSet, "state" | "blockingErrors" | "itemCount">): boolean {
    return s.state === "ready_for_review" && !s.blockingErrors && s.itemCount > 0;
}

/** 少于请求数量时如实告知，不补凑题目。 */
export function variantShortfall(s: Pick<VariantSet, "requested" | "itemCount">): string {
    if (s.itemCount >= s.requested) return "";
    return `请求 ${s.requested} 题，独立验证后保留 ${s.itemCount} 题（不足部分不会补凑）`;
}

export type InviteRow = { id: string; status: string; expiresAt: string; maxUses: number; uses: number; createdAt: string };
export const INVITE_STATUS_LABEL: Record<string, string> = {
    active: "有效",
    revoked: "已撤销",
    exhausted_or_expired: "已用尽或已过期",
};

export type RosterRow = { enrollmentId: string; studentUserId: string; status: string; consentOk: boolean };
export type ExtensionRow = { id: string; studentUserId: string; dueAt: string; reason: string; createdAt: string };

/**
 * 把 datetime-local 的本地时间转为带时区的 ISO 字符串，并校验延期晚于原截止时间。
 * 返回错误文本或 ISO 值；服务端仍会再校验一次。
 */
export function extensionDueISO(local: string, assignmentDue?: string | null): { ok: true; iso: string } | { ok: false; error: string } {
    if (!local) return { ok: false, error: "请选择延期截止时间" };
    const d = new Date(local);
    if (Number.isNaN(d.getTime())) return { ok: false, error: "时间格式无效" };
    if (assignmentDue && d.getTime() <= new Date(assignmentDue).getTime()) return { ok: false, error: "延期截止时间必须晚于作业原截止时间" };
    return { ok: true, iso: d.toISOString() };
}

export type SetAttempt = { id: string; item: string; result: string; valid: boolean; response: string };

/** 题组自测进度：哪些题已提交、还剩几题。每题只提交一次。 */
export function setProgress(questions: { id: string }[], attempts: SetAttempt[]) {
    const done = new Map<string, SetAttempt>();
    for (const a of attempts) if (a.item !== "main" && a.valid && !done.has(a.item)) done.set(a.item, a);
    const remaining = questions.filter((q) => !done.has(q.id)).length;
    return { done, remaining, complete: questions.length > 0 && remaining === 0 };
}
