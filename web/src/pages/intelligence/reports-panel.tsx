import { useCallback, useEffect, useState } from "react";
import { Button, Card, Collapse, List, Space, Tag, Typography, message } from "antd";
import { Callout } from "@/components/ui/product/callout/callout";

import { exportReport, getReport, listBriefSchedules, listReports, type BriefSchedule, type ReportDetail, type ReportSummary } from "@/services/api/intelligence";

const KIND_LABEL: Record<string, { color: string; label: string }> = {
    source_statement: { color: "blue", label: "信源陈述" },
    cross_source_verified: { color: "green", label: "跨源印证" },
    model_inference: { color: "orange", label: "模型推断" },
};

/** P3: reports list/detail with expandable evidence per claim, and brief schedules. */
export function ReportsPanel({ workspaceId }: { workspaceId?: string }) {
    const [reports, setReports] = useState<ReportSummary[]>([]);
    const [briefs, setBriefs] = useState<BriefSchedule[]>([]);
    const [detail, setDetail] = useState<ReportDetail>();

    const refresh = useCallback(() => {
        if (!workspaceId) return;
        void listReports(workspaceId)
            .then(setReports)
            .catch(() => setReports([]));
        void listBriefSchedules(workspaceId)
            .then(setBriefs)
            .catch(() => setBriefs([]));
    }, [workspaceId]);
    useEffect(refresh, [refresh]);

    const open = (id: string) => {
        if (workspaceId) void getReport(workspaceId, id).then(setDetail);
    };
    const cov = detail?.coverage;

    return (
        <div style={{ display: "grid", gridTemplateColumns: "320px 1fr 320px", gap: 16, padding: "0 16px 16px" }}>
            <Card
                size="small"
                title="报告列表"
                extra={
                    <Button size="small" onClick={refresh}>
                        刷新
                    </Button>
                }
            >
                <List
                    size="small"
                    dataSource={reports}
                    locale={{ emptyText: "暂无报告" }}
                    renderItem={(r) => (
                        <List.Item style={{ cursor: "pointer", fontWeight: r.id === detail?.id ? 600 : 400 }} onClick={() => open(r.id)}>
                            <div>
                                <Tag>{r.kind === "brief" ? "简报" : "报告"}</Tag>
                                {r.title}
                                <div>
                                    <Typography.Text type="secondary">
                                        {new Date(r.created_at).toLocaleString()} · {r.claims} 条论断
                                    </Typography.Text>
                                </div>
                            </div>
                        </List.Item>
                    )}
                />
            </Card>
            <Card
                size="small"
                title={detail?.title ?? "报告详情"}
                extra={
                    detail &&
                    workspaceId && (
                        <Space size={4} wrap>
                            {(["md", "html", "json", "pdf", "docx"] as const).map((f) => (
                                <Button key={f} size="small" onClick={() => void exportReport(workspaceId, detail.id, f).catch(() => void message.error("导出失败"))}>
                                    导出 {f.toUpperCase()}
                                </Button>
                            ))}
                        </Space>
                    )
                }
            >
                {!detail ? (
                    <Typography.Text type="secondary">请选择一份报告查看详情与证据链</Typography.Text>
                ) : (
                    <>
                        {detail.skill?.id && (
                            <Typography.Paragraph type="secondary">
                                技能 {detail.skill.id}@{detail.skill.version} · {detail.skill.hash?.slice(0, 12)}
                            </Typography.Paragraph>
                        )}
                        {!!(cov?.source_failures?.length || cov?.truncated || cov?.conflicting_evidence?.length) && (
                            <div style={{ marginBottom: 8 }}>
                                <Callout tone="warning" title="覆盖范围缺口提醒">
                                    {[
                                        cov?.source_failures?.length ? `采集失败信源：${cov.source_failures.map((f) => `${f.source_id} (${f.error})`).join("、")}` : "",
                                        cov?.truncated ? "部分材料因预算或读取窗口上限被截断。" : "",
                                        cov?.conflicting_evidence?.length ? `已修订文档（可能存在冲突证据）：${cov.conflicting_evidence.map((c) => `${c.document_id}@r${c.revision}`).join("、")}` : "",
                                    ]
                                        .filter(Boolean)
                                        .join(" ")}
                                </Callout>
                            </div>
                        )}
                        <Typography.Paragraph style={{ whiteSpace: "pre-wrap" }}>{detail.body}</Typography.Paragraph>
                        <Collapse
                            size="small"
                            items={detail.claims.map((c) => ({
                                key: String(c.idx),
                                label: (
                                    <span>
                                        <Tag color={KIND_LABEL[c.kind]?.color}>{KIND_LABEL[c.kind]?.label ?? c.kind}</Tag>
                                        {c.text}
                                    </span>
                                ),
                                children: c.citations.length ? (
                                    c.citations.map((x) => (
                                        <div key={x.evidence_id} style={{ marginBottom: 8 }}>
                                            <Typography.Text strong>
                                                {x.title || x.document_id} · 版本 r{x.revision} · 片段 #{x.chunk_index}
                                            </Typography.Text>
                                            {x.truncated && <Tag color="gold">已截断</Tag>}
                                            <Typography.Paragraph style={{ whiteSpace: "pre-wrap", marginBottom: 0 }}>{x.quote}</Typography.Paragraph>
                                        </div>
                                    ))
                                ) : (
                                    <Typography.Text type="secondary">无直接引用证据（模型推断）</Typography.Text>
                                ),
                            }))}
                        />
                    </>
                )}
            </Card>
            <Card size="small" title="定时简报计划">
                <List
                    size="small"
                    dataSource={briefs}
                    locale={{ emptyText: "暂无定时简报计划" }}
                    renderItem={(b) => (
                        <List.Item>
                            <div>
                                <div>
                                    {b.name} {!b.enabled && <Tag>已暂停</Tag>}
                                </div>
                                <Typography.Text type="secondary">
                                    {b.cron} ({b.timezone}) · 下次运行 {new Date(b.next_run_at).toLocaleString()}
                                </Typography.Text>
                            </div>
                        </List.Item>
                    )}
                />
            </Card>
        </div>
    );
}
