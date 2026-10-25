import { useEffect, useState } from "react";
import { Button, Checkbox, Form, Input, Radio, Select, Space, Tag, Typography, message } from "antd";
import { AppModal } from "@/components/ui/product/app-modal/app-modal";
import { canvasResourceMentionToken, type CanvasResourceReference } from "@/lib/canvas/canvas-resource-references";
import { handoffToCanvas, type CanvasHandoffCitation, type CanvasHandoffKind, type CanvasHandoffResult, type IntelTopic, type LibraryDocument, type ReportDetail } from "@/services/api/intelligence";
import { listRemoteCanvasProjects, type RemoteUserDataSummary } from "@/services/api/user-data";
import { KIND_LABEL } from "./reports-panel";

export type CanvasHandoffDraft = {
    kind: CanvasHandoffKind;
    sourceId?: string;
    title: string;
    content: string;
    citations?: CanvasHandoffCitation[];
    defaultPrompt?: string;
};

export const HANDOFF_KIND_LABEL: Record<CanvasHandoffKind, string> = {
    report: "研究报告",
    brief: "情报简报",
    topic: "研究专题",
    document: "原始素材",
    analysis: "情报分析",
};

export function buildReportHandoffDraft(detail: ReportDetail): CanvasHandoffDraft {
    const kind: CanvasHandoffKind = detail.kind === "brief" ? "brief" : "report";
    const claimLines = (detail.claims ?? []).map((c) => {
        const tag = KIND_LABEL[c.kind]?.label ?? c.kind;
        return `- [${tag}] ${c.text}`;
    });
    const sections = [detail.body?.trim() || ""];
    if (claimLines.length > 0) {
        sections.push(`### 核心论断\n${claimLines.join("\n")}`);
    }
    const seen = new Set<string>();
    const citations: CanvasHandoffCitation[] = [];
    for (const claim of detail.claims ?? []) {
        for (const cit of claim.citations ?? []) {
            const key = `${cit.document_id}:${cit.quote || ""}`;
            if (seen.has(key)) continue;
            seen.add(key);
            citations.push({
                document_id: cit.document_id,
                title: cit.title || cit.document_id,
                snippet: cit.quote,
            });
        }
    }
    return {
        kind,
        sourceId: detail.id,
        title: detail.title,
        content: sections.filter(Boolean).join("\n\n"),
        citations,
        defaultPrompt: "请基于该情报报告节点，拆解 3~6 个核心分镜脚本并生成配套视觉画面节点",
    };
}

export function buildTopicHandoffDraft(topic: IntelTopic): CanvasHandoffDraft {
    const parts: string[] = [];
    if (topic.question?.trim()) {
        parts.push(`### 核心研究问题\n${topic.question.trim()}`);
    }
    if (topic.keywords?.length) {
        parts.push(`### 关注关键词\n${topic.keywords.join("、")}`);
    }
    if (topic.notes?.trim()) {
        parts.push(`### 专题结论与笔记\n${topic.notes.trim()}`);
    }
    return {
        kind: "topic",
        sourceId: topic.id,
        title: topic.title,
        content: parts.join("\n\n") || topic.title,
        citations: [],
        defaultPrompt: "请围绕该研究专题节点策划角色设定、视觉风格参考与分镜大纲节点",
    };
}

export function buildDocumentHandoffDraft(doc: LibraryDocument): CanvasHandoffDraft {
    const lines = [
        `### 素材标题\n${doc.title || doc.url}`,
        doc.source_title || doc.source_id ? `- **来源**: ${doc.source_title || doc.source_id}${doc.source_kind ? ` (${doc.source_kind})` : ""}` : "",
        doc.url ? `- **原文链接**: ${doc.url}` : "",
        doc.published_at ? `- **发布时间**: ${doc.published_at}` : "",
    ].filter(Boolean);
    return {
        kind: "document",
        sourceId: doc.id,
        title: doc.title || doc.url,
        content: lines.join("\n"),
        citations: [
            {
                document_id: doc.id,
                title: doc.title || doc.id,
                url: doc.url,
            },
        ],
        defaultPrompt: "请提炼该情报素材的叙事看点，在画布上生成分镜脚本与视觉画面节点",
    };
}

export function extractHandoffCanvasUrl(msg: { toolName?: string; content: string }): { canvasUrl: string; canvasTitle?: string } | null {
    if (!msg.content) return null;
    if (msg.toolName && msg.toolName !== "intel_handoff_to_canvas") return null;
    if (!msg.content.includes("canvas_url") && msg.toolName !== "intel_handoff_to_canvas") return null;
    try {
        const parsed = JSON.parse(msg.content) as {
            canvas_url?: string;
            canvas_title?: string;
            output?: { canvas_url?: string; canvas_title?: string };
        };
        const url = parsed.canvas_url || parsed.output?.canvas_url;
        const title = parsed.canvas_title || parsed.output?.canvas_title;
        if (typeof url === "string" && url.startsWith("/canvas/")) {
            return { canvasUrl: url, canvasTitle: title };
        }
    } catch {
        // ignore non-JSON messages
    }
    return null;
}

export function buildIntelHandoffAgentPrefill(options: { searchParams: URLSearchParams; references: CanvasResourceReference[] }): {
    intelNodeId: string;
    wantAgent: boolean;
    prefillText: string;
    cleanedSearchParams: URLSearchParams;
} | null {
    const intelNodeId = options.searchParams.get("intelNodeId")?.trim() || "";
    const wantAgent = options.searchParams.get("agent") === "1";
    const intelPrompt = options.searchParams.get("intelPrompt")?.trim() || "";
    if (!intelNodeId && !wantAgent && !intelPrompt) return null;

    const reference = intelNodeId ? options.references.find((item) => item.nodeId === intelNodeId) : undefined;
    const mention = reference ? canvasResourceMentionToken(reference) : "";
    const instruction = intelPrompt || (reference ? "请基于该情报节点拆解分镜脚本与视觉画面节点" : "");
    const prefillText = [mention, instruction].filter(Boolean).join(" ");

    const cleanedSearchParams = new URLSearchParams(options.searchParams);
    cleanedSearchParams.delete("agent");
    cleanedSearchParams.delete("intelNodeId");
    cleanedSearchParams.delete("intelPrompt");

    return {
        intelNodeId,
        wantAgent: wantAgent || Boolean(intelPrompt),
        prefillText,
        cleanedSearchParams,
    };
}

export function CanvasHandoffModal({
    workspaceId,
    draft,
    initialCanvases = [],
    onClose,
    onSuccess,
    onNavigate,
}: {
    workspaceId?: string;
    draft?: CanvasHandoffDraft;
    initialCanvases?: RemoteUserDataSummary[];
    onClose: () => void;
    onSuccess?: (result: CanvasHandoffResult) => void;
    onNavigate?: (url: string) => void;
}) {
    const [targetMode, setTargetMode] = useState<"new" | "existing">("new");
    const [canvases, setCanvases] = useState<RemoteUserDataSummary[]>(initialCanvases);
    const [selectedCanvasId, setSelectedCanvasId] = useState<string | undefined>(initialCanvases[0]?.id);
    const [canvasTitle, setCanvasTitle] = useState(draft?.title ?? "");
    const [nodeTitle, setNodeTitle] = useState(draft?.title ?? "");
    const [content, setContent] = useState(draft?.content ?? "");
    const [openAgent, setOpenAgent] = useState(true);
    const [agentPrompt, setAgentPrompt] = useState(draft?.defaultPrompt ?? "请基于该情报节点拆解分镜脚本与视觉画面节点");
    const [submitting, setSubmitting] = useState(false);

    useEffect(() => {
        if (!draft) return;
        setTargetMode("new");
        setCanvasTitle(draft.title);
        setNodeTitle(draft.title);
        setContent(draft.content);
        setOpenAgent(true);
        setAgentPrompt(draft.defaultPrompt ?? "请基于该情报节点拆解分镜脚本与视觉画面节点");
        void listRemoteCanvasProjects()
            .then((r) => {
                const list = r.projects ?? [];
                setCanvases(list);
                setSelectedCanvasId((cur) => cur ?? list[0]?.id);
            })
            .catch(() => {
                // keep initialCanvases if offline or in unit tests
            });
    }, [draft]);

    const submit = async () => {
        if (!workspaceId || !draft) return;
        if (!nodeTitle.trim() || !content.trim()) {
            void message.warning("请填写情报节点标题与内容");
            return;
        }
        if (targetMode === "existing" && !selectedCanvasId) {
            void message.warning("请选择要投送的目标画布");
            return;
        }
        setSubmitting(true);
        try {
            const res = await handoffToCanvas(workspaceId, {
                canvasId: targetMode === "existing" ? selectedCanvasId : undefined,
                canvasTitle: targetMode === "new" ? canvasTitle.trim() || nodeTitle.trim() : undefined,
                kind: draft.kind,
                sourceId: draft.sourceId,
                title: nodeTitle.trim(),
                content: content.trim(),
                citations: draft.citations,
                agentPrompt: openAgent ? agentPrompt.trim() : undefined,
                openAgent,
            });
            void message.success(res.created_canvas ? "已创建新画布并写入情报源节点" : "已将情报源节点投送到目标画布");
            onSuccess?.(res);
            onClose();
            if (onNavigate) {
                onNavigate(res.canvas_url);
            } else if (typeof window !== "undefined" && window.location) {
                window.location.assign(res.canvas_url);
            }
        } catch (e) {
            void message.error(e instanceof Error ? e.message : "投送到画布失败");
        } finally {
            setSubmitting(false);
        }
    };

    return (
        <AppModal open={!!draft} title="投送到画布继续创作" onOk={() => void submit()} onCancel={onClose} confirmLoading={submitting} okText="确认投送并打开画布" cancelText="取消" width={600}>
            {draft && (
                <Form layout="vertical">
                    <div style={{ marginBottom: 12 }}>
                        <Space size={8}>
                            <Tag color="blue">{HANDOFF_KIND_LABEL[draft.kind]}</Tag>
                            <Tag color="purple">附带 {draft.citations?.length ?? 0} 条可溯源证据</Tag>
                        </Space>
                    </div>
                    <Form.Item label="目标画布">
                        <Radio.Group value={targetMode} onChange={(e) => setTargetMode(e.target.value)}>
                            <Radio value="new">新建独立画布</Radio>
                            <Radio value="existing">投送到已有画布</Radio>
                        </Radio.Group>
                    </Form.Item>
                    {targetMode === "new" ? (
                        <Form.Item label="新画布名称">
                            <Input value={canvasTitle} onChange={(e) => setCanvasTitle(e.target.value)} placeholder="输入新画布名称" />
                        </Form.Item>
                    ) : (
                        <Form.Item label="选择已有画布">
                            <Select value={selectedCanvasId} onChange={setSelectedCanvasId} placeholder="选择目标画布" options={canvases.map((c) => ({ value: c.id, label: c.title || c.id }))} />
                        </Form.Item>
                    )}
                    <Form.Item label="画布文本节点标题">
                        <Input value={nodeTitle} onChange={(e) => setNodeTitle(e.target.value)} />
                    </Form.Item>
                    <Form.Item label="节点正文（投送时将自动附加情报来源与证据引用脚标）">
                        <Input.TextArea rows={5} value={content} onChange={(e) => setContent(e.target.value)} />
                    </Form.Item>
                    <Form.Item style={{ marginBottom: 8 }}>
                        <Checkbox checked={openAgent} onChange={(e) => setOpenAgent(e.target.checked)}>
                            打开画布后自动唤起画布 Pi Agent 并引用该情报节点
                        </Checkbox>
                    </Form.Item>
                    {openAgent && (
                        <Form.Item label="画布 Pi Agent 预填创作指令">
                            <Input.TextArea rows={2} value={agentPrompt} onChange={(e) => setAgentPrompt(e.target.value)} placeholder="例如：基于该情报节点拆解分镜脚本与视觉画面节点" />
                            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                                进入画布后将自动关联 @情报节点 并填入上述指令，供你确认后发起分镜或视觉生成。
                            </Typography.Text>
                        </Form.Item>
                    )}
                </Form>
            )}
        </AppModal>
    );
}
