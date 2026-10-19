import { useEffect, useState } from "react";
import { Descriptions, Tag, Typography } from "antd";
import { Callout } from "@/components/ui/product/callout/callout";

import { getMonitorPreview, type MonitorPlan } from "@/services/api/intelligence";

/** Micro-credits → credits with up to 4 decimals. */
export const credits = (micro?: number) => (micro == null ? "—" : `${(micro / 1_000_000).toLocaleString(undefined, { maximumFractionDigits: 4 })} 积分`);

const EXPANSION_LABEL: Record<string, string> = { sources: "扩大来源", budget: "提高预算", frequency: "提高频率", notification_targets: "改变发送目标", created: "新建" };

function parseArgs(raw: string): Record<string, unknown> {
    try {
        const v = JSON.parse(raw) as unknown;
        return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : { value: v };
    } catch {
        return { arguments: raw };
    }
}

const show = (v: unknown) => (Array.isArray(v) ? v.join(", ") : v && typeof v === "object" ? JSON.stringify(v) : String(v ?? "—"));

/** PRD §5.1 structured plan: sources, schedule, timezone, extraction method, budget cap, cost estimate, notification targets. */
export function MonitorPlanView({ plan }: { plan: MonitorPlan }) {
    const est = plan.cost_estimate;
    return (
        <>
            {!!plan.expansion?.length && (
                <div style={{ marginBottom: 8 }}>
                    <Callout tone="warning" title="此变更扩大了已批准的范围，需要重新决策">
                        {plan.expansion.map((x) => EXPANSION_LABEL[x] ?? x).join("、")}
                    </Callout>
                </div>
            )}
            <Descriptions size="small" column={1} bordered>
                <Descriptions.Item label="名称">{plan.name}</Descriptions.Item>
                <Descriptions.Item label="关键词">
                    {plan.keywords.map((k) => (
                        <Tag key={k}>{k}</Tag>
                    ))}
                </Descriptions.Item>
                <Descriptions.Item label="来源范围">
                    {(plan.sources ?? plan.source_ids.map((id) => ({ id, kind: "", title: id, url: "" }))).map((s) => (
                        <div key={s.id}>
                            {s.kind && <Tag>{s.kind}</Tag>}
                            {s.title || s.url || s.id}
                        </div>
                    ))}
                </Descriptions.Item>
                <Descriptions.Item label="周期">
                    每 {Math.round(plan.interval_seconds / 60)} 分钟{plan.runs_per_day != null ? `（每天约 ${plan.runs_per_day} 次采集）` : ""}
                    {plan.misfire_policy ? ` · 漏跑策略 ${plan.misfire_policy}` : ""}
                </Descriptions.Item>
                <Descriptions.Item label="时区">{plan.timezone}</Descriptions.Item>
                <Descriptions.Item label="提取方式">{plan.extraction_method ?? "—"}</Descriptions.Item>
                <Descriptions.Item label="费用上限（每窗口）">{credits(plan.budget_micro_per_window)}</Descriptions.Item>
                <Descriptions.Item label="费用估算">
                    {est ? (
                        <>
                            每窗口 {credits(est.per_window_micro)} · 每天 {credits(est.per_day_micro)} · 30 天 {credits(est.per_30d_micro)}
                            <div>
                                <Typography.Text type="secondary">
                                    价格版本 {est.price_version}；{est.basis}
                                </Typography.Text>
                            </div>
                        </>
                    ) : (
                        "—"
                    )}
                </Descriptions.Item>
                <Descriptions.Item label="通知目标">{plan.notification_target_ids?.length ? plan.notification_target_ids.join(", ") : "仅站内通知"}</Descriptions.Item>
            </Descriptions>
        </>
    );
}

/** Approval card body: monitor plans render structured; other tools render their arguments as a field list (never raw JSON). */
export function ApprovalDetails({ workspaceId, tool, rawArgs }: { workspaceId?: string; tool: string; rawArgs: string }) {
    const args = parseArgs(rawArgs);
    const previewId = tool === "intel_apply_monitor" && typeof args.preview_id === "string" ? args.preview_id : undefined;
    const [plan, setPlan] = useState<MonitorPlan>();
    const [err, setErr] = useState<string>();
    useEffect(() => {
        setPlan(undefined);
        setErr(undefined);
        if (workspaceId && previewId)
            void getMonitorPreview(workspaceId, previewId)
                .then(setPlan)
                .catch(() => setErr("无法读取监控计划预览（可能已过期）"));
    }, [workspaceId, previewId]);
    if (previewId) {
        if (plan) return <MonitorPlanView plan={plan} />;
        return <Typography.Text type={err ? "danger" : "secondary"}>{err ?? "正在读取监控计划…"}</Typography.Text>;
    }
    return (
        <Descriptions size="small" column={1} bordered>
            {Object.entries(args).map(([k, v]) => (
                <Descriptions.Item key={k} label={k}>
                    {show(v)}
                </Descriptions.Item>
            ))}
        </Descriptions>
    );
}
