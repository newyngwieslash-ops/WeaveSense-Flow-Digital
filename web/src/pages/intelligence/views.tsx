import { useCallback, useEffect, useState } from "react";
import { Button, Card, Input, List, Select, Space, Table, Tag, Tooltip, Typography, message } from "antd";

import { EmptyState } from "@/components/ui/product/empty-state/empty-state";
import {
    addTerm,
    deleteTerm,
    downloadMedia,
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
    type ExtractionCompleteness,
    type IntelMonitor,
    type LibraryDocument,
    type ReportSummary,
    type SkillInstall,
    type SkillPack,
    type SourceHealth,
    type TermAlias,
} from "@/services/api/intelligence";

import { classifyEmpty, EMPTY_REASON_TEXT } from "./empty-reason";
import { credits } from "./plan-card";
import { buildDocumentHandoffDraft, CanvasHandoffModal, type CanvasHandoffDraft } from "./canvas-handoff-modal";

const fmt = (t?: string | null) => (t ? new Date(t).toLocaleString() : "—");
/** Jobs still needing work or attention (PRD §5 总览 待处理任务). */
const PENDING_STATES = new Set(["queued", "pending", "leased", "running", "retry_wait", "cancel_requested", "reconciliation_required"]);
export const EXTRACTION_LABEL: Record<string, { color: string; text: string }> = {
    full_text: { color: "green", text: "全文" },
    feed_summary: { color: "gold", text: "摘要" },
    title_only: { color: "red", text: "仅标题" },
};
function ExtractionTag({ e }: { e?: ExtractionCompleteness }) {
    if (!e) return <>—</>;
    const l = EXTRACTION_LABEL[e.level] ?? { color: "default", text: e.level };
    return (
        <Tooltip title={`方式 ${e.method} · ${e.body_chars} 字 · 附件 ${e.media_stored}/${e.media_total}`}>
            <Tag color={l.color}>{l.text}</Tag>
            {e.media_missing > 0 && <Tag color="orange">缺附件 {e.media_missing}</Tag>}
        </Tooltip>
    );
}
const JOB_COLOR: Record<string, string> = {
    succeeded: "green",
    partial: "gold",
    failed: "red",
    cancelled: "default",
    cancel_requested: "volcano",
    retry_wait: "orange",
    reconciliation_required: "magenta",
    pending: "blue",
    queued: "blue",
    leased: "blue",
    running: "processing",
};
export const JOB_LABEL: Record<string, string> = {
    queued: "排队中",
    pending: "待处理",
    leased: "已领取",
    running: "运行中",
    retry_wait: "等待重试",
    succeeded: "已完成",
    partial: "部分完成",
    failed: "失败",
    cancel_requested: "取消中",
    cancelled: "已取消",
    reconciliation_required: "待对账",
};
export const HEALTH_LABEL: Record<string, string> = {
    healthy: "正常",
    backoff: "退避重试",
    degraded: "降级",
    paused: "已暂停",
    never_run: "未运行",
};
export const SCHEDULE_STATE_LABEL: Record<string, string> = {
    active: "启用中",
    disabled: "已停用",
    blocked: "受阻",
};

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

export type OverviewInitialData = {
    sources?: SourceHealth[];
    jobs?: CollectionJob[];
    schedules?: CollectionSchedule[];
    docs?: LibraryDocument[];
    reports?: ReportSummary[];
};

/** PRD §5.1 总览: latest materials, next runs, failing sources, budget/blocked schedules, reports; empty list says why. */
export function OverviewView({ workspaceId, initialData }: { workspaceId?: string; initialData?: OverviewInitialData }) {
    const [sources] = useWorkspaceData<SourceHealth[]>(workspaceId, listSourcesHealth, initialData?.sources ?? []);
    const [jobs] = useWorkspaceData<CollectionJob[]>(workspaceId, (ws) => listJobs(ws), initialData?.jobs ?? []);
    const [schedules] = useWorkspaceData<CollectionSchedule[]>(workspaceId, listSchedules, initialData?.schedules ?? []);
    const [docs] = useWorkspaceData<LibraryDocument[]>(workspaceId, (ws) => listDocuments(ws), initialData?.docs ?? []);
    const [reports] = useWorkspaceData<ReportSummary[]>(workspaceId, listReports, initialData?.reports ?? []);
    const reason = EMPTY_REASON_TEXT[classifyEmpty({ sources, jobs, schedules })];
    const failing = sources.filter((s) => s.health === "backoff" || s.health === "degraded" || s.status === "paused");
    const next = schedules.filter((s) => s.enabled).sort((a, b) => a.next_run_at.localeCompare(b.next_run_at))[0];
    const pendingJobs = jobs.filter((j) => PENDING_STATES.has(j.job_state) || PENDING_STATES.has(j.status));
    return (
        <div style={{ display: "grid", gridTemplateColumns: "2fr 1fr", gap: 16, padding: 16 }}>
            <Card size="small" title="最新材料">
                {docs.length === 0 ? <EmptyState title={reason.title} description={reason.detail} /> : <List size="small" dataSource={docs.slice(0, 10)} renderItem={(d) => <List.Item>{d.title || d.url}</List.Item>} />}
            </Card>
            <Space direction="vertical" style={{ width: "100%" }}>
                <Card size="small" title="下次执行">
                    {next ? `${fmt(next.next_run_at)} · ${next.kind === "cron" ? next.cron : `每 ${Math.round((next.interval_seconds ?? 0) / 60)} 分钟`}` : "没有启用的计划"}
                </Card>
                <Card size="small" title={`待处理任务（${pendingJobs.length}）`}>
                    {pendingJobs.length === 0
                        ? "无"
                        : pendingJobs.slice(0, 8).map((j) => (
                              <div key={j.job_id}>
                                  <Tag color={JOB_COLOR[j.job_state]}>{JOB_LABEL[j.job_state] ?? j.job_state}</Tag>
                                  {j.source_title || j.source_id}
                                  {j.retry_at ? <Typography.Text type="secondary"> · 重试 {fmt(j.retry_at)}</Typography.Text> : null}
                              </div>
                          ))}
                </Card>
                <Card size="small" title="失败 / 暂停的来源">
                    {failing.length === 0
                        ? "无"
                        : failing.map((s) => (
                              <div key={s.id}>
                                  <Tag color="red">{HEALTH_LABEL[s.health] ?? s.health}</Tag>
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
                                      <Tag color="orange">{SCHEDULE_STATE_LABEL.blocked}</Tag>
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

export type KeywordsInitialData = {
    monitors?: IntelMonitor[];
    terms?: { builtin_version: string; builtin_groups: number; builtin: string[][]; custom: TermAlias[] };
};

/** PRD §5.1 关键词: monitor keywords plus the term dictionary (built-in groups + workspace aliases). */
export function KeywordsView({
    workspaceId,
    onOpenAgent,
    onEditMonitor,
    initialData,
}: {
    workspaceId?: string;
    onOpenAgent?: () => void;
    onEditMonitor?: (m: IntelMonitor) => void;
    initialData?: KeywordsInitialData;
}) {
    const [monitors] = useWorkspaceData<IntelMonitor[]>(workspaceId, listMonitors, initialData?.monitors ?? []);
    const [terms, refresh] = useWorkspaceData(
        workspaceId,
        listTerms,
        initialData?.terms ?? { builtin_version: "", builtin_groups: 0, builtin: [] as string[][], custom: [] as TermAlias[] },
    );
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
            <Card
                size="small"
                title="监控关键词"
                extra={
                    onOpenAgent && (
                        <Button size="small" onClick={onOpenAgent}>
                            唤起情报助手
                        </Button>
                    )
                }
            >
                <List
                    size="small"
                    dataSource={monitors}
                    locale={{ emptyText: "暂无监控；可点击右上角「唤起情报助手」在对话中创建" }}
                    renderItem={(m) => (
                        <List.Item
                            actions={
                                onEditMonitor
                                    ? [
                                          <Button key="edit" size="small" type="link" onClick={() => onEditMonitor(m)}>
                                              编辑
                                          </Button>,
                                      ]
                                    : []
                            }
                        >
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
export function LibraryView({
    workspaceId,
    initialData,
    onNavigate,
}: {
    workspaceId?: string;
    initialData?: { docs?: LibraryDocument[]; sources?: SourceHealth[]; jobs?: CollectionJob[] };
    onNavigate?: (url: string) => void;
}) {
    const [q, setQ] = useState("");
    const [docs, setDocs] = useState<LibraryDocument[]>(initialData?.docs ?? []);
    const [handoffDraft, setHandoffDraft] = useState<CanvasHandoffDraft>();
    const [sources] = useWorkspaceData<SourceHealth[]>(workspaceId, listSourcesHealth, initialData?.sources ?? []);
    const [jobs] = useWorkspaceData<CollectionJob[]>(workspaceId, (ws) => listJobs(ws), initialData?.jobs ?? []);
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
                        {
                            title: "来源",
                            render: (_: unknown, d: LibraryDocument) =>
                                d.source_kind ? (
                                    <>
                                        <Tag>{d.source_kind}</Tag>
                                        {d.source_title || d.source_id}
                                    </>
                                ) : (
                                    d.source_id
                                ),
                        },
                        { title: "发布时间", dataIndex: "published_at", render: fmt },
                        { title: "事件时间", dataIndex: "event_occurred_at", render: fmt },
                        { title: "采集时间", dataIndex: "collected_at", render: fmt },
                        { title: "提取完整度", render: (_: unknown, d: LibraryDocument) => <ExtractionTag e={d.extraction} /> },
                        { title: "修订", dataIndex: "current_revision" },
                        {
                            title: "附件",
                            render: (_: unknown, d: LibraryDocument) =>
                                (d.media_objects ?? [])
                                    .filter((m) => m.object_key)
                                    .map((m, i) => (
                                        <Button key={m.object_key} size="small" type="link" onClick={() => workspaceId && void downloadMedia(workspaceId, d.id, m.object_key as string).catch(() => void message.error("下载失败或无权访问"))}>
                                            {m.type} {i + 1}
                                        </Button>
                                    )),
                        },
                        {
                            title: "操作",
                            render: (_: unknown, d: LibraryDocument) => (
                                <Button size="small" type="link" onClick={() => setHandoffDraft(buildDocumentHandoffDraft(d))}>
                                    投送到画布
                                </Button>
                            ),
                        },
                    ]}
                />
            )}
            <CanvasHandoffModal workspaceId={workspaceId} draft={handoffDraft} onClose={() => setHandoffDraft(undefined)} onNavigate={onNavigate} />
        </Card>
    );
}

/** PRD §5.1 任务: collection jobs (PRD job states) and schedules (incl. blocked reason, window budget). */
export function JobsView({
    workspaceId,
    initialData,
}: {
    workspaceId?: string;
    initialData?: { jobs?: CollectionJob[]; schedules?: CollectionSchedule[] };
}) {
    const [status, setStatus] = useState<string>();
    const [jobs, refresh] = useWorkspaceData<CollectionJob[]>(workspaceId, (ws) => listJobs(ws, status), initialData?.jobs ?? []);
    const [schedules] = useWorkspaceData<CollectionSchedule[]>(workspaceId, listSchedules, initialData?.schedules ?? []);
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
                        placeholder="按状态筛选"
                        style={{ width: 160 }}
                        value={status}
                        onChange={setStatus}
                        options={["queued", "running", "retry_wait", "succeeded", "partial", "failed", "cancelled", "reconciliation_required"].map((v) => ({ value: v, label: JOB_LABEL[v] ?? v }))}
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
                        { title: "状态", dataIndex: "job_state", render: (s: string) => <Tag color={JOB_COLOR[s]}>{JOB_LABEL[s] ?? s}</Tag> },
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
                    renderItem={(s) => {
                        const rawState = s.state ?? (s.enabled ? "active" : "disabled");
                        return (
                            <List.Item>
                                <div>
                                    <Tag color={s.state === "blocked" ? "orange" : s.enabled ? "green" : "default"}>{SCHEDULE_STATE_LABEL[rawState] ?? rawState}</Tag>
                                    {s.kind === "cron" ? s.cron : `每 ${Math.round((s.interval_seconds ?? 0) / 60)} 分钟`} · {s.timezone} · 下次 {fmt(s.next_run_at)}
                                    {s.blocked_reason && <Typography.Text type="warning"> · {s.blocked_reason}</Typography.Text>}
                                    {!!s.budget_micro_per_window && <Typography.Text type="secondary"> · 每窗预算 {credits(s.budget_micro_per_window)}</Typography.Text>}
                                </div>
                            </List.Item>
                        );
                    }}
                />
            </Card>
        </div>
    );
}

/** PRD §5.1 行业包: packs with their skills, source templates and install state. */
export function PacksView({
    workspaceId,
    initialData,
}: {
    workspaceId?: string;
    initialData?: { packs: SkillPack[]; installed: SkillInstall[] };
}) {
    const [data, refresh] = useWorkspaceData(workspaceId, listSkills, initialData ?? { packs: [] as SkillPack[], installed: [] as SkillInstall[] });
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
