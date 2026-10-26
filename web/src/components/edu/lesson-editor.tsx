import { useCallback, useEffect, useMemo, useState } from "react";

import { edu, type EduScope } from "@/services/api/edu";

import { conflictOf, diffEdits, draftKey, draftOf, draftProblems, EDIT_CLASS_LABEL, estimateText, INHERIT_LABEL, INVALIDATES_LABEL, type Draft, type Editable } from "./lesson-edit-logic";

/**
 * 教师端：讲解的结构化编辑（AC-17/18）。只改字段，不改 HTML 或代码；保存生成新修订，原修订与已发布版本不变。
 * 草稿自动保存在本机，冲突时保留本人修改，可另存为分支或打开最新修订。
 */
const btn = "inline-flex h-8 items-center rounded-md bg-stone-900 px-3 text-sm text-white disabled:opacity-40 dark:bg-stone-100 dark:text-stone-900";
const ghost = "inline-flex h-8 items-center rounded-md border border-stone-300 px-2 text-sm disabled:opacity-40 dark:border-stone-600";
const input = "w-full rounded-md border border-stone-300 bg-transparent px-2 py-1 text-sm dark:border-stone-600";
const TERMINAL = new Set(["succeeded", "failed", "cancelled"]);

type Impact = {
    impact: { classes: string[]; invalidates: string[]; manualCheck: boolean; regenerates: string[] };
    estimate: { maxComputeMs: number; maxCostMicro: number; unitMicro: number; priceVersion: string };
    approvalInherited: boolean;
    editReviewInherit: string;
};
type JobRow = { id: string; status: string; stage: string; error?: { code?: string; message?: string } | null; lessonRevisionId?: string };
type HistoryNode = { id: string; lessonHash: string; state: string; editClasses?: string; approvalInheritedFrom?: string; createdAt: string };

export function LessonEditor({ scope, lessonId, onOpenRevision }: { scope: EduScope; lessonId: string; onOpenRevision: (id: string) => void }) {
    const [ed, setEd] = useState<Editable | null>(null);
    const [draft, setDraft] = useState<Draft | null>(null);
    const [undo, setUndo] = useState<Draft[]>([]);
    const [impact, setImpact] = useState<Impact | null>(null);
    const [job, setJob] = useState<JobRow | null>(null);
    const [conflict, setConflict] = useState<ReturnType<typeof conflictOf>>(null);
    const [history, setHistory] = useState<HistoryNode[]>([]);
    const [msg, setMsg] = useState("");

    const load = useCallback(() => {
        void edu<Editable>("get", `/lesson-revisions/${lessonId}/editable`, scope).then((r) => {
            if (!r.ok) return setMsg(`${r.code}：${r.message}`);
            setEd(r.data);
            const saved = window.localStorage.getItem(draftKey(lessonId, r.data.lessonHash));
            setDraft(saved ? (JSON.parse(saved) as Draft) : draftOf(r.data));
            setUndo([]);
            setImpact(null);
            setConflict(null);
        });
        void edu<{ lineage: HistoryNode[] }>("get", `/lesson-revisions/${lessonId}/history`, scope).then((r) => r.ok && setHistory(r.data.lineage));
    }, [lessonId, scope]);
    useEffect(load, [load]);

    const edits = useMemo(() => (ed && draft ? diffEdits(ed, draft) : []), [ed, draft]);
    const problems = useMemo(() => (ed ? draftProblems(ed, edits) : []), [ed, edits]);

    useEffect(() => {
        if (!ed || !draft) return;
        const key = draftKey(lessonId, ed.lessonHash);
        if (edits.length) window.localStorage.setItem(key, JSON.stringify(draft));
        else window.localStorage.removeItem(key);
    }, [draft, ed, edits.length, lessonId]);

    useEffect(() => {
        if (!job || TERMINAL.has(job.status)) return;
        const t = window.setTimeout(() => {
            void edu<JobRow>("get", `/jobs/${job.id}`, scope).then((r) => {
                if (!r.ok) return;
                setJob(r.data);
                if (r.data.status === "succeeded" && r.data.lessonRevisionId && ed) {
                    window.localStorage.removeItem(draftKey(lessonId, ed.lessonHash));
                    onOpenRevision(r.data.lessonRevisionId);
                }
            });
        }, 1500);
        return () => window.clearTimeout(t);
    }, [job, scope, ed, lessonId, onOpenRevision]);

    if (!ed || !draft) return <p className="text-sm text-stone-500">{msg || "载入可编辑内容…"}</p>;

    const change = (next: Draft) => {
        setUndo((u) => [...u.slice(-49), draft]);
        setDraft(next);
        setImpact(null);
    };
    const setBeat = (id: string, field: "title" | "say" | "board", v: string) => change({ ...draft, beats: { ...draft.beats, [id]: { ...draft.beats[id], [field]: v } } });
    const preview = async () => {
        const r = await edu<Impact>("post", `/lesson-revisions/${lessonId}/edit-impact`, scope, { edits });
        if (!r.ok) return setMsg(r.code === "POLICY_REVOKED" ? "这部分不能在讲解里修改，请新建题目修订" : `${r.code}：${r.message}`);
        setMsg("");
        setImpact(r.data);
    };
    const save = async (branch: boolean) => {
        const r = await edu<{ job: JobRow }>("post", `/lesson-revisions/${lessonId}/edits`, scope, {
            baseLessonHash: ed.lessonHash,
            edits,
            saveAs: branch ? "branch" : undefined,
            idempotencyKey: crypto.randomUUID(),
        });
        if (r.ok) {
            setConflict(null);
            setMsg("");
            return setJob(r.data.job);
        }
        const c = conflictOf(r.detail);
        if (c) return setConflict(c);
        setMsg(r.code === "BUDGET_EXCEEDED" ? "额度不足，未提交计算" : `${r.code}：${r.message}`);
    };
    const unverified = ed.quality?.edited_unverified || [];

    return (
        <div className="space-y-3" data-testid="lesson-editor">
            <p className="text-xs text-stone-500">
                基于修订 {ed.lessonRevisionId}（哈希 {ed.lessonHash.slice(7, 19)}）。保存后生成新修订，本修订与已发布版本不变。机构规则：{INHERIT_LABEL[ed.editReviewInherit] || ed.editReviewInherit}（待确认）。
            </p>
            {unverified.length ? <p className="text-sm text-amber-700">本修订有老师改过的板书（{unverified.join("、")}），未经自动验证，审核时请逐行核对。</p> : null}
            {ed.beats.map((b) => (
                <fieldset key={b.id} className="rounded-md border border-stone-200 p-2 dark:border-stone-700">
                    <legend className="px-1 text-xs text-stone-500">
                        步骤 {b.id}
                        {b.reveals_answer ? "（含答案，学生按答案政策解锁）" : ""}
                    </legend>
                    <input aria-label={`${b.id} 标题`} className={input} value={draft.beats[b.id]?.title ?? ""} onChange={(e) => setBeat(b.id, "title", e.target.value)} />
                    <textarea aria-label={`${b.id} 讲解`} className={`${input} mt-1`} rows={2} value={draft.beats[b.id]?.say ?? ""} onChange={(e) => setBeat(b.id, "say", e.target.value)} />
                    <textarea aria-label={`${b.id} 板书`} className={`${input} mt-1 font-mono`} rows={2} value={draft.beats[b.id]?.board ?? ""} onChange={(e) => setBeat(b.id, "board", e.target.value)} />
                </fieldset>
            ))}
            {ed.hints.map((h) => (
                <label key={h.level} className="block text-sm">
                    提示 {h.level}（{h.kind}）
                    <textarea className={input} rows={2} value={draft.hints[h.level] ?? ""} onChange={(e) => change({ ...draft, hints: { ...draft.hints, [h.level]: e.target.value } })} />
                </label>
            ))}
            <div className="text-sm">
                读音
                {draft.pronunciations.map((p, i) => (
                    <div key={i} className="mt-1 flex gap-2">
                        <input aria-label="术语" className={input} value={p.term} onChange={(e) => change({ ...draft, pronunciations: draft.pronunciations.map((x, j) => (j === i ? { ...x, term: e.target.value } : x)) })} />
                        <input aria-label="读音" className={input} value={p.reading} onChange={(e) => change({ ...draft, pronunciations: draft.pronunciations.map((x, j) => (j === i ? { ...x, reading: e.target.value } : x)) })} />
                        <button className={ghost} onClick={() => change({ ...draft, pronunciations: draft.pronunciations.filter((_, j) => j !== i) })}>
                            删除
                        </button>
                    </div>
                ))}
                <button className={`${ghost} mt-1`} onClick={() => change({ ...draft, pronunciations: [...draft.pronunciations, { term: "", reading: "" }] })}>
                    添加读音
                </button>
            </div>

            <div className="flex flex-wrap items-center gap-2">
                <span className="text-sm" data-testid="edit-count">
                    {edits.length} 处修改
                </span>
                <button className={ghost} disabled={!undo.length} onClick={() => (setDraft(undo[undo.length - 1]), setUndo(undo.slice(0, -1)), setImpact(null))}>
                    撤销
                </button>
                <button className={ghost} disabled={!edits.length} onClick={() => change(draftOf(ed))}>
                    放弃全部修改
                </button>
                <button className={ghost} disabled={!edits.length || problems.length > 0} onClick={() => void preview()}>
                    查看影响
                </button>
                <button className={btn} disabled={!impact || (!!job && !TERMINAL.has(job.status))} onClick={() => void save(false)}>
                    保存为新修订
                </button>
            </div>
            {problems.length ? <p className="text-sm text-red-700">{problems.join("；")}</p> : null}
            {impact ? (
                <div className="rounded-md bg-stone-50 p-2 text-sm dark:bg-stone-800" data-testid="edit-impact">
                    <p>修改类型：{impact.impact.classes.map((c) => EDIT_CLASS_LABEL[c] || c).join("、")}</p>
                    <p>需要重新生成或失效：{impact.impact.invalidates.map((c) => INVALIDATES_LABEL[c] || c).join("、")}；题目、解答、验证、图形和检查题保持不变。</p>
                    {impact.impact.manualCheck ? <p className="text-amber-700">改动了推理板书：不会自动验证，必须人工审核。</p> : null}
                    <p>{impact.approvalInherited ? "仅改读音：按机构规则继承上一版审核结论。" : "保存后需要重新审核才能发布。"}</p>
                    <p className="text-stone-500">{estimateText(impact.estimate)}</p>
                </div>
            ) : null}
            {conflict ? (
                <div className="rounded-md border border-amber-400 p-2 text-sm" data-testid="edit-conflict">
                    <p>
                        {conflict.reason === "stale_hash" ? "该修订已变化。" : "已有其他修改基于这一修订保存。"}你的 {conflict.yourEdits.length} 处修改已保留在本机。
                    </p>
                    <div className="mt-1 flex gap-2">
                        <button className={btn} onClick={() => void save(true)}>
                            另存为分支
                        </button>
                        {conflict.latestRevisionId && conflict.latestRevisionId !== lessonId ? (
                            <button className={ghost} onClick={() => onOpenRevision(conflict.latestRevisionId)}>
                                打开最新修订
                            </button>
                        ) : null}
                    </div>
                </div>
            ) : null}
            {job ? (
                <p className="text-sm" data-testid="edit-job">
                    {job.status === "succeeded" ? "已生成新修订，待审核" : job.status === "failed" ? `生成失败：${job.error?.code || ""} ${job.error?.message || ""}（草稿仍保留）` : `正在重新生成（${job.stage || job.status}）`}
                </p>
            ) : null}
            {msg ? <p className="text-sm text-red-700">{msg}</p> : null}
            {history.length > 1 ? (
                <details className="text-sm">
                    <summary>修订历史</summary>
                    <ol className="mt-1 list-decimal pl-5">
                        {history.map((n) => (
                            <li key={n.id}>
                                {n.id} {n.state}
                                {n.editClasses
                                    ? `，修改：${n.editClasses
                                          .split(",")
                                          .map((c) => EDIT_CLASS_LABEL[c] || c)
                                          .join("、")}`
                                    : "，原始生成"}
                                {n.approvalInheritedFrom ? "，审核继承" : ""}
                            </li>
                        ))}
                    </ol>
                </details>
            ) : null}
        </div>
    );
}
