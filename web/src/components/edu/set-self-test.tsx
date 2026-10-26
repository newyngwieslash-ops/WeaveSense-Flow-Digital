import { useState } from "react";

import { RESULT_LABEL, type EduAttempt, type LearningState } from "@/services/api/edu";

import { setProgress } from "./edu-admin-logic";

/**
 * 学生端题组自测（FR-S08）：独立自测单元内每题只提交一次，全部提交前只显示“已记录”，
 * 不给对错与答案；全部提交后服务端才返回结果与参考答案。
 */
const btn = "inline-flex h-8 items-center rounded-md bg-stone-900 px-3 text-sm text-white disabled:opacity-40 dark:bg-stone-100 dark:text-stone-900";

export function SetSelfTest({ selfTest, attempts, busy, onSubmit }: { selfTest: NonNullable<LearningState["selfTest"]>; attempts: EduAttempt[]; busy: boolean; onSubmit: (item: string, response: string) => void }) {
    const [draft, setDraft] = useState<Record<string, string>>({});
    const questions = selfTest.questions || [];
    const { done, remaining } = setProgress(questions, attempts);
    const submitted = selfTest.state === "submitted";
    return (
        <div className="space-y-3 text-sm" data-testid="set-self-test">
            <p className="text-xs text-stone-500">
                共 {questions.length} 题，每题只能提交一次；{submitted ? "已全部提交" : `还剩 ${remaining} 题，全部提交后显示结果`}
                {selfTest.helpUsed || selfTest.sawSolution ? "（使用过帮助，不计为独立完成）" : ""}
            </p>
            <ol className="list-decimal space-y-3 pl-5">
                {questions.map((q) => {
                    const at = done.get(q.id);
                    return (
                        <li key={q.id}>
                            <p className="whitespace-pre-wrap">{q.prompt}</p>
                            {at ? (
                                <p className="mt-1">
                                    你的答案：{at.response}；{RESULT_LABEL[at.result] || at.result}
                                    {submitted && selfTest.answerKeys?.[q.id] ? `；参考答案：${selfTest.answerKeys[q.id].plain}` : ""}
                                </p>
                            ) : (
                                <div className="mt-1 flex gap-2">
                                    <input className="h-8 flex-1 rounded-md border px-2" aria-label={`第 ${q.id} 题答案`} maxLength={200} value={draft[q.id] || ""} onChange={(e) => setDraft({ ...draft, [q.id]: e.target.value })} />
                                    <button className={btn} disabled={busy || !(draft[q.id] || "").trim()} onClick={() => onSubmit(q.id, draft[q.id] || "")}>
                                        提交本题
                                    </button>
                                </div>
                            )}
                        </li>
                    );
                })}
            </ol>
        </div>
    );
}
