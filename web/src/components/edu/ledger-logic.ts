/**
 * 机构管理后台（账本对账、预算、成本）的纯逻辑。金额一律为平台账本的微单位整数，
 * 页面只做展示与表单校验，计费、对账与限额判定全部在服务端。
 */

export type LedgerAnomaly = { id: string; jobId: string; kind: string; detail: string; status: string; owner?: string; note?: string; detectedAt: string; lastSeenAt: string };
export type BudgetLimits = { monthlyMicro: number; teacherMonthlyMicro: number; maxActiveJobs: number; maxActivePerTeacher: number; maxRenderJobs: number; alertPercent: number };
export type CostLine = { component: string; source: string; metered: boolean; costMicro: number; quantity: number; unit: string };

export const ANOMALY_LABEL: Record<string, string> = {
    stage_unbilled: "阶段有费用但未结算",
    stage_child_mismatch: "阶段费用与子预留不一致",
    duplicate_stage_charge: "同一阶段重复扣费",
    usage_mismatch: "用量记录与阶段不一致",
    root_mismatch: "根预留与子预留不一致",
    settlement_stuck: "终态作业结算未关闭",
    over_budget: "超出作业冻结预算",
    unknown_outcome_stale: "提交结果长时间未知",
    hold_after_close: "关闭后仍持有预算",
};

export const STATUS_LABEL: Record<string, string> = { open: "待处理", investigating: "处理中", resolved: "已解决" };

export const COST_LABEL: Record<string, string> = {
    compute_succeeded: "成功交付的计算",
    render: "课件渲染",
    failed_and_cancelled: "失败与取消",
    open_holds: "未关闭预留（不计成本）",
    storage: "产物存储",
    tts: "语音合成",
    egress: "下载流量",
    human_support: "人工支持",
};

export const anomalyLabel = (kind: string) => ANOMALY_LABEL[kind] || kind;

/** 微单位整数转可读金额（保留 4 位小数），负数与非数字原样标注。 */
export function formatMicro(v: number | null | undefined): string {
    if (v === null || v === undefined || !Number.isFinite(v)) return "无";
    const sign = v < 0 ? "-" : "";
    return `${sign}${(Math.abs(v) / 1e6).toFixed(4)}`;
}

/** 处理状态变更的前置条件与服务端一致：处理中需要负责人，解决需要说明。 */
export function anomalyUpdateError(status: string, owner: string, note: string): string {
    if (!["open", "investigating", "resolved"].includes(status)) return "未知状态";
    if (owner.length > 40) return "负责人不超过 40 字";
    if ([...note].length > 400) return "说明不超过 400 字";
    if (status === "investigating" && !owner.trim()) return "处理中需要指定负责人";
    if (status === "resolved" && !note.trim()) return "标记解决需要填写处理说明";
    return "";
}

export type BudgetForm = Record<keyof BudgetLimits, string>;

/** 表单转限额：空白为 0（不限），负数、小数、超范围都拒绝，绝不静默修正。 */
export function parseBudgetForm(f: BudgetForm): { ok: true; value: BudgetLimits } | { ok: false; error: string } {
    const out = {} as BudgetLimits;
    for (const k of Object.keys(f) as (keyof BudgetLimits)[]) {
        const raw = (f[k] ?? "").trim();
        if (raw === "") {
            out[k] = 0;
            continue;
        }
        if (!/^\d+$/.test(raw)) return { ok: false, error: `${k} 必须是非负整数` };
        const n = Number(raw);
        if (!Number.isSafeInteger(n)) return { ok: false, error: `${k} 超出范围` };
        out[k] = n;
    }
    if (out.alertPercent === 0) out.alertPercent = 80;
    if (out.alertPercent > 100) return { ok: false, error: "提醒比例为 1 至 100" };
    if (out.maxActiveJobs > 10000) return { ok: false, error: "机构运行中上限不超过 10000" };
    return { ok: true, value: out };
}

export function budgetFormOf(b: Partial<BudgetLimits> | null | undefined): BudgetForm {
    const s = (v?: number) => (v ? String(v) : "");
    return {
        monthlyMicro: s(b?.monthlyMicro),
        teacherMonthlyMicro: s(b?.teacherMonthlyMicro),
        maxActiveJobs: s(b?.maxActiveJobs),
        maxActivePerTeacher: s(b?.maxActivePerTeacher),
        maxRenderJobs: s(b?.maxRenderJobs),
        alertPercent: s(b?.alertPercent || 80),
    };
}

/** 预算使用率；不限时返回 null，不把“不限”显示为 0%。 */
export function usagePercent(committed: number, limit: number): number | null {
    if (!limit) return null;
    return Math.round((committed * 1000) / limit) / 10;
}

/** 成本分项展示：未计量项明确写“未计量”，不显示为 0 成本。 */
export function costLineText(l: CostLine): string {
    if (!l.metered) return l.quantity > 0 ? `未计量（${l.quantity} ${l.unit}）` : "未计量";
    if (l.component === "open_holds") return `持有 ${formatMicro(l.quantity)}`;
    return formatMicro(l.costMicro);
}

/** 当前自然月 YYYY-MM（按给定时区，默认与服务端一致的 Asia/Shanghai）。 */
export function currentPeriod(now = new Date(), timeZone = "Asia/Shanghai"): string {
    const parts = new Intl.DateTimeFormat("en-CA", { timeZone, year: "numeric", month: "2-digit" }).formatToParts(now);
    const y = parts.find((p) => p.type === "year")?.value;
    const m = parts.find((p) => p.type === "month")?.value;
    return `${y}-${m}`;
}
