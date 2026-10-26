import { useCallback, useEffect, useMemo, useState } from "react";

import {
    anomalyLabel,
    anomalyUpdateError,
    budgetFormOf,
    COST_LABEL,
    costLineText,
    currentPeriod,
    formatMicro,
    parseBudgetForm,
    STATUS_LABEL,
    usagePercent,
    type BudgetForm,
    type BudgetLimits,
    type CostLine,
    type LedgerAnomaly,
} from "@/components/edu/ledger-logic";
import { edu } from "@/services/api/edu";
import { listIntelWorkspaces, type IntelWorkspace } from "@/services/api/intelligence";
import { apiClient } from "@/services/api/request";

/**
 * 机构管理后台：账本对账与审计导出、对账发现的负责人与处理状态、机构预算与公平限流、
 * 周期交付成本。只有租户所有者、管理员或 org_admin 能打开；非管理员由服务端返回 403。
 */
const box = "rounded-lg border border-stone-200 p-4 dark:border-stone-700";
const btn = "inline-flex h-8 items-center rounded-md bg-stone-900 px-3 text-sm text-white disabled:opacity-40 dark:bg-stone-100 dark:text-stone-900";
const ghost = "inline-flex h-8 items-center rounded-md border border-stone-300 px-2 text-sm disabled:opacity-40 dark:border-stone-600";
const input = "h-8 rounded-md border border-stone-300 bg-transparent px-2 text-sm dark:border-stone-600";

type BudgetView = {
    budget: BudgetLimits;
    period: string;
    timezone: string;
    organizationCommittedMicro: number;
    myCommittedMicro: number;
    activeJobs: number;
    activeRenderJobs: number;
    alerts: { id: string; scope: string; level: string; spentMicro: number; limitMicro: number; createdAt: string }[];
};
type CostReport = { period: string; lines: CostLine[]; totalMicro: number; acceptedLessons: number; activeClasses: number; perAcceptedLessonMicro: number | null; perActiveClassMicro: number | null; supportUnattributed: number };

const BUDGET_FIELDS: { key: keyof BudgetLimits; label: string }[] = [
    { key: "monthlyMicro", label: "机构月预算（微单位，空为不限）" },
    { key: "teacherMonthlyMicro", label: "每位教师月预算" },
    { key: "maxActiveJobs", label: "机构同时运行作业上限" },
    { key: "maxActivePerTeacher", label: "每位教师同时运行上限" },
    { key: "maxRenderJobs", label: "课件渲染并发上限" },
    { key: "alertPercent", label: "提醒比例（%）" },
];

export default function EduAdmin() {
    const [workspaces, setWorkspaces] = useState<IntelWorkspace[]>([]);
    const [ws, setWs] = useState("");
    const scope = useMemo(() => ({ workspace: ws }), [ws]);
    const [msg, setMsg] = useState("");
    const [anoms, setAnoms] = useState<LedgerAnomaly[]>([]);
    const [edits, setEdits] = useState<Record<string, { owner: string; note: string }>>({});
    const [budget, setBudget] = useState<BudgetView | null>(null);
    const [form, setForm] = useState<BudgetForm>(budgetFormOf(null));
    const [reason, setReason] = useState("");
    const [period, setPeriod] = useState(currentPeriod());
    const [cost, setCost] = useState<CostReport | null>(null);
    const [denied, setDenied] = useState(false);

    useEffect(() => {
        void listIntelWorkspaces().then((l) => {
            setWorkspaces(l);
            setWs(l[0]?.id || "");
        });
    }, []);

    const load = useCallback(() => {
        if (!ws) return;
        void edu<LedgerAnomaly[]>("get", "/ledger/anomalies", scope).then((r) => {
            if (!r.ok) {
                setDenied(r.status === 403);
                setMsg(r.message);
                return;
            }
            setDenied(false);
            setAnoms(r.data);
        });
        void edu<BudgetView>("get", "/budget", scope).then((r) => {
            if (r.ok) {
                setBudget(r.data);
                setForm(budgetFormOf(r.data.budget));
            }
        });
    }, [ws, scope]);
    useEffect(load, [load]);
    useEffect(() => {
        if (!ws || !/^\d{4}-\d{2}$/.test(period)) return;
        void edu<CostReport>("get", `/cost-report?period=${period}`, scope).then((r) => setCost(r.ok ? r.data : null));
    }, [ws, scope, period]);

    const reconcile = async () => {
        const r = await edu<{ jobs: number; findings: number }>("post", "/ledger/reconcile", scope, {});
        setMsg(r.ok ? `已对账 ${r.data.jobs} 个作业，发现 ${r.data.findings} 项` : r.message);
        load();
    };
    const exportCSV = async () => {
        const res = await apiClient.request({ method: "get", url: "/edu/ledger/report?format=csv", headers: { "X-Workspace-Id": ws }, responseType: "text", validateStatus: () => true });
        if (res.status !== 200) {
            setMsg(`导出失败（${res.status}）`);
            return;
        }
        const url = URL.createObjectURL(new Blob([res.data as string], { type: "text/csv;charset=utf-8" }));
        const a = document.createElement("a");
        a.href = url;
        a.download = `edu-ledger-${new Date().toISOString().slice(0, 10)}.csv`;
        a.click();
        URL.revokeObjectURL(url);
    };
    const updateAnomaly = async (row: LedgerAnomaly, status: string) => {
        const e = edits[row.id] || { owner: row.owner || "", note: "" };
        const err = anomalyUpdateError(status, e.owner, e.note);
        if (err) {
            setMsg(err);
            return;
        }
        const r = await edu("post", `/ledger/anomalies/${row.id}`, scope, { status, owner: e.owner, note: e.note });
        setMsg(r.ok ? `已更新为${STATUS_LABEL[status]}` : r.message);
        load();
    };
    const saveBudget = async () => {
        const p = parseBudgetForm(form);
        if (!p.ok) {
            setMsg(p.error);
            return;
        }
        const r = await edu("put", "/budget", scope, { ...p.value, reason });
        setMsg(r.ok ? "限额已保存并写入审计" : r.message);
        load();
    };

    return (
        <div className="mx-auto flex max-w-5xl flex-col gap-4 p-6">
            <div className="flex items-center gap-3">
                <h1 className="text-lg font-semibold">机构管理：账本、预算与成本</h1>
                <select className={input} value={ws} onChange={(e) => setWs(e.target.value)} aria-label="工作区">
                    {workspaces.map((w) => (
                        <option key={w.id} value={w.id}>
                            {w.name}
                        </option>
                    ))}
                </select>
            </div>
            {msg && (
                <p role="status" className="text-sm text-stone-600 dark:text-stone-300">
                    {msg}
                </p>
            )}
            {denied ? (
                <p className={box}>只有机构所有者、管理员或教育管理员可以查看账本与成本。</p>
            ) : (
                <>
                    <section className={box} aria-label="账本对账">
                        <div className="mb-3 flex items-center gap-2">
                            <h2 className="font-medium">账本对账</h2>
                            <button className={btn} onClick={() => void reconcile()}>
                                立即对账
                            </button>
                            <button className={ghost} onClick={() => void exportCSV()}>
                                导出审计 CSV（最近 31 天）
                            </button>
                            <span className="text-xs text-stone-500">系统每天自动对账一次</span>
                        </div>
                        {anoms.length === 0 ? (
                            <p className="text-sm text-stone-500">没有未解决的对账发现。</p>
                        ) : (
                            <table className="w-full text-sm">
                                <thead>
                                    <tr className="text-left text-stone-500">
                                        <th>发现</th>
                                        <th>作业</th>
                                        <th>状态</th>
                                        <th>负责人</th>
                                        <th>处理说明</th>
                                        <th />
                                    </tr>
                                </thead>
                                <tbody>
                                    {anoms.map((a) => {
                                        const e = edits[a.id] || { owner: a.owner || "", note: "" };
                                        const set = (p: Partial<typeof e>) => setEdits({ ...edits, [a.id]: { ...e, ...p } });
                                        return (
                                            <tr key={a.id} className="border-t border-stone-100 align-top dark:border-stone-800">
                                                <td title={a.detail}>{anomalyLabel(a.kind)}</td>
                                                <td className="font-mono text-xs">{a.jobId}</td>
                                                <td>{STATUS_LABEL[a.status] || a.status}</td>
                                                <td>
                                                    <input className={input} value={e.owner} maxLength={40} onChange={(x) => set({ owner: x.target.value })} aria-label="负责人" />
                                                </td>
                                                <td>
                                                    <input className={input} value={e.note} maxLength={400} onChange={(x) => set({ note: x.target.value })} aria-label="处理说明" />
                                                </td>
                                                <td className="flex gap-1">
                                                    {a.status !== "investigating" && (
                                                        <button className={ghost} onClick={() => void updateAnomaly(a, "investigating")}>
                                                            处理中
                                                        </button>
                                                    )}
                                                    {a.status !== "resolved" && (
                                                        <button className={ghost} onClick={() => void updateAnomaly(a, "resolved")}>
                                                            已解决
                                                        </button>
                                                    )}
                                                </td>
                                            </tr>
                                        );
                                    })}
                                </tbody>
                            </table>
                        )}
                    </section>

                    <section className={box} aria-label="预算与限流">
                        <h2 className="mb-2 font-medium">预算与公平限流</h2>
                        {budget && (
                            <p className="mb-3 text-sm text-stone-600 dark:text-stone-300">
                                {budget.period}（{budget.timezone}）机构已承诺 {formatMicro(budget.organizationCommittedMicro)}
                                {usagePercent(budget.organizationCommittedMicro, budget.budget.monthlyMicro) !== null && `，占月预算 ${usagePercent(budget.organizationCommittedMicro, budget.budget.monthlyMicro)}%`}
                                ；运行中 {budget.activeJobs} 个，其中渲染 {budget.activeRenderJobs} 个。预算不足只阻止新的付费生成，已有课件照常可读。
                            </p>
                        )}
                        <div className="grid grid-cols-2 gap-2">
                            {BUDGET_FIELDS.map((f) => (
                                <label key={f.key} className="flex flex-col text-sm">
                                    {f.label}
                                    <input className={input} inputMode="numeric" value={form[f.key]} onChange={(e) => setForm({ ...form, [f.key]: e.target.value })} />
                                </label>
                            ))}
                            <label className="col-span-2 flex flex-col text-sm">
                                变更原因（写入审计）
                                <input className={input} value={reason} maxLength={200} onChange={(e) => setReason(e.target.value)} />
                            </label>
                        </div>
                        <button className={`${btn} mt-3`} onClick={() => void saveBudget()}>
                            保存限额
                        </button>
                        {budget && budget.alerts.length > 0 && (
                            <ul className="mt-3 text-sm">
                                {budget.alerts.map((a) => (
                                    <li key={a.id}>
                                        {a.level === "exhausted" ? "已耗尽" : "接近上限"}：{a.scope === "organization" ? "机构" : a.scope.replace("teacher:", "教师 ")}，{formatMicro(a.spentMicro)} / {formatMicro(a.limitMicro)}
                                    </li>
                                ))}
                            </ul>
                        )}
                    </section>

                    <section className={box} aria-label="交付成本">
                        <div className="mb-2 flex items-center gap-2">
                            <h2 className="font-medium">交付成本</h2>
                            <input className={input} value={period} onChange={(e) => setPeriod(e.target.value)} aria-label="周期" placeholder="YYYY-MM" />
                        </div>
                        {cost ? (
                            <>
                                <table className="w-full text-sm">
                                    <tbody>
                                        {cost.lines.map((l) => (
                                            <tr key={l.component} className="border-t border-stone-100 dark:border-stone-800">
                                                <td>{COST_LABEL[l.component] || l.component}</td>
                                                <td className="text-right font-mono">{costLineText(l)}</td>
                                                <td className="pl-3 text-xs text-stone-500">{l.source}</td>
                                            </tr>
                                        ))}
                                    </tbody>
                                </table>
                                <p className="mt-2 text-sm">
                                    合计 {formatMicro(cost.totalMicro)}；接受课件 {cost.acceptedLessons} 份，每份 {formatMicro(cost.perAcceptedLessonMicro)}；活跃班级 {cost.activeClasses} 个，每班 {formatMicro(cost.perActiveClassMicro)}
                                    {cost.supportUnattributed > 0 && `；${cost.supportUnattributed} 条人工支持未归属`}
                                </p>
                            </>
                        ) : (
                            <p className="text-sm text-stone-500">暂无该周期数据。</p>
                        )}
                    </section>
                </>
            )}
        </div>
    );
}
