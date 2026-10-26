import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";

import { buildDocumentHandoffDraft, buildIntelHandoffAgentPrefill, buildReportHandoffDraft, buildTopicHandoffDraft, extractHandoffCanvasUrl } from "../src/pages/intelligence/canvas-handoff-modal";
import { ApprovalDetails, MonitorPlanView } from "../src/pages/intelligence/plan-card";
import { formatCoverageNotices, KIND_LABEL, REPORT_EXPORT_FORMATS, ReportsPanel } from "../src/pages/intelligence/reports-panel";
import { buildSourceInput, KIND_HINT, SOURCE_STATUS_LABEL, SourcesPanel } from "../src/pages/intelligence/sources-panel";
import { TopicsPanel } from "../src/pages/intelligence/topics-panel";
import { EXTRACTION_LABEL, HEALTH_LABEL, JOB_LABEL, JobsView, KeywordsView, LibraryView, OverviewView, PacksView, SCHEDULE_STATE_LABEL } from "../src/pages/intelligence/views";
import { normalizeEditableRole, RESERVATION_STATUS_LABEL, ROLE_LABEL, WorkspaceAdminPanel } from "../src/pages/intelligence/workspace-admin-panel";
import { fileNameFrom } from "../src/services/api/intelligence";

describe("SourcesPanel helpers and rendering", () => {
    test("buildSourceInput trims values, rejects blank input, and builds protocol-specific payloads", () => {
        expect(buildSourceInput("rss", "   ")).toBeUndefined();
        expect(buildSourceInput("rss", " https://example.com/feed.xml ")).toEqual({
            kind: "rss",
            url: "https://example.com/feed.xml",
        });
        expect(buildSourceInput("telegram", " @ai_channel ")).toEqual({
            kind: "telegram",
            config: { handle: "@ai_channel" },
        });
        expect(buildSourceInput("wechat", " wx_src_01 ")).toEqual({
            kind: "wechat",
            config: { gateway_source_id: "wx_src_01" },
        });
    });

    test("renders source status labels, Telegram attachment notice, and connector acceptance samples", () => {
        expect(SOURCE_STATUS_LABEL).toEqual({
            active: "启用中",
            paused: "已暂停",
            revoked: "已停用",
        });
        expect(KIND_HINT.wechat).toContain("微信网关源 ID");

        const html = renderToStaticMarkup(
            <SourcesPanel
                workspaceId="ws_1"
                initialKind="telegram"
                initialSources={[
                    { id: "s1", kind: "rss", url: "https://a.example/rss", title: "AI 资讯源", status: "active" },
                    { id: "s2", kind: "telegram", url: "https://t.me/b", title: "TG 频道", status: "paused", last_error: "401 unauthorized" },
                    { id: "s3", kind: "wechat", url: "wx://c", title: "公众号源", status: "revoked" },
                ]}
                initialSamples={[
                    {
                        id: "smp_1",
                        kind: "telegram",
                        source_id: "s2",
                        collector: "tg-public",
                        provider: "mirror-1",
                        mode: "live",
                        items: 15,
                        media_unsupported: 2,
                        recorded_at: "2026-10-19T08:00:00Z",
                    },
                ]}
            />,
        );

        expect(html).toContain("支持公开频道消息与受管大小内的媒体附件采集");
        expect(html).toContain("AI 资讯源");
        expect(html).toContain("停用");
        expect(html).toContain("已暂停");
        expect(html).toContain("401 unauthorized");
        expect(html).toContain("已停用");
        expect(html).toContain("实采");
        expect(html).toContain("采集 15 条（通过 tg-public / mirror-1）");
        expect(html).toContain("2 个超限/不支持附件");
    });
});

describe("ReportsPanel helpers, export filename parsing, and rendering", () => {
    test("formatCoverageNotices summarizes source failures, truncation, and conflicting revisions", () => {
        expect(formatCoverageNotices(undefined)).toEqual([]);
        expect(formatCoverageNotices({})).toEqual([]);
        const notices = formatCoverageNotices({
            source_failures: [
                { source_id: "src_rss", error: "timeout" },
                { source_id: "src_tg", error: "403" },
            ],
            truncated: true,
            conflicting_evidence: [{ document_id: "doc_9", revision: 3, note: "updated" }],
        });
        expect(notices).toEqual(["采集失败信源：src_rss (timeout)、src_tg (403)", "部分材料因预算或读取窗口上限被截断。", "已修订文档（可能存在冲突证据）：doc_9@r3"]);
    });

    test("fileNameFrom parses RFC 5987 UTF-8 filenames, quoted ASCII filenames, and fallbacks", () => {
        expect(fileNameFrom({ "content-disposition": "attachment; filename*=UTF-8''%E5%91%A8%E6%8A%A5.pdf" }, "report.pdf")).toBe("周报.pdf");
        expect(fileNameFrom({ "Content-Disposition": 'attachment; filename="weekly-brief.docx"' }, "report.docx")).toBe("weekly-brief.docx");
        expect(fileNameFrom({}, "report.md")).toBe("report.md");
    });

    test("renders empty state and populated report detail with all 5 export formats, coverage Callout, and claim evidence", () => {
        expect(REPORT_EXPORT_FORMATS).toEqual(["md", "html", "json", "pdf", "docx"]);
        expect(KIND_LABEL.cross_source_verified.label).toBe("跨源印证");

        const emptyHtml = renderToStaticMarkup(<ReportsPanel workspaceId="ws_1" />);
        expect(emptyHtml).toContain("请选择一份报告查看详情与证据链");
        expect(emptyHtml).toContain("暂无报告");
        expect(emptyHtml).toContain("暂无定时简报计划");

        const detailHtml = renderToStaticMarkup(
            <ReportsPanel
                workspaceId="ws_1"
                initialReports={[
                    {
                        id: "rep_1",
                        kind: "brief",
                        title: "今日 AI 简报",
                        coverage: { truncated: true },
                        claims: 2,
                        created_at: "2026-10-19T10:00:00Z",
                    },
                ]}
                initialBriefs={[
                    {
                        id: "br_1",
                        name: "早报计划",
                        query: "AI",
                        cron: "0 9 * * *",
                        timezone: "Asia/Shanghai",
                        window_hours: 24,
                        next_run_at: "2026-10-20T01:00:00Z",
                        enabled: false,
                    },
                ]}
                initialDetail={{
                    id: "rep_1",
                    kind: "brief",
                    title: "今日 AI 简报",
                    body: "核心结论正文",
                    created_at: "2026-10-19T10:00:00Z",
                    claims: [
                        {
                            idx: 1,
                            text: "多源确认新模型已发布",
                            kind: "cross_source_verified",
                            citations: [
                                {
                                    evidence_id: "ev_1",
                                    document_id: "doc_1",
                                    source_id: "s1",
                                    title: "官方公告",
                                    revision: 2,
                                    chunk_index: 0,
                                    quote: "已正式上线",
                                    truncated: true,
                                },
                            ],
                        },
                    ],
                    coverage: {
                        truncated: true,
                        source_failures: [{ source_id: "s_bad", error: "502" }],
                    },
                    skill: { id: "daily_brief", version: "1.2.0", hash: "abcdef1234567890" },
                }}
            />,
        );

        expect(detailHtml).toContain("今日 AI 简报");
        expect(detailHtml).toContain("简报");
        expect(detailHtml).toContain("技能 daily_brief@1.2.0 · abcdef123456");
        expect(detailHtml).toContain("覆盖范围缺口提醒");
        expect(detailHtml).toContain("采集失败信源：s_bad (502)");
        expect(detailHtml).toContain("部分材料因预算或读取窗口上限被截断。");
        for (const fmt of ["MD", "HTML", "JSON", "PDF", "DOCX"]) {
            expect(detailHtml).toContain(`导出 ${fmt}`);
        }
        expect(detailHtml).toContain("跨源印证");
        expect(detailHtml).toContain("多源确认新模型已发布");
        expect(detailHtml).toContain("早报计划");
        expect(detailHtml).toContain("已暂停");
    });
});

describe("WorkspaceAdminPanel helpers and rendering", () => {
    test("normalizeEditableRole maps legacy member/owner to editor while preserving admin and viewer", () => {
        expect(normalizeEditableRole("admin")).toBe("admin");
        expect(normalizeEditableRole("viewer")).toBe("viewer");
        expect(normalizeEditableRole("editor")).toBe("editor");
        expect(normalizeEditableRole("member")).toBe("editor");
        expect(normalizeEditableRole("owner")).toBe("editor");
    });

    test("returns empty markup without workspaceId and renders localized badges, reservations, and member roles", () => {
        expect(renderToStaticMarkup(<WorkspaceAdminPanel />)).toBe("");
        expect(RESERVATION_STATUS_LABEL.unknown).toBe("结果未决");
        expect(ROLE_LABEL.owner).toBe("所有者 (owner)");
        expect(ROLE_LABEL.member).toBe("编辑者 (editor)");

        const html = renderToStaticMarkup(
            <WorkspaceAdminPanel
                workspaceId="ws_1"
                initialData={{
                    notes: [
                        { id: "n1", type: "alert", title: "信源异常告警", refId: "s1", readAt: null, createdAt: "2026-10-19T08:00:00Z" },
                        { id: "n2", type: "report", title: "简报已生成", refId: "r1", readAt: "2026-10-19T09:00:00Z", createdAt: "2026-10-19T08:30:00Z" },
                    ],
                    packs: [
                        {
                            id: "pack_fin",
                            version: "1.0.0",
                            name: "金融研报包",
                            skills: [{ id: "sk_1", version: "1.0.0", name: "财报拆解", method: "llm", hash: "h1", pack_id: "pack_fin" }],
                        },
                    ],
                    installed: [{ id: "inst_1", skillId: "sk_1", version: "1.0.0", hash: "h1", packId: "pack_fin", updatedAt: "2026-10-19T08:00:00Z" }],
                    account: {
                        id: "acc_1",
                        tenantId: "t_1",
                        payerUserId: "u_1",
                        kind: "shared",
                        monthlyQuotaMicro: 5_000_000,
                        maxOpenReserves: 5,
                    },
                    reservations: [
                        {
                            id: "res_1",
                            purpose: "窗口采集预留",
                            parentId: "job_1",
                            reservedMicro: 500_000,
                            allocatedMicro: 500_000,
                            consumedMicro: 250_000,
                            status: "unknown",
                            createdAt: "2026-10-19T08:00:00Z",
                        },
                        {
                            id: "res_2",
                            purpose: "简报生成预留",
                            parentId: "rep_1",
                            reservedMicro: 200_000,
                            allocatedMicro: 200_000,
                            consumedMicro: 200_000,
                            status: "settled",
                            createdAt: "2026-10-19T07:00:00Z",
                        },
                    ],
                    members: [
                        { userId: "u_owner", email: "owner@example.com", name: "Alice Owner", role: "owner", status: "active", joinedAt: "2026-10-01T00:00:00Z" },
                        { userId: "u_member", email: "bob@example.com", name: "Bob LegacyMember", role: "member", status: "active", joinedAt: "2026-10-02T00:00:00Z" },
                    ],
                    canManage: true,
                }}
            />,
        );

        expect(html).toContain("1 条未读");
        expect(html).toContain("信源异常告警");
        expect(html).toContain("标为已读");
        expect(html).toContain("金融研报包");
        expect(html).toContain("财报拆解");
        expect(html).toContain("团队共享账户");
        expect(html).toContain("5.00 积分");
        expect(html).toContain("结果未决");
        expect(html).toContain("已结算");
        expect(html).toContain("标记已执行");
        expect(html).toContain("标记未执行");
        expect(html).toContain("所有者 (owner)");
        expect(html).toContain("编辑者 (editor)");
    });
});

describe("Views (OverviewView, KeywordsView, LibraryView, JobsView, PacksView) and PlanCard rendering", () => {
    test("OverviewView and LibraryView surface classified EmptyState reasons when document list is empty", () => {
        const overviewHtml = renderToStaticMarkup(
            <OverviewView
                workspaceId="ws_1"
                initialData={{
                    sources: [{ id: "s1", kind: "rss", url: "https://a", title: "源A", status: "active", health: "backoff" }],
                    jobs: [
                        {
                            job_id: "j1",
                            operation_id: "op1",
                            source_id: "s1",
                            source_title: "源A",
                            status: "retry_wait",
                            job_state: "retry_wait",
                            items_new: 0,
                            items_revised: 0,
                            attempts: 2,
                            created_at: "2026-10-19T08:00:00Z",
                        },
                    ],
                    schedules: [
                        {
                            id: "sc1",
                            source_ids: ["s1"],
                            kind: "interval",
                            interval_seconds: 900,
                            timezone: "Asia/Shanghai",
                            next_run_at: "2026-10-19T12:00:00Z",
                            enabled: true,
                            state: "blocked",
                            blocked_reason: "budget_insufficient",
                        },
                    ],
                    docs: [],
                    reports: [],
                }}
            />,
        );

        expect(overviewHtml).toContain("采集排队中");
        expect(overviewHtml).toContain("等待重试");
        expect(overviewHtml).toContain("退避重试");
        expect(overviewHtml).toContain("受阻");
        expect(overviewHtml).toContain("budget_insufficient");

        const libraryEmptyHtml = renderToStaticMarkup(
            <LibraryView
                workspaceId="ws_1"
                initialData={{
                    docs: [],
                    sources: [{ id: "s1", kind: "telegram", url: "https://t.me/a", title: "TG", status: "paused", health: "paused", paused_reason: "credential_invalid" }],
                    jobs: [],
                }}
            />,
        );
        expect(libraryEmptyHtml).toContain("权限已失效");
        expect(libraryEmptyHtml).toContain("有信源的凭据失效或计划的授权已被撤销");
    });

    test("KeywordsView renders Agent drawer trigger, monitor edit action, and custom term aliases", () => {
        const html = renderToStaticMarkup(
            <KeywordsView
                workspaceId="ws_1"
                onOpenAgent={() => {}}
                onEditMonitor={() => {}}
                initialData={{
                    monitors: [
                        {
                            id: "m1",
                            name: "大模型竞品监控",
                            keywords: ["DeepSeek", "Qwen"],
                            source_ids: ["s1"],
                            interval_seconds: 1800,
                            timezone: "Asia/Shanghai",
                            created_at: "2026-10-19T00:00:00Z",
                        },
                    ],
                    terms: {
                        builtin_version: "v2026.10",
                        builtin_groups: 8,
                        builtin: [["LLM", "大语言模型"]],
                        custom: [{ term: "MoE", alias: "混合专家模型" }],
                    },
                }}
            />,
        );

        expect(html).toContain("唤起情报助手");
        expect(html).toContain("大模型竞品监控");
        expect(html).toContain("DeepSeek");
        expect(html).toContain("Qwen");
        expect(html).toContain("编辑");
        expect(html).toContain("术语与别名（内置 8 组 · v2026.10）");
        expect(html).toContain("MoE ⇄ 混合专家模型");
    });

    test("JobsView, PacksView, MonitorPlanView, and ApprovalDetails render localized labels and formatted credits", () => {
        expect(JOB_LABEL.reconciliation_required).toBe("待对账");
        expect(HEALTH_LABEL.backoff).toBe("退避重试");
        expect(SCHEDULE_STATE_LABEL.blocked).toBe("受阻");
        expect(EXTRACTION_LABEL.full_text.text).toBe("全文");

        const jobsHtml = renderToStaticMarkup(
            <JobsView
                workspaceId="ws_1"
                initialData={{
                    jobs: [
                        {
                            job_id: "j1",
                            operation_id: "op1",
                            source_id: "s1",
                            source_title: "行业快讯",
                            status: "reconciliation_required",
                            job_state: "reconciliation_required",
                            items_new: 3,
                            items_revised: 1,
                            attempts: 1,
                            created_at: "2026-10-19T08:00:00Z",
                        },
                    ],
                    schedules: [
                        {
                            id: "sc1",
                            source_ids: ["s1"],
                            kind: "interval",
                            interval_seconds: 600,
                            timezone: "Asia/Shanghai",
                            next_run_at: "2026-10-19T09:00:00Z",
                            enabled: true,
                            state: "active",
                            budget_micro_per_window: 1_500_000,
                        },
                    ],
                }}
            />,
        );
        expect(jobsHtml).toContain("待对账");
        expect(jobsHtml).toContain("启用中");
        expect(jobsHtml).toContain("每窗预算");
        expect(jobsHtml).toContain("1.5");
        expect(jobsHtml).toContain("积分");

        const packsHtml = renderToStaticMarkup(
            <PacksView
                workspaceId="ws_1"
                initialData={{
                    packs: [{ id: "p1", version: "1.0", name: "跨境电商包", skills: [{ id: "s1", version: "1.0", name: "选品雷达", method: "llm", hash: "h", pack_id: "p1" }] }],
                    installed: [{ id: "i1", skillId: "s1", version: "1.0", hash: "h", packId: "p1", updatedAt: "2026-10-19T00:00:00Z" }],
                }}
            />,
        );
        expect(packsHtml).toContain("跨境电商包");
        expect(packsHtml).toContain("已安装");

        const planHtml = renderToStaticMarkup(
            <MonitorPlanView
                plan={{
                    preview_id: "prv_1",
                    name: "算力供应链监控",
                    keywords: ["H200", "CoWoS"],
                    source_ids: ["s1"],
                    interval_seconds: 900,
                    timezone: "Asia/Shanghai",
                    budget_micro_per_window: 2_000_000,
                    expansion: ["sources", "budget"],
                    cost_estimate: {
                        price_version: "v1",
                        per_window_micro: 400_000,
                        per_day_micro: 9_600_000,
                        per_30d_micro: 288_000_000,
                        basis: "按信源与窗口估算",
                    },
                }}
            />,
        );
        expect(planHtml).toContain("此变更扩大了已批准的范围，需要重新决策");
        expect(planHtml).toContain("扩大来源、提高预算");
        expect(planHtml).toContain("仅站内通知");

        const approvalHtml = renderToStaticMarkup(<ApprovalDetails tool="intel_create_source" rawArgs='{"kind":"rss","url":"https://example.com/rss"}' />);
        expect(approvalHtml).toContain("https://example.com/rss");

        const approvalInvalidJsonHtml = renderToStaticMarkup(<ApprovalDetails tool="custom_tool" rawArgs="non-json-payload" />);
        expect(approvalInvalidJsonHtml).toContain("non-json-payload");
    });

    test("TopicsPanel renders saved topics, keywords, and conditional conversation trigger", () => {
        const topics = [
            {
                id: "tp_1",
                workspaceId: "ws_1",
                title: "出海短剧投放趋势",
                question: "北美市场 ROI 变化",
                keywords: ["ReelShort", "DramaBox"],
                sourceIds: ["s1"],
                notes: "",
                version: 1,
                createdAt: "2026-10-19T00:00:00Z",
                updatedAt: "2026-10-19T00:00:00Z",
            },
        ];
        const withOpenHtml = renderToStaticMarkup(<TopicsPanel workspaceId="ws_1" initialTopics={topics} onOpen={() => {}} />);
        expect(withOpenHtml).toContain("出海短剧投放趋势");
        expect(withOpenHtml).toContain("ReelShort");
        expect(withOpenHtml).toContain("DramaBox");
        expect(withOpenHtml).toContain("编辑");
        expect(withOpenHtml).toContain("对话");

        const withoutOpenHtml = renderToStaticMarkup(<TopicsPanel workspaceId="ws_1" initialTopics={topics} />);
        expect(withoutOpenHtml).toContain("编辑");
        expect(withoutOpenHtml).toContain("投送到画布");
        expect(withoutOpenHtml).not.toContain(">对话<");
    });

    test("Intelligence ↔ Canvas handoff draft builders, tool URL extractor, and Canvas Agent prefill helper", () => {
        const reportDraft = buildReportHandoffDraft({
            id: "rep_101",
            kind: "report",
            title: "短剧买量趋势报告",
            body: "Q3 海外短剧投放素材以强反转为主。",
            claims: [
                {
                    idx: 1,
                    kind: "cross_source_verified",
                    text: "反转类素材完播率提升 45%",
                    citations: [
                        { evidence_id: "ev_1", document_id: "doc_1", title: "投放周报", revision: 1, chunk_index: 0, quote: "反转完播率 +45%", truncated: false },
                        { evidence_id: "ev_2", document_id: "doc_1", title: "投放周报", revision: 1, chunk_index: 0, quote: "反转完播率 +45%", truncated: false },
                    ],
                },
            ],
        });
        expect(reportDraft.kind).toBe("report");
        expect(reportDraft.sourceId).toBe("rep_101");
        expect(reportDraft.content).toContain("Q3 海外短剧投放素材以强反转为主。");
        expect(reportDraft.content).toContain("- [跨源印证] 反转类素材完播率提升 45%");
        expect(reportDraft.citations).toHaveLength(1);
        expect(reportDraft.citations?.[0]).toEqual({
            document_id: "doc_1",
            title: "投放周报",
            snippet: "反转完播率 +45%",
        });

        const topicDraft = buildTopicHandoffDraft({
            id: "tp_9",
            workspaceId: "ws_1",
            title: "东南亚受众偏好",
            question: "印尼市场偏爱哪类女主设定？",
            keywords: ["逆袭", "豪门"],
            sourceIds: ["s1"],
            notes: "本土化配音转化率高",
            version: 2,
            createdAt: "2026-10-19T00:00:00Z",
            updatedAt: "2026-10-19T00:00:00Z",
        });
        expect(topicDraft.kind).toBe("topic");
        expect(topicDraft.content).toContain("印尼市场偏爱哪类女主设定？");
        expect(topicDraft.content).toContain("逆袭、豪门");
        expect(topicDraft.content).toContain("本土化配音转化率高");

        const docDraft = buildDocumentHandoffDraft({
            id: "doc_99",
            source_id: "s1",
            source_title: "TikTok 榜单",
            source_kind: "rss",
            title: "热门短剧 TOP10 拆解",
            url: "https://example.com/top10",
            current_revision: 1,
        });
        expect(docDraft.kind).toBe("document");
        expect(docDraft.citations?.[0]?.url).toBe("https://example.com/top10");

        expect(
            extractHandoffCanvasUrl({
                toolName: "intel_handoff_to_canvas",
                content: JSON.stringify({ canvas_id: "cv_1", canvas_title: "测试画布", canvas_url: "/canvas/cv_1?agent=1&intelNodeId=n_1" }),
            }),
        ).toEqual({
            canvasUrl: "/canvas/cv_1?agent=1&intelNodeId=n_1",
            canvasTitle: "测试画布",
        });
        expect(extractHandoffCanvasUrl({ toolName: "intel_search_documents", content: '{"canvas_url":"/canvas/cv_1"}' })).toBeNull();

        const prefill = buildIntelHandoffAgentPrefill({
            searchParams: new URLSearchParams("agent=1&intelNodeId=intel_node_1&intelPrompt=%E6%8B%86%E8%A7%A3%E5%89%8D%E4%B8%89%E9%9B%86%E5%88%86%E9%95%9C&keep=1"),
            references: [
                {
                    id: "node:intel_node_1",
                    nodeId: "intel_node_1",
                    kind: "text",
                    label: "文本1",
                    title: "短剧买量趋势报告",
                    origin: "canvas",
                },
            ],
        });
        expect(prefill).not.toBeNull();
        expect(prefill?.intelNodeId).toBe("intel_node_1");
        expect(prefill?.wantAgent).toBe(true);
        expect(prefill?.prefillText).toBe("@文本1 拆解前三集分镜");
        expect(prefill?.cleanedSearchParams.get("agent")).toBeNull();
        expect(prefill?.cleanedSearchParams.get("intelNodeId")).toBeNull();
        expect(prefill?.cleanedSearchParams.get("intelPrompt")).toBeNull();
        expect(prefill?.cleanedSearchParams.get("keep")).toBe("1");
    });
});
