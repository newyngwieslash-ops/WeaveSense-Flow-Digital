import { useCallback, useEffect, useMemo, useState } from "react";

import { edu, RESULT_LABEL, type EduAttempt } from "@/services/api/edu";
import { listIntelWorkspaces, type IntelWorkspace } from "@/services/api/intelligence";

/**
 * 教师端课堂视图：班级、作业、学情分布、求助分组与回复、逐条作答与人工判定、开放答案。
 * 所有数据均来自服务端已鉴权接口；分布只是统计，错因需要老师对照作答核实。
 */
const box = "rounded-lg border border-stone-200 p-4 dark:border-stone-700";
const btn = "inline-flex h-8 items-center rounded-md bg-stone-900 px-3 text-sm text-white disabled:opacity-40 dark:bg-stone-100 dark:text-stone-900";
const ghost = "inline-flex h-8 items-center rounded-md border border-stone-300 px-2 text-sm disabled:opacity-40 dark:border-stone-600";

type EduClass = { id: string; name: string; status: string };
type EduAssignmentRow = { id: string; title: string; state: string; answerPolicy: string; dueAt?: string; answerReleasedAt?: string };
type Dashboard = { students: number; distribution: Record<string, number>; selfTest: { independent: number; independentCorrect: number }; help: { open: number; openReports: number }; note: string };
type HelpGroup = { groupKey: string; kind: string; beatId: string; priority: number; open: number; requests: { id: string; message: string; state: string; createdAt: string }[] };

const DIST_LABEL: Record<string, string> = {
    not_started: "未开始",
    attempted: "已尝试",
    completed_independently: "独立完成",
    completed_after_hints: "借助提示完成",
    pending_teacher: "待老师判断",
};
const KIND_LABEL: Record<string, string> = { content_error: "内容可能有错", access_problem: "访问或显示问题", dont_understand: "看不懂", ask_teacher: "提问" };

export default function TeacherClassroom() {
    const [workspaces, setWorkspaces] = useState<IntelWorkspace[]>([]);
    const [ws, setWs] = useState("");
    const scope = useMemo(() => ({ workspace: ws }), [ws]);
    const [classes, setClasses] = useState<EduClass[]>([]);
    const [classId, setClassId] = useState("");
    const [assignments, setAssignments] = useState<EduAssignmentRow[]>([]);
    const [asId, setAsId] = useState("");
    const [dash, setDash] = useState<Dashboard | null>(null);
    const [help, setHelp] = useState<HelpGroup[]>([]);
    const [attempts, setAttempts] = useState<EduAttempt[]>([]);
    const [msg, setMsg] = useState("");

    useEffect(() => {
        void listIntelWorkspaces().then((l) => {
            setWorkspaces(l);
            setWs(l[0]?.id || "");
        });
    }, []);
    useEffect(() => {
        if (!ws) return;
        void edu<EduClass[]>("get", "/classes", scope).then((r) => (r.ok ? setClasses(r.data) : setMsg(r.message)));
    }, [ws, scope]);
    useEffect(() => {
        if (!classId) return;
        void edu<EduAssignmentRow[]>("get", `/classes/${classId}/assignments`, scope).then((r) => r.ok && setAssignments(r.data));
    }, [classId, scope]);

    const load = useCallback(() => {
        if (!asId) return;
        void edu<Dashboard>("get", `/assignments/${asId}/dashboard`, scope).then((r) => (r.ok ? setDash(r.data) : setMsg(r.message)));
        void edu<HelpGroup[]>("get", `/assignments/${asId}/help`, scope).then((r) => r.ok && setHelp(r.data));
        void edu<{ attempts: EduAttempt[] }>("get", `/assignments/${asId}/attempts`, scope).then((r) => r.ok && setAttempts(r.data.attempts));
    }, [asId, scope]);
    useEffect(load, [load]);

    const judge = async (id: string, result: string) => {
        const r = await edu("post", `/attempts/${id}/judge`, scope, { result });
        setMsg(r.ok ? "已记录老师判定" : r.message);
        load();
    };
    const release = async () => {
        if (!confirm("开放后学生可查看完整讲解和答案，且无法撤回。确定开放？")) return;
        const r = await edu("post", `/assignments/${asId}/release-answer`, scope, {});
        setMsg(r.ok ? "答案已开放" : r.message);
    };

    return (
        <main className="mx-auto w-full max-w-5xl px-4 py-6 text-foreground">
            <h1 className="mb-4 text-xl font-semibold">课堂与学情</h1>
            {msg ? <p className="mb-3 rounded-md bg-amber-50 p-2 text-sm text-amber-900 dark:bg-amber-950 dark:text-amber-100">{msg}</p> : null}
            <div className="mb-4 flex flex-wrap gap-2">
                <select className="h-9 rounded-md border px-2" value={ws} onChange={(e) => setWs(e.target.value)}>
                    {workspaces.map((w) => (
                        <option key={w.id} value={w.id}>
                            {w.name}
                        </option>
                    ))}
                </select>
                <select className="h-9 rounded-md border px-2" value={classId} onChange={(e) => setClassId(e.target.value)}>
                    <option value="">选择班级</option>
                    {classes.map((c) => (
                        <option key={c.id} value={c.id}>
                            {c.name}
                        </option>
                    ))}
                </select>
                <select className="h-9 rounded-md border px-2" value={asId} onChange={(e) => setAsId(e.target.value)}>
                    <option value="">选择作业</option>
                    {assignments.map((a) => (
                        <option key={a.id} value={a.id}>
                            {a.title}
                        </option>
                    ))}
                </select>
                {asId ? (
                    <button className={ghost} onClick={() => void release()}>
                        开放答案
                    </button>
                ) : null}
            </div>

            {dash ? (
                <section className={box}>
                    <h2 className="mb-2 font-semibold">学情分布（{dash.students} 名学生）</h2>
                    <div className="grid grid-cols-2 gap-2 text-sm md:grid-cols-5">
                        {Object.entries(DIST_LABEL).map(([k, v]) => (
                            <div key={k} className="rounded-md bg-stone-100 p-2 dark:bg-stone-800">
                                <div className="text-stone-500">{v}</div>
                                <div className="text-lg font-semibold">{dash.distribution[k] || 0}</div>
                            </div>
                        ))}
                    </div>
                    <p className="mt-2 text-sm">
                        独立自测 {dash.selfTest.independent} 人，其中正确 {dash.selfTest.independentCorrect}；待处理求助 {dash.help.open}，内容问题报告 {dash.help.openReports}
                    </p>
                    <p className="mt-1 text-xs text-stone-500">仅为统计分布，错因请对照学生作答核实。</p>
                </section>
            ) : null}

            {asId ? <HelpPanel asId={asId} scope={scope} groups={help} onDone={load} /> : null}

            {attempts.length ? (
                <section className={`${box} mt-4`}>
                    <h2 className="mb-2 font-semibold">作答记录</h2>
                    <table className="w-full text-sm">
                        <thead>
                            <tr className="text-left text-stone-500">
                                <th>学生</th>
                                <th>题</th>
                                <th>作答</th>
                                <th>自动结果</th>
                                <th>提示层</th>
                                <th>老师判定</th>
                            </tr>
                        </thead>
                        <tbody>
                            {attempts.map((a) => (
                                <tr key={a.id} className="border-t border-stone-100 dark:border-stone-800">
                                    <td>{a.studentUserId?.slice(-6)}</td>
                                    <td>{a.item === "main" ? "主题" : "自测"}</td>
                                    <td>{a.kind === "no_idea" ? "暂时不会" : a.response}</td>
                                    <td>{RESULT_LABEL[a.result] || a.result}</td>
                                    <td>{a.hintLevel}</td>
                                    <td className="space-x-1 py-1">
                                        {a.teacherResult ? <span>{RESULT_LABEL[a.teacherResult]}</span> : null}
                                        {a.valid && a.kind === "answer"
                                            ? (["correct", "partial", "incorrect"] as const).map((x) => (
                                                  <button key={x} className={ghost} onClick={() => void judge(a.id, x)}>
                                                      {RESULT_LABEL[x]}
                                                  </button>
                                              ))
                                            : null}
                                    </td>
                                </tr>
                            ))}
                        </tbody>
                    </table>
                </section>
            ) : null}
        </main>
    );
}

function HelpPanel({ asId, scope, groups, onDone }: { asId: string; scope: { workspace: string }; groups: HelpGroup[]; onDone: () => void }) {
    const [text, setText] = useState<Record<string, string>>({});
    const [pub, setPub] = useState<Record<string, boolean>>({});
    const [err, setErr] = useState("");
    const send = async (g: HelpGroup) => {
        const r = await edu("post", `/assignments/${asId}/help/reply`, scope, { groupKey: g.groupKey, body: text[g.groupKey] || "", public: !!pub[g.groupKey] });
        if (!r.ok) return setErr(r.code === "PERSONAL_INFO" ? "公开回复中疑似包含学生个人信息，请修改后再发" : r.message);
        setErr("");
        setText((t) => ({ ...t, [g.groupKey]: "" }));
        onDone();
    };
    if (!groups.length) return null;
    return (
        <section className={`${box} mt-4`}>
            <h2 className="mb-2 font-semibold">求助（按优先级）</h2>
            {err ? <p className="mb-2 text-sm text-red-600">{err}</p> : null}
            {groups.map((g) => (
                <div key={g.groupKey} className="mb-3 border-b border-stone-100 pb-3 dark:border-stone-800">
                    <p className="text-sm font-medium">
                        {KIND_LABEL[g.kind] || g.kind}
                        {g.beatId ? ` · ${g.beatId}` : ""} · 待处理 {g.open}
                    </p>
                    <ul className="ml-4 list-disc text-sm text-stone-600 dark:text-stone-300">
                        {g.requests.map((r) => (
                            <li key={r.id}>{r.message || "（无补充说明）"}</li>
                        ))}
                    </ul>
                    <div className="mt-2 flex flex-wrap items-center gap-2">
                        <input className="h-8 flex-1 rounded-md border px-2 text-sm" value={text[g.groupKey] || ""} onChange={(e) => setText((t) => ({ ...t, [g.groupKey]: e.target.value }))} placeholder="回复这一组" />
                        <label className="text-sm">
                            <input type="checkbox" checked={!!pub[g.groupKey]} onChange={(e) => setPub((p) => ({ ...p, [g.groupKey]: e.target.checked }))} /> 全班可见
                        </label>
                        <button className={btn} disabled={!text[g.groupKey]} onClick={() => void send(g)}>
                            回复
                        </button>
                    </div>
                </div>
            ))}
        </section>
    );
}
