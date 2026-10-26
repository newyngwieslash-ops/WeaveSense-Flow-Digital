import { apiClient } from "./request";

/**
 * 数学工作台接口。edumath 的错误放在 data.error.code，并以 HTTP 状态返回，
 * 因此这里自行解包，保留机器可读的错误码，页面只按错误码分支。
 * 学生请求携带 X-Edu-Tenant，教师请求携带 X-Workspace-Id；权限全部由服务端判定。
 */
export type EduScope = { tenant?: string; workspace?: string };
export type EduResult<T> = { ok: true; status: number; data: T } | { ok: false; status: number; code: string; message: string; offline?: boolean };

export async function edu<T>(method: "get" | "post" | "put", path: string, scope: EduScope, body?: unknown): Promise<EduResult<T>> {
    const headers: Record<string, string> = {};
    if (scope.tenant) headers["X-Edu-Tenant"] = scope.tenant;
    if (scope.workspace) headers["X-Workspace-Id"] = scope.workspace;
    try {
        const res = await apiClient.request({ method, url: `/edu${path}`, data: body, headers, validateStatus: () => true });
        const env = (res.data || {}) as { code?: number; data?: unknown; msg?: string };
        if (res.status < 400 && (env.code ?? 0) === 0) return { ok: true, status: res.status, data: env.data as T };
        const err = (env.data as { error?: { code?: string; message?: string } } | null)?.error;
        return { ok: false, status: res.status, code: err?.code || `HTTP_${res.status}`, message: err?.message || env.msg || "请求失败" };
    } catch {
        return { ok: false, status: 0, code: "NETWORK", message: "网络不可用", offline: true };
    }
}

export type EduRoleBinding = { tenantId: string; role: string; status: string };
export type EduMe = { userId: string; roles: EduRoleBinding[] };
export type EduAttempt = {
    id: string;
    clientKey: string;
    item: "main" | "check";
    kind: "answer" | "no_idea";
    response: string;
    valid: boolean;
    result: string;
    firstValid: boolean;
    independent: boolean;
    hintLevel: number;
    late: boolean;
    createdAt: string;
    studentUserId?: string;
    teacherResult?: string;
    teacherNote?: string;
};
export type StudentLesson = {
    projection: "student";
    reveal: string;
    meta: { title: string; family_id: string };
    problem: { text: string; unit?: string };
    figure?: unknown;
    hints: { level: number; kind: string; text: string }[];
    gated_hint_levels: number[];
    check_question?: { prompt?: string };
    beats: { id: string; title: string; locked?: boolean }[];
};
export type LearningState = {
    policy: { startsAt?: string; dueAt?: string; allowLate: boolean; answerPolicy: string; answerAvailable: boolean; late: boolean; closed: boolean; unitPolicy: string; approxAllowed: boolean };
    attempts: EduAttempt[];
    hintLevels: number[];
    selfTest: null | { state: string; helpUsed: boolean; sawSolution: boolean; answerKey?: { plain: string } };
    review: null | { id: string; stage: number; dueAt: string; state: string };
    predictions?: { promptId: string; choice: number }[];
};
export type HintBody = { level: number; kind: string; text?: string; beats?: { id: string; title: string; board?: string | string[]; say?: string }[]; answer?: { plain: string } };

export const RESULT_LABEL: Record<string, string> = {
    correct: "正确",
    correct_approx: "近似正确",
    incorrect: "不正确",
    needs_exact: "请给出精确值",
    needs_teacher: "等待老师判断",
    invalid: "无法识别",
    no_idea: "暂时不会",
    undetermined: "暂未判定",
    partial: "部分正确",
    untested: "未作答",
    pending_judgement: "待老师判断",
};

export function newClientKey(): string {
    const c = globalThis.crypto;
    if (c?.randomUUID) return c.randomUUID().replace(/-/g, "");
    return `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 12)}`;
}
