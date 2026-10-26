import { useCallback, useEffect, useState } from "react";

import { edu, type EduScope } from "@/services/api/edu";

import { extensionDueISO, INVITE_STATUS_LABEL, type ExtensionRow, type InviteRow, type RosterRow } from "./edu-admin-logic";

/**
 * 教师端班级管理：建班、邀请码（明文只在创建时显示一次）、撤销、名册、退出班级、同意记录，
 * 以及单个学生的作业延期。所有权限与校验由服务端完成。
 */
const btn = "inline-flex h-8 items-center rounded-md bg-stone-900 px-3 text-sm text-white disabled:opacity-40 dark:bg-stone-100 dark:text-stone-900";
const ghost = "inline-flex h-8 items-center rounded-md border border-stone-300 px-2 text-sm disabled:opacity-40 dark:border-stone-600";

export function ClassAdmin({ scope, classId, onCreated }: { scope: EduScope; classId: string; onCreated: (id: string) => void }) {
    const [name, setName] = useState("");
    const [invites, setInvites] = useState<InviteRow[]>([]);
    const [roster, setRoster] = useState<RosterRow[]>([]);
    const [code, setCode] = useState("");
    const [ttl, setTtl] = useState(168);
    const [maxUses, setMaxUses] = useState(60);
    const [evidence, setEvidence] = useState("");
    const [msg, setMsg] = useState("");

    const reload = useCallback(() => {
        if (!classId) return;
        void edu<InviteRow[]>("get", `/classes/${classId}/invites`, scope).then((r) => r.ok && setInvites(r.data));
        void edu<RosterRow[]>("get", `/classes/${classId}/roster`, scope).then((r) => r.ok && setRoster(r.data));
    }, [classId, scope]);
    useEffect(() => {
        setCode("");
        reload();
    }, [reload]);

    const create = async () => {
        const r = await edu<{ id: string }>("post", "/classes", scope, { name: name.trim() });
        if (!r.ok) return setMsg(`${r.code}：${r.message}`);
        setName("");
        onCreated(r.data.id);
    };
    const invite = async () => {
        const r = await edu<{ code: string }>("post", `/classes/${classId}/invites`, scope, { ttlHours: ttl, maxUses });
        if (!r.ok) return setMsg(`${r.code}：${r.message}`);
        setCode(r.data.code);
        reload();
    };
    const act = async (path: string, body: unknown, ok: string) => {
        const r = await edu("post", path, scope, body);
        setMsg(r.ok ? ok : `${r.code}：${r.message}`);
        reload();
    };

    return (
        <div className="space-y-3 text-sm">
            {msg ? <p className="text-amber-700">{msg}</p> : null}
            <div className="flex gap-2">
                <input className="h-8 flex-1 rounded-md border px-2" placeholder="新班级名称" maxLength={120} value={name} onChange={(e) => setName(e.target.value)} />
                <button className={btn} disabled={!name.trim()} onClick={() => void create()}>
                    新建班级
                </button>
            </div>
            {classId ? (
                <>
                    <div>
                        <h3 className="font-medium">邀请码</h3>
                        <div className="mt-1 flex flex-wrap items-center gap-2">
                            <label>
                                有效小时 <input type="number" className="h-8 w-20 rounded-md border px-2" min={1} max={720} value={ttl} onChange={(e) => setTtl(Number(e.target.value))} />
                            </label>
                            <label>
                                最多使用 <input type="number" className="h-8 w-20 rounded-md border px-2" min={1} max={500} value={maxUses} onChange={(e) => setMaxUses(Number(e.target.value))} />
                            </label>
                            <button className={btn} onClick={() => void invite()}>
                                生成邀请码
                            </button>
                        </div>
                        {code ? (
                            <p className="mt-1 rounded-md bg-amber-50 p-2 font-mono dark:bg-amber-950" data-testid="invite-code">
                                {code}
                                <span className="ml-2 font-sans text-xs">只显示这一次，服务端只保存哈希，请现在分发</span>
                            </p>
                        ) : null}
                        <ul className="mt-1 space-y-1">
                            {invites.map((i) => (
                                <li key={i.id} className="flex flex-wrap items-center gap-2">
                                    <span>
                                        {i.id.slice(-6)}：{INVITE_STATUS_LABEL[i.status] || i.status}，已用 {i.uses}/{i.maxUses}，到期 {new Date(i.expiresAt).toLocaleString()}
                                    </span>
                                    {i.status === "active" ? (
                                        <button className={ghost} onClick={() => void act(`/classes/${classId}/invites/${i.id}/revoke`, {}, "邀请码已撤销")}>
                                            撤销
                                        </button>
                                    ) : null}
                                </li>
                            ))}
                        </ul>
                    </div>
                    <div>
                        <h3 className="font-medium">名册（{roster.filter((r) => r.status === "active").length} 名在读）</h3>
                        <input className="mt-1 h-8 w-full rounded-md border px-2" placeholder="同意凭证编号（纸质或系统记录编号，记录同意时必填建议）" value={evidence} onChange={(e) => setEvidence(e.target.value)} />
                        <ul className="mt-1 space-y-1">
                            {roster.map((s) => (
                                <li key={s.enrollmentId} className="flex flex-wrap items-center gap-2">
                                    <span>
                                        {s.studentUserId}：{s.status === "active" ? "在读" : "已退出"}，{s.consentOk ? "已记录同意" : "未记录同意（不能使用学习功能）"}
                                    </span>
                                    {s.status === "active" ? (
                                        <>
                                            <button className={ghost} onClick={() => void act("/consents", { studentUserId: s.studentUserId, status: s.consentOk ? "withdrawn" : "granted", evidenceRef: evidence }, "已记录")}>
                                                {s.consentOk ? "记录撤回同意" : "记录同意"}
                                            </button>
                                            <button className={ghost} onClick={() => confirm("让该学生退出班级？其作业访问会立即失效。") && void act(`/enrollments/${s.enrollmentId}/withdraw`, {}, "已退出班级")}>
                                                退出班级
                                            </button>
                                        </>
                                    ) : null}
                                </li>
                            ))}
                        </ul>
                    </div>
                </>
            ) : null}
        </div>
    );
}

export function ExtensionPanel({ scope, classId, assignmentId, dueAt }: { scope: EduScope; classId: string; assignmentId: string; dueAt?: string }) {
    const [rows, setRows] = useState<ExtensionRow[]>([]);
    const [roster, setRoster] = useState<RosterRow[]>([]);
    const [student, setStudent] = useState("");
    const [local, setLocal] = useState("");
    const [reason, setReason] = useState("");
    const [msg, setMsg] = useState("");
    const reload = useCallback(() => {
        void edu<ExtensionRow[]>("get", `/assignments/${assignmentId}/extensions`, scope).then((r) => r.ok && setRows(r.data));
        void edu<RosterRow[]>("get", `/classes/${classId}/roster`, scope).then((r) => r.ok && setRoster(r.data.filter((x) => x.status === "active")));
    }, [assignmentId, classId, scope]);
    useEffect(reload, [reload]);
    const save = async () => {
        const due = extensionDueISO(local, dueAt);
        if (!due.ok) return setMsg(due.error);
        const r = await edu("post", `/assignments/${assignmentId}/extensions`, scope, { studentUserId: student, dueAt: due.iso, reason });
        setMsg(r.ok ? "已设置延期" : `${r.code}：${r.message}`);
        reload();
    };
    const remove = async (sid: string) => {
        const r = await edu("post", `/assignments/${assignmentId}/extensions/${sid}/remove`, scope, {});
        setMsg(r.ok ? "已取消延期" : `${r.code}：${r.message}`);
        reload();
    };
    return (
        <div className="text-sm">
            <p className="text-xs text-stone-500">原截止 {dueAt ? new Date(dueAt).toLocaleString() : "未设置（不能延期）"}；时间按本机时区输入，服务端以 UTC 保存。</p>
            {msg ? <p className="text-amber-700">{msg}</p> : null}
            <div className="mt-1 flex flex-wrap gap-2">
                <select className="h-8 rounded-md border px-2" value={student} onChange={(e) => setStudent(e.target.value)}>
                    <option value="">选择学生</option>
                    {roster.map((s) => (
                        <option key={s.studentUserId} value={s.studentUserId}>
                            {s.studentUserId}
                        </option>
                    ))}
                </select>
                <input type="datetime-local" className="h-8 rounded-md border px-2" value={local} onChange={(e) => setLocal(e.target.value)} />
                <input className="h-8 flex-1 rounded-md border px-2" maxLength={200} placeholder="原因（可选）" value={reason} onChange={(e) => setReason(e.target.value)} />
                <button className={btn} disabled={!student || !dueAt} onClick={() => void save()}>
                    设置延期
                </button>
            </div>
            <ul className="mt-1 space-y-1">
                {rows.map((x) => (
                    <li key={x.id} className="flex items-center gap-2">
                        <span>
                            {x.studentUserId}：延至 {new Date(x.dueAt).toLocaleString()}
                            {x.reason ? `（${x.reason}）` : ""}
                        </span>
                        <button className={ghost} onClick={() => void remove(x.studentUserId)}>
                            取消
                        </button>
                    </li>
                ))}
            </ul>
        </div>
    );
}
