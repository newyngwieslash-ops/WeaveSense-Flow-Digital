import { useCallback, useEffect, useState } from "react";
import { Button, Card, Input, List, Select, Space, Table, Tag, Typography, message } from "antd";

import { EmptyState } from "@/components/ui/product/empty-state/empty-state";
import {
    addTerm,
    deleteTerm,
    installSkills,
    listDocuments,
    listJobs,
    listMonitors,
    listReports,
    listSchedules,
    listSkills,
    listSourcesHealth,
    listTerms,
    type CollectionJob,
    type CollectionSchedule,
    type IntelMonitor,
    type LibraryDocument,
    type ReportSummary,
    type SkillInstall,
    type SkillPack,
    type SourceHealth,
    type TermAlias,
} from "@/services/api/intelligence";

import { classifyEmpty, EMPTY_REASON_TEXT } from "./empty-reason";

const fmt = (t?: string | null) => (t ? new Date(t).toLocaleString() : "—");
const JOB_COLOR: Record<string, string> = { succeeded: "green", partial: "gold", failed: "red", cancelled: "default", retry_wait: "orange", pending: "blue", leased: "blue", running: "processing" };

function useWorkspaceData<T>(workspaceId: string | undefined, load: (ws: string) => Promise<T>, empty: T) {
    const [data, setData] = useState<T>(empty);
    const refresh = useCallback(() => {
        if (!workspaceId) return;
        void load(workspaceId)
            .then(setData)
            .catch(() => setData(empty));
        // eslint-disable-next-line react-hooks/exhaustive-deps
    }, [workspaceId]);
    useEffect(refresh, [refresh]);
    return [data, refresh] as const;
}

/** PRD §5.1 总览: latest materials, next runs, failing sources, budget/blocked schedules, reports; empty list says why. */
export function OverviewView({ workspaceId }: { workspaceId?: string }) {
    const [sources] = useWorkspaceData<SourceHealth[]>(workspaceId, listSourcesHealth, []);
    const [jobs] = useWorkspaceData<CollectionJob[]>(workspaceId, (ws) => listJobs(ws), []);
    const [schedules] = useWorkspaceData<CollectionSchedule[]>(workspaceId, listSchedules, []);
    const [docs] = useWorkspaceData<LibraryDocument[]>(workspaceId, (ws) => listDocuments(ws), []);
    const [reports] = useWorkspaceData<ReportSummary[]>(workspaceId, listReports, []);
    const reason = EMPTY_REASON_TEXT[classifyEmpty({ sources, jobs, schedules })];
    const failing = sources.filter((s) => s.health === "backoff" || s.health === "degraded" || s.status === "paused");
    const next = schedules.filter((s) => s.enabled).sort((a, b) => a.next_run_at.localeCompare(b.next_run_at))[0];
    return (
        <div style={{ display: "grid", gridTemplateColumns: "2fr 1fr", gap: 16, padding: 16 }}>
            <Card size="small" title="最新材料">
                {docs.length === 0 ? <EmptyState title={reason.title} description={reason.detail} /> : <List size="small" dataSource={docs.slice(0, 10)} renderItem={(d) => <List.Item>{d.title || d.url}</List.Item>} />}
            </Card>
            <Space direction="vertical" style={{ width: "100%" }}>
                <Card size="small" title="下次执行">
                    {next ? `${fmt(next.next_run_at)} · ${next.kind === "cron" ? next.cron : `每 ${Math.round((next.interval_seconds ?? 0) / 60)} 分钟`}` : "没有启用的计划"}
                </Card>
                <Card size="small" title="失败 / 暂停的来源">
                    {failing.length === 0
                        ? "无"
                        : failing.map((s) => (
                              <div key={s.id}>
                                  <Tag color="red">{s.health}</Tag>
                                  {s.title || s.url}
                              </div>
                          ))}
                </Card>
                <Card size="small" title="受阻计划 / 预算">
                    {schedules.filter((s) => s.state === "blocked").length === 0
                        ? "无"
                        : schedules
                              .filter((s) => s.state === "blocked")
                              .map((s) => (
                                  <div key={s.id}>
                                      <Tag color="orange">blocked</Tag>
                                      {s.blocked_reason}
                                  </div>
                              ))}
                </Card>
                <Card size="small" title="最近报告">
                    {reports.length === 0 ? "暂无报告" : reports.slice(0, 5).map((r) => <div key={r.id}>{r.title}</div>)}
                </Card>
            </Space>
        </div>
    );
}

/** PRD §5.1 关键词: monitor keywords plus the term dictionary (built-in groups + workspace aliases). */
export function KeywordsView({ workspaceId }: { workspaceId?: string }) {
    const [monitors] = useWorkspaceData<IntelMonitor[]>(workspaceId, listMonitors, []);
    const [terms, refresh] = useWorkspaceData(workspaceId, listTerms, { builtin_version: "", builtin_groups: 0, builtin: [] as string[][], custom: [] as TermAlias[] });
    const [term, setTerm] = useState("");
    const [alias, setAlias] = useState("");
    const add = async () => {
        if (!workspaceId || !term.trim() || !alias.trim()) return;
        try {
            await addTerm(workspaceId, term.trim(), alias.trim());
            setTerm("");
            setAlias("");
            refresh();
        } catch (e) {
            void message.error(e instanceof Error ? e.message : "添加失败");
        }
    };
    return (
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 16, padding: 16 }}>
            <Card size="small" title="监控关键词">
                <List
                    size="small"
                    dataSource={monitors}
                    locale={{ emptyText: "还没有监控；在对话里让 Agent 创建" }}
                    renderItem={(m) => (
                        <List.Item>
                            <div>
                                <div>{m.name}</div>
                                {m.keywords.map((k) => (
                                    <Tag key={k}>{k}</Tag>
                                ))}
                            </div>
                        </List.Item>
                    )}
                />
            </Card>
            <Card size="small" title={`术语与别名（内置 ${terms.builtin_groups} 组 · ${terms.builtin_version}）`}>
                <Space.Compact style={{ width: "100%", marginBottom: 8 }}>
                    <Input value={term} onChange={(e) => setTerm(e.target.value)} placeholder="术语" />
                    <Input value={alias} onChange={(e) => setAlias(e.target.value)} placeholder="别名" onPressEnter={() => void add()} />
                    <Button type="primary" onClick={() => void add()}>
                        添加
                    </Button>
                </Space.Compact>
                <List
                    size="small"
                    dataSource={terms.custom}
                    locale={{ emptyText: "没有自定义别名" }}
                    renderItem={(t) => (
                        <List.Item
                            actions={[
                                <Button key="del" size="small" danger onClick={() => workspaceId && void deleteTerm(workspaceId, t.term, t.alias).then(refresh)}>
                                    删除
                                </Button>,
                            ]}
                        >
                            {t.term} ⇄ {t.alias}
                        </List.Item>
                    )}
                />
            </Card>
        </div>
    );
}

/** PRD §5.1 内容库: collected documents with published / event / collected times, searchable. */
export function LibraryView({ workspaceId }: { workspaceId?: string }) {
    const [q, setQ] = useState("");
    const [docs, setDocs] = useState<LibraryDocument[]>([]);
    const [sources] = useWorkspaceData<SourceHealth[]>(workspaceId, listSourcesHealth, []);
    const [jobs] = useWorkspaceData<CollectionJob[]>(workspaceId, (ws) => listJobs(ws), []);
    const load = useCallback(() => {
        if (workspaceId)
            void listDocuments(workspaceId, q || undefined)
                .then(setDocs)
                .catch(() => setDocs([]));
    }, [workspaceId, q]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
    useEffect(load, [workspaceId]);
    const reason = EMPTY_REASON_TEXT[classifyEmpty({ sources, jobs })];
    return (
        <Card size="small" style={{ margin: 16 }} title="内容库">
            <Input.Search value={q} onChange={(e) => setQ(e.target.value)} onSearch={load} placeholder="按标题筛选" style={{ marginBottom: 8 }} />
            {docs.length === 0 && !q ? (
                <EmptyState title={reason.title} description={reason.detail} />
            ) : (
                <Table
                    size="small"
                    rowKey="id"
                    dataSource={docs}
                    pagination={false}
                    columns={[
                        { title: "标题", dataIndex: "title", render: (t: string, d: LibraryDocument) => <a href={d.url}>{t || d.url}</a> },
                        { title: "发布时间", dataIndex: "published_at", render: fmt },
                        { title: "事件时间", dataIndex: "event_occurred_at", render: fmt },
                        { title: "采集时间", dataIndex: "collected_at", render: fmt },
                        { title: "修订", dataIndex: "current_revision" },
                    ]}
                />
            )}
        </Card>
    );
}

/** PRD §5.1 任务: collection jobs (PRD job states) and schedules (incl. blocked reason, window budget). */
export function JobsView({ workspaceId }: { workspaceId?: string }) {
    const [status, setStatus] = useState<string>();
    const [jobs, refresh] = useWorkspaceData<CollectionJob[]>(workspaceId, (ws) => listJobs(ws, status), []);
    const [schedules] = useWorkspaceData<CollectionSchedule[]>(workspaceId, listSchedules, []);
    // eslint-disable-next-line react-hooks/exhaustive-deps
    useEffect(refresh, [status]);
    return (
        <div style={{ display: "grid", gap: 16, padding: 16 }}>
            <Card
                size="small"
                title="采集任务"
                extra={
                    <Select
                        allowClear
                        placeholder="状态"
                        style={{ width: 160 }}
                        value={status}
                        onChange={setStatus}
                        options={["queued", "running", "retry_wait", "succeeded", "partial", "failed", "cancelled", "reconciliation_required"].map((v) => ({ value: v, label: v }))}
                    />
                }
            >
                <Table
                    size="small"
                    rowKey="job_id"
                    dataSource={jobs}
                    pagination={false}
                    locale={{ emptyText: "没有采集任务" }}
                    columns={[
                        { title: "信源", render: (_: unknown, j: CollectionJob) => j.source_title || j.source_id },
                        { title: "状态", dataIndex: "job_state", render: (s: string) => <Tag color={JOB_COLOR[s]}>{s}</Tag> },
                        { title: "新增/修订", render: (_: unknown, j: CollectionJob) => `${j.items_new}/${j.items_revised}` },
                        { title: "计划窗口", dataIndex: "scheduled_at", render: fmt },
                        { title: "原因", render: (_: unknown, j: CollectionJob) => j.error || j.partial_reason || "" },
                        { title: "创建", dataIndex: "created_at", render: fmt },
                    ]}
                />
            </Card>
            <Card size="small" title="采集计划">
                <List
                    size="small"
                    dataSource={schedules}
                    locale={{ emptyText: "没有计划" }}
                    renderItem={(s) => (
                        <List.Item>
                            <div>
                                <Tag color={s.state === "blocked" ? "orange" : s.enabled ? "green" : "default"}>{s.state ?? (s.enabled ? "active" : "disabled")}</Tag>
                                {s.kind === "cron" ? s.cron : `每 ${Math.round((s.interval_seconds ?? 0) / 60)} 分钟`} · {s.timezone} · 下次 {fmt(s.next_run_at)}
                                {s.blocked_reason && <Typography.Text type="warning"> · {s.blocked_reason}</Typography.Text>}
                                {!!s.budget_micro_per_window && <Typography.Text type="secondary"> · 每窗预算 {s.budget_micro_per_window}</Typography.Text>}
                            </div>
                        </List.Item>
                    )}
                />
            </Card>
        </div>
    );
}

/** PRD §5.1 行业包: packs with their skills, source templates and install state. */
export function PacksView({ workspaceId }: { workspaceId?: string }) {
    const [data, refresh] = useWorkspaceData(workspaceId, listSkills, { packs: [] as SkillPack[], installed: [] as SkillInstall[] });
    const installed = new Set(data.installed.map((i) => i.packId));
    return (
        <Card size="small" style={{ margin: 16 }} title="行业包">
            <List
                dataSource={data.packs}
                locale={{ emptyText: "没有可用的行业包" }}
                renderItem={(p) => (
                    <List.Item
                        actions={[
                            <Button key="install" size="small" disabled={installed.has(p.id)} onClick={() => workspaceId && void installSkills(workspaceId, { packId: p.id }).then(refresh)}>
                                {installed.has(p.id) ? "已安装" : "安装"}
                            </Button>,
                        ]}
                    >
                        <div>
                            <div>
                                <b>{p.name}</b> <Tag>{p.kind ?? "pack"}</Tag> <Typography.Text type="secondary">v{p.version}</Typography.Text>
                            </div>
                            {p.skills.map((s) => (
                                <Tag key={s.id}>{s.name}</Tag>
                            ))}
                        </div>
                    </List.Item>
                )}
            />
        </Card>
    );
}
