import { useCallback, useEffect, useState } from "react";
import { Alert, Button, Card, Collapse, List, Space, Tag, Typography, message } from "antd";

import { exportReport, getReport, listBriefSchedules, listReports, type BriefSchedule, type ReportDetail, type ReportSummary } from "@/services/api/intelligence";

const KIND_LABEL: Record<string, { color: string; label: string }> = {
    source_statement: { color: "blue", label: "source statement" },
    cross_source_verified: { color: "green", label: "cross-source verified" },
    model_inference: { color: "orange", label: "model inference" },
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
                title="Reports"
                extra={
                    <Button size="small" onClick={refresh}>
                        Refresh
                    </Button>
                }
            >
                <List
                    size="small"
                    dataSource={reports}
                    locale={{ emptyText: "No reports yet" }}
                    renderItem={(r) => (
                        <List.Item style={{ cursor: "pointer", fontWeight: r.id === detail?.id ? 600 : 400 }} onClick={() => open(r.id)}>
                            <div>
                                <Tag>{r.kind}</Tag>
                                {r.title}
                                <div>
                                    <Typography.Text type="secondary">
                                        {new Date(r.created_at).toLocaleString()} · {r.claims} claims
                                    </Typography.Text>
                                </div>
                            </div>
                        </List.Item>
                    )}
                />
            </Card>
            <Card
                size="small"
                title={detail?.title ?? "Report"}
                extra={
                    detail &&
                    workspaceId && (
                        <Space size={4}>
                            {(["md", "html", "json"] as const).map((f) => (
                                <Button key={f} size="small" onClick={() => void exportReport(workspaceId, detail.id, f).catch(() => void message.error("导出失败"))}>
                                    导出 {f.toUpperCase()}
                                </Button>
                            ))}
                        </Space>
                    )
                }
            >
                {!detail ? (
                    <Typography.Text type="secondary">Select a report</Typography.Text>
                ) : (
                    <>
                        {detail.skill?.id && (
                            <Typography.Paragraph type="secondary">
                                Skill {detail.skill.id}@{detail.skill.version} · {detail.skill.hash?.slice(0, 12)}
                            </Typography.Paragraph>
                        )}
                        {!!(cov?.source_failures?.length || cov?.truncated || cov?.conflicting_evidence?.length) && (
                            <Alert
                                type="warning"
                                style={{ marginBottom: 8 }}
                                message="Coverage gaps"
                                description={[
                                    cov?.source_failures?.length ? `Failed sources: ${cov.source_failures.map((f) => `${f.source_id} (${f.error})`).join(", ")}` : "",
                                    cov?.truncated ? "Some material was truncated by the budget or read window." : "",
                                    cov?.conflicting_evidence?.length ? `Revised documents (possible conflicts): ${cov.conflicting_evidence.map((c) => `${c.document_id}@r${c.revision}`).join(", ")}` : "",
                                ]
                                    .filter(Boolean)
                                    .join(" ")}
                            />
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
                                                {x.title || x.document_id} · rev {x.revision} · chunk {x.chunk_index}
                                            </Typography.Text>
                                            {x.truncated && <Tag color="gold">truncated</Tag>}
                                            <Typography.Paragraph style={{ whiteSpace: "pre-wrap", marginBottom: 0 }}>{x.quote}</Typography.Paragraph>
                                        </div>
                                    ))
                                ) : (
                                    <Typography.Text type="secondary">No evidence (model inference)</Typography.Text>
                                ),
                            }))}
                        />
                    </>
                )}
            </Card>
            <Card size="small" title="Brief schedules">
                <List
                    size="small"
                    dataSource={briefs}
                    locale={{ emptyText: "No brief schedules" }}
                    renderItem={(b) => (
                        <List.Item>
                            <div>
                                <div>
                                    {b.name} {!b.enabled && <Tag>paused</Tag>}
                                </div>
                                <Typography.Text type="secondary">
                                    {b.cron} ({b.timezone}) · next {new Date(b.next_run_at).toLocaleString()}
                                </Typography.Text>
                            </div>
                        </List.Item>
                    )}
                />
            </Card>
        </div>
    );
}
