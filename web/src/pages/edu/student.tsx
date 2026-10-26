import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router";

import { clearOtherUsers, enqueue, flushQueue, readQueue, type PendingAttempt } from "@/lib/edu-offline-queue";
import { edu, newClientKey, RESULT_LABEL, type EduAttempt, type EduMe, type HintBody, type LearningState, type StudentLesson } from "@/services/api/edu";
import { useUserStore } from "@/stores/use-user-store";

/**
 * 学生端：作业列表、作答、分层提示、自测、求助、复习与进度。
 * 页面只渲染服务端返回的学生投影；答案与完整讲解只在服务端放行后由接口返回，前端不持有也不隐藏。
 */
const box = "rounded-lg border border-stone-200 p-4 dark:border-stone-700";
const btn = "inline-flex h-9 items-center rounded-md bg-stone-900 px-3 text-sm text-white disabled:opacity-40 dark:bg-stone-100 dark:text-stone-900";
const ghost = "inline-flex h-9 items-center rounded-md border border-stone-300 px-3 text-sm disabled:opacity-40 dark:border-stone-600";

const HELP_KINDS: [string, string][] = [
    ["dont_understand", "这一步看不懂"],
    ["ask_teacher", "想问老师"],
    ["content_error", "内容可能有错"],
    ["access_problem", "打不开或显示异常"],
];

function useStudentTenant() {
    const [tenants, setTenants] = useState<string[] | null>(null);
    const [tenant, setTenant] = useState("");
    const [error, setError] = useState("");
    useEffect(() => {
        void edu<EduMe>("get", "/me", {}).then((r) => {
            if (!r.ok) return setError(r.message);
            const ts = [...new Set(r.data.roles.filter((b) => b.role === "student").map((b) => b.tenantId))];
            setTenants(ts);
            setTenant(ts[0] || "");
        });
    }, []);
    return { tenants, tenant, setTenant, error };
}

function useOfflineFlush(tenant: string, onFlushed: () => void) {
    const userId = useUserStore((s) => s.user?.id) || "";
    const [pending, setPending] = useState(0);
    const flush = useCallback(async () => {
        if (!userId) return;
        const r = await flushQueue(localStorage, userId, async (p: PendingAttempt) => {
            const res = await edu("post", `/student/assignments/${encodeURIComponent(p.assignmentId)}/attempts`, { tenant: p.tenant }, { item: p.item, kind: p.kind, response: p.response, clientKey: p.clientKey, reviewItemId: p.reviewItemId });
            return res.ok ? { ok: true } : { ok: false, offline: res.offline, code: res.code };
        });
        setPending(r.remaining.length);
        if (r.sent.length) onFlushed();
    }, [userId, onFlushed]);
    useEffect(() => {
        clearOtherUsers(localStorage, userId || null);
        setPending(userId ? readQueue(localStorage, userId).length : 0);
        void flush();
        window.addEventListener("online", flush);
        return () => window.removeEventListener("online", flush);
    }, [userId, tenant, flush]);
    const queue = (p: PendingAttempt) => {
        if (!userId) return;
        setPending(enqueue(localStorage, userId, p).length);
    };
    return { pending, queue, flush };
}

export function StudentHome() {
    const { tenants, tenant, setTenant, error } = useStudentTenant();
    const [list, setList] = useState<{ id: string; title: string }[]>([]);
    const [review, setReview] = useState<{ id: string; assignmentId: string; title: string; stage: number; dueAt: string; due: boolean }[]>([]);
    const [progress, setProgress] = useState<{
        summary: { independentResults: number; minimumNeeded: number; evidence: string };
        assignments: { assignmentId: string; title: string; firstAttempt: string; attempts: number; selfTest: string; selfTestIndependent: boolean }[];
    } | null>(null);
    useEffect(() => {
        if (!tenant) return;
        const s = { tenant };
        void edu<{ id: string; title: string }[]>("get", "/student/assignments", s).then((r) => r.ok && setList(r.data));
        void edu<typeof review>("get", "/student/review", s).then((r) => r.ok && setReview(r.data));
        void edu<NonNullable<typeof progress>>("get", "/student/progress", s).then((r) => r.ok && setProgress(r.data));
    }, [tenant]);
    if (error) return <Shell title="我的学习">{error}</Shell>;
    if (tenants && tenants.length === 0) return <Shell title="我的学习">还没有加入任何班级。请使用老师提供的邀请码加入。</Shell>;
    return (
        <Shell title="我的学习">
            {tenants && tenants.length > 1 ? (
                <select className="mb-4 h-9 rounded-md border px-2" value={tenant} onChange={(e) => setTenant(e.target.value)}>
                    {tenants.map((t) => (
                        <option key={t}>{t}</option>
                    ))}
                </select>
            ) : null}
            <section className={box}>
                <h2 className="mb-2 font-semibold">作业</h2>
                {list.length === 0 ? <p className="text-sm text-stone-500">暂无作业</p> : null}
                <ul className="space-y-2">
                    {list.map((a) => (
                        <li key={a.id}>
                            <Link className="underline" to={`/edu/learn/${a.id}?tenant=${encodeURIComponent(tenant)}`}>
                                {a.title}
                            </Link>
                        </li>
                    ))}
                </ul>
            </section>
            <section className={`${box} mt-4`}>
                <h2 className="mb-2 font-semibold">复习</h2>
                {review.length === 0 ? <p className="text-sm text-stone-500">没有待复习的题目</p> : null}
                <ul className="space-y-1 text-sm">
                    {review.map((r) => (
                        <li key={r.id}>
                            {r.title}（第 {r.stage + 1} 轮，{r.due ? "已到期" : `${new Date(r.dueAt).toLocaleString()} 到期`}）{" "}
                            {r.due ? (
                                <Link className="underline" to={`/edu/learn/${r.assignmentId}?tenant=${encodeURIComponent(tenant)}&review=${r.id}`}>
                                    去复习
                                </Link>
                            ) : null}
                        </li>
                    ))}
                </ul>
            </section>
            {progress ? (
                <section className={`${box} mt-4 text-sm`}>
                    <h2 className="mb-2 font-semibold">学习记录</h2>
                    <p>
                        独立完成的记录 {progress.summary.independentResults} 条{progress.summary.evidence === "insufficient" ? `，少于 ${progress.summary.minimumNeeded} 条，暂不判断掌握程度` : ""}
                    </p>
                    <ul className="mt-2 space-y-1">
                        {progress.assignments.map((a) => (
                            <li key={a.assignmentId}>
                                {a.title}：首次作答 {RESULT_LABEL[a.firstAttempt] || a.firstAttempt || "未作答"}，共 {a.attempts} 次
                                {a.selfTest ? `；同构变式自测 ${RESULT_LABEL[a.selfTest] || a.selfTest}${a.selfTestIndependent ? "（独立）" : "（使用过帮助）"}` : ""}
                            </li>
                        ))}
                    </ul>
                </section>
            ) : null}
        </Shell>
    );
}

export function StudentAssignment() {
    const { id = "" } = useParams();
    const qs = useMemo(() => new URLSearchParams(location.search), []);
    const tenant = qs.get("tenant") || "";
    const reviewItemId = qs.get("review") || undefined;
    const scope = useMemo(() => ({ tenant }), [tenant]);
    const [lesson, setLesson] = useState<StudentLesson | null>(null);
    const [previewUrl, setPreviewUrl] = useState("");
    const [state, setState] = useState<LearningState | null>(null);
    const [hints, setHints] = useState<HintBody[]>([]);
    const [answer, setAnswer] = useState("");
    const [check, setCheck] = useState("");
    const [checkPrompt, setCheckPrompt] = useState("");
    const [msg, setMsg] = useState("");
    const [busy, setBusy] = useState(false);
    const [helpKind, setHelpKind] = useState("dont_understand");
    const [helpText, setHelpText] = useState("");
    const [helpBeat, setHelpBeat] = useState("");

    const reload = useCallback(() => {
        void edu<LearningState>("get", `/student/assignments/${id}/learning`, scope).then((r) => (r.ok ? setState(r.data) : setMsg(r.message)));
    }, [id, scope]);
    const { pending, queue } = useOfflineFlush(tenant, reload);

    useEffect(() => {
        void edu<{ lesson: StudentLesson; preview: { url: string } }>("get", `/student/assignments/${id}`, scope).then((r) => {
            if (!r.ok) return setMsg(r.code === "NOT_STARTED" ? "作业尚未开始" : r.message);
            setLesson(r.data.lesson);
            setPreviewUrl(r.data.preview?.url || "");
        });
        reload();
    }, [id, scope, reload]);

    const submit = async (item: "main" | "check", kind: "answer" | "no_idea", response: string) => {
        setBusy(true);
        const clientKey = newClientKey();
        const body = { item, kind, response, clientKey, reviewItemId: item === "main" ? reviewItemId : undefined };
        const r = await edu<{ attempt: EduAttempt }>("post", `/student/assignments/${id}/attempts`, scope, body);
        setBusy(false);
        if (!r.ok && r.offline) {
            queue({ assignmentId: id, tenant, ...body, queuedAt: new Date().toISOString() });
            return setMsg("网络不可用，已暂存，恢复后自动提交");
        }
        if (!r.ok) return setMsg(errorText(r.code, r.message));
        setMsg(`已记录：${RESULT_LABEL[r.data.attempt.result] || r.data.attempt.result}`);
        if (item === "main") setAnswer("");
        else setCheck("");
        reload();
    };

    const nextHint = async () => {
        const level = (state?.hintLevels.length ? Math.max(...state.hintLevels) : 0) + 1;
        const r = await edu<HintBody>("post", `/student/assignments/${id}/hints`, scope, { level });
        if (!r.ok) return setMsg(errorText(r.code, r.message));
        setHints((h) => [...h.filter((x) => x.level !== r.data.level), r.data]);
        reload();
    };

    const startSelfTest = async () => {
        const r = await edu<{ question: { prompt?: string } }>("post", `/student/assignments/${id}/selftest/start`, scope, {});
        if (!r.ok) return setMsg(errorText(r.code, r.message));
        setCheckPrompt(r.data.question.prompt || "");
        reload();
    };

    const askHelp = async () => {
        const r = await edu("post", `/student/assignments/${id}/help`, scope, { kind: helpKind, beatId: helpBeat || undefined, message: helpText, clientKey: newClientKey() });
        setMsg(r.ok ? "已发送给老师" : errorText(r.code, r.message));
        if (r.ok) setHelpText("");
    };

    const p = state?.policy;
    const levels = state?.hintLevels || [];
    const nextLevel = (levels.length ? Math.max(...levels) : 0) + 1;
    return (
        <Shell title={lesson?.meta.title || "作业"} back>
            {msg ? <p className="mb-3 rounded-md bg-amber-50 p-2 text-sm text-amber-900 dark:bg-amber-950 dark:text-amber-100">{msg}</p> : null}
            {pending ? <p className="mb-3 text-sm text-stone-500">有 {pending} 条作答待网络恢复后提交</p> : null}
            {lesson ? (
                <section className={box}>
                    <p className="whitespace-pre-wrap leading-7">{lesson.problem.text}</p>
                    {previewUrl ? <iframe title="图形" src={previewUrl} sandbox="allow-scripts" className="mt-3 h-72 w-full rounded-md border" /> : null}
                    {p ? (
                        <p className="mt-2 text-xs text-stone-500">
                            {p.dueAt ? `截止 ${new Date(p.dueAt).toLocaleString()}；` : ""}
                            {p.unitPolicy === "required" ? "答案需带单位；" : ""}
                            {p.approxAllowed ? "允许近似值" : "请给出精确值"}
                            {p.closed ? "；已截止" : p.late ? "；已过截止时间，按迟交记录" : ""}
                        </p>
                    ) : null}
                </section>
            ) : null}

            <section className={`${box} mt-4`}>
                <h2 className="mb-2 font-semibold">{reviewItemId ? "复习作答" : "我的答案"}</h2>
                <div className="flex flex-wrap gap-2">
                    <input className="h-9 min-w-0 flex-1 rounded-md border px-2" value={answer} onChange={(e) => setAnswer(e.target.value)} placeholder="例如 2√22/11 cm" maxLength={200} />
                    <button className={btn} disabled={busy || p?.closed} onClick={() => void submit("main", "answer", answer)}>
                        提交
                    </button>
                    <button className={ghost} disabled={busy || p?.closed} onClick={() => void submit("main", "no_idea", "")}>
                        暂时不会
                    </button>
                </div>
                <ul className="mt-3 space-y-1 text-sm">
                    {(state?.attempts || [])
                        .filter((a) => a.item === "main")
                        .map((a) => (
                            <li key={a.id}>
                                {a.kind === "no_idea" ? "暂时不会" : a.response || "（空）"} → {RESULT_LABEL[a.teacherResult || a.result] || a.result}
                                {a.teacherResult ? "（老师判定）" : ""}
                                {a.late ? "（迟交）" : ""}
                            </li>
                        ))}
                </ul>
            </section>

            <section className={`${box} mt-4`}>
                <h2 className="mb-2 font-semibold">提示</h2>
                {hints
                    .sort((a, b) => a.level - b.level)
                    .map((h) => (
                        <div key={h.level} className="mb-2 text-sm">
                            <b>第 {h.level} 层</b>
                            {h.text ? <p className="whitespace-pre-wrap">{h.text}</p> : null}
                            {h.beats?.map((b) => (
                                <div key={b.id} className="mt-1">
                                    <p className="font-medium">{b.title}</p>
                                    <p className="whitespace-pre-wrap">{b.board || b.say}</p>
                                </div>
                            ))}
                            {h.answer ? <p className="mt-1">答案：{h.answer.plain}</p> : null}
                        </div>
                    ))}
                {nextLevel <= 5 ? (
                    <button className={ghost} onClick={() => void nextHint()}>
                        {nextLevel === 5 ? "查看完整讲解" : `获取第 ${nextLevel} 层提示`}
                    </button>
                ) : null}
            </section>

            <section className={`${box} mt-4`}>
                <h2 className="mb-2 font-semibold">举一反三自测</h2>
                {!state?.selfTest ? (
                    <button className={ghost} onClick={() => void startSelfTest()}>
                        开始自测
                    </button>
                ) : (
                    <>
                        {checkPrompt ? <p className="mb-2 whitespace-pre-wrap text-sm">{checkPrompt}</p> : null}
                        {state.selfTest.state !== "submitted" ? (
                            <div className="flex gap-2">
                                <input className="h-9 flex-1 rounded-md border px-2" value={check} onChange={(e) => setCheck(e.target.value)} maxLength={200} />
                                <button className={btn} disabled={busy} onClick={() => void submit("check", "answer", check)}>
                                    提交自测
                                </button>
                            </div>
                        ) : (
                            <p className="text-sm">
                                自测已提交{state.selfTest.answerKey ? `，参考答案：${state.selfTest.answerKey.plain}` : ""}
                                {state.selfTest.helpUsed || state.selfTest.sawSolution ? "（使用过帮助，不计为独立完成）" : ""}
                            </p>
                        )}
                    </>
                )}
            </section>

            <section className={`${box} mt-4`}>
                <h2 className="mb-2 font-semibold">求助与反馈</h2>
                <div className="flex flex-wrap gap-2">
                    <select className="h-9 rounded-md border px-2" value={helpKind} onChange={(e) => setHelpKind(e.target.value)}>
                        {HELP_KINDS.map(([k, v]) => (
                            <option key={k} value={k}>
                                {v}
                            </option>
                        ))}
                    </select>
                    <select className="h-9 rounded-md border px-2" value={helpBeat} onChange={(e) => setHelpBeat(e.target.value)}>
                        <option value="">整道题</option>
                        {lesson?.beats.map((b) => (
                            <option key={b.id} value={b.id}>
                                {b.title}
                            </option>
                        ))}
                    </select>
                </div>
                <textarea className="mt-2 w-full rounded-md border p-2 text-sm" rows={2} maxLength={300} value={helpText} onChange={(e) => setHelpText(e.target.value)} placeholder="可以补充说明（不必填写个人信息）" />
                <button className={`${ghost} mt-2`} onClick={() => void askHelp()}>
                    发送
                </button>
            </section>
        </Shell>
    );
}

function errorText(code: string, fallback: string) {
    const m: Record<string, string> = {
        ASSIGNMENT_CLOSED: "作业已截止",
        NOT_STARTED: "作业尚未开始",
        HINT_ORDER: "请按顺序获取提示",
        HINT_GATED: "这一层提示需要先作答一次",
        ANSWER_NOT_RELEASED: "老师还没有开放答案",
        SELFTEST_NOT_STARTED: "请先开始自测",
        REVIEW_NOT_DUE: "复习还没到时间",
        CONSENT_REQUIRED: "需要监护人同意后才能使用",
    };
    return m[code] || fallback;
}

function Shell({ title, back, children }: { title: string; back?: boolean; children: React.ReactNode }) {
    return (
        <main className="mx-auto w-full max-w-2xl px-4 py-6 text-foreground">
            {back ? (
                <Link to="/edu/learn" className="text-sm underline">
                    返回
                </Link>
            ) : null}
            <h1 className="mb-4 mt-1 text-xl font-semibold">{title}</h1>
            {children}
        </main>
    );
}
