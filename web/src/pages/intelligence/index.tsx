import { useCallback, useEffect, useRef, useState } from "react";
import { Button, Card, Input, List, Select, Space, Tag, Typography, message } from "antd";
import { EmptyState } from "@/components/ui/product/empty-state/empty-state";
import { ReportsPanel } from "./reports-panel";
import { SourcesPanel } from "./sources-panel";
import { WorkspaceAdminPanel } from "./workspace-admin-panel";

import {
    cachedConversations,
    callTool,
    createConversation,
    listConversations,
    listIntelWorkspaces,
    listMessages,
    listMonitors,
    type FlowConversation,
    type FlowMessage,
    type IntelMonitor,
    type IntelWorkspace,
    type ToolResult,
} from "@/services/api/intelligence";
import { createAgentRun, decideAgentApproval, getAgentRun, subscribeAgentEvents, type AgentApproval } from "@/services/api/agent";

const TERMINAL = new Set(["completed", "failed", "cancelled", "rejected"]);

const newCallId = () => `ui_${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 8)}`;

/** P2 intelligence workspace: scoped chat (server conversation directory), tool actions, approvals, monitors. */
export default function IntelligencePage() {
    const [workspaces, setWorkspaces] = useState<IntelWorkspace[]>([]);
    const [workspaceId, setWorkspaceId] = useState<string>();
    const [conversations, setConversations] = useState<FlowConversation[]>(cachedConversations);
    const [active, setActive] = useState<FlowConversation>();
    const [messages, setMessages] = useState<FlowMessage[]>([]);
    const [monitors, setMonitors] = useState<IntelMonitor[]>([]);
    const [pending, setPending] = useState<ToolResult[]>([]);
    const [text, setText] = useState("");
    const [runId, setRunId] = useState<string>();
    const [streaming, setStreaming] = useState("");
    const [approval, setApproval] = useState<AgentApproval>();
    const unsubscribe = useRef<(() => void) | undefined>(undefined);
    useEffect(() => () => unsubscribe.current?.(), []);

    useEffect(() => {
        void listIntelWorkspaces().then((list) => {
            setWorkspaces(list);
            setWorkspaceId((current) => current ?? list[0]?.id);
        });
        void listConversations().then(setConversations);
    }, []);

    const refreshMonitors = useCallback(() => {
        if (workspaceId)
            void listMonitors(workspaceId)
                .then(setMonitors)
                .catch(() => setMonitors([]));
    }, [workspaceId]);
    useEffect(refreshMonitors, [refreshMonitors]);

    const refreshMessages = useCallback(async (conv: FlowConversation) => {
        const page = await listMessages(conv.id);
        setMessages([...page.messages].reverse());
    }, []);

    const ensureConversation = async () => {
        if (active) return active;
        if (!workspaceId) throw new Error("no workspace");
        const conv = await createConversation({ kind: "intelligence_workspace", id: workspaceId }, text.slice(0, 60) || "Intelligence chat");
        setActive(conv);
        setConversations(await listConversations());
        return conv;
    };

    /** Sends the turn to the Pi agent (subject-bound run) and streams the model reply. */
    const send = async () => {
        const prompt = text.trim();
        if (!prompt || runId) return;
        if (active?.subjectKind === "canvas") {
            void message.info("This conversation belongs to a canvas; continue it from the canvas sidebar.");
            return;
        }
        const conv = await ensureConversation();
        setText("");
        setStreaming("");
        try {
            const { run } = await createAgentRun({ subject: { kind: conv.subjectKind as "intelligence_workspace" | "topic", id: conv.subjectId }, conversationId: conv.id, prompt, permissionMode: "request_approval", idempotencyKey: newCallId() });
            setRunId(run.id);
            await refreshMessages(conv);
            unsubscribe.current?.();
            unsubscribe.current = subscribeAgentEvents(run.id, (event) => {
                const p = event.payload as { text?: string; replace?: boolean; status?: string };
                if (event.type === "assistant_delta") setStreaming((cur) => (p.replace ? p.text || "" : cur + (p.text || "")));
                else if (event.type === "assistant_message") setStreaming(p.text || "");
                else if (event.type === "approval_requested") void getAgentRun(run.id).then((r) => setApproval(r.run.approval));
                // run_terminal is the server's explicit end marker; the status checks are a fallback for older servers.
                if (event.type === "run_terminal" || (event.type === "run_status" && p.status && TERMINAL.has(p.status))) {
                    unsubscribe.current?.();
                    setRunId(undefined);
                    setApproval(undefined);
                    setStreaming("");
                    refreshMonitors();
                    void refreshMessages(conv);
                }
            });
        } catch (err) {
            setRunId(undefined);
            void message.error(err instanceof Error ? err.message : "agent run failed");
        }
    };

    const decideRun = async (approve: boolean) => {
        if (!runId || !approval) return;
        await decideAgentApproval(runId, approval.approvalId, approve ? "approve" : "reject");
        setApproval(undefined);
    };

    const runTool = async (name: string, args: Record<string, unknown>) => {
        const conv = await ensureConversation();
        const result = await callTool(conv.id, newCallId(), name, args);
        if (result.status === "waiting_approval") setPending((p) => [...p, result]);
        if (result.status === "failed") void message.error(result.error || "tool failed");
        await refreshMessages(conv);
        return result;
    };

    const decide = (r: ToolResult) => {
        // Manual tool calls no longer approve outside the agent: apply_monitor is approved via the Pi run chain.
        setPending((p) => p.filter((x) => x !== r));
        void message.info("Ask the agent to create the monitor; approval happens in the agent run.");
    };

    return (
        <>
            <div style={{ display: "grid", gridTemplateColumns: "240px 1fr 320px", gap: 16, padding: 16 }}>
                <Card size="small" title="Conversations">
                    <Select style={{ width: "100%", marginBottom: 8 }} value={workspaceId} onChange={setWorkspaceId} options={workspaces.map((w) => ({ value: w.id, label: w.name }))} />
                    <Button
                        block
                        onClick={() => {
                            setActive(undefined);
                            setMessages([]);
                        }}
                    >
                        New
                    </Button>
                    <List
                        size="small"
                        dataSource={conversations}
                        renderItem={(c) => (
                            <List.Item
                                style={{ cursor: "pointer", fontWeight: c.id === active?.id ? 600 : 400 }}
                                onClick={() => {
                                    setActive(c);
                                    void refreshMessages(c);
                                }}
                            >
                                {c.subjectKind === "canvas" && <Tag color="purple">canvas</Tag>}
                                {c.subjectKind === "topic" && <Tag color="gold">topic</Tag>}
                                {c.title || c.id}
                            </List.Item>
                        )}
                    />
                </Card>
                <Card size="small" title="Intelligence chat">
                    <div style={{ minHeight: 320, maxHeight: 520, overflow: "auto" }}>
                        {messages.length === 0 ? (
                            <EmptyState title="Ask about collected materials" />
                        ) : (
                            messages.map((m) => (
                                <div key={m.id} style={{ marginBottom: 8 }}>
                                    <Tag>{m.toolName || m.role}</Tag>
                                    <Typography.Text style={{ whiteSpace: "pre-wrap" }}>{m.content.slice(0, 2000)}</Typography.Text>
                                </div>
                            ))
                        )}
                        {streaming && (
                            <div style={{ marginBottom: 8 }}>
                                <Tag color="blue">assistant</Tag>
                                <Typography.Text style={{ whiteSpace: "pre-wrap" }}>{streaming}</Typography.Text>
                            </div>
                        )}
                    </div>
                    <Space.Compact style={{ width: "100%" }}>
                        <Input value={text} onChange={(e) => setText(e.target.value)} onPressEnter={() => void send()} placeholder="Message" />
                        <Button type="primary" loading={!!runId} onClick={() => void send()}>
                            Send
                        </Button>
                        <Button onClick={() => void runTool("intel_search_documents", { query: text })} disabled={!text.trim()}>
                            Search
                        </Button>
                    </Space.Compact>
                    {approval && (
                        <Card size="small" style={{ marginTop: 8 }} title={`Approval needed: ${approval.call.function.name}`}>
                            <Typography.Paragraph style={{ whiteSpace: "pre-wrap" }}>{approval.call.function.arguments}</Typography.Paragraph>
                            <Space>
                                <Button type="primary" onClick={() => void decideRun(true)}>
                                    Approve
                                </Button>
                                <Button danger onClick={() => void decideRun(false)}>
                                    Reject
                                </Button>
                            </Space>
                        </Card>
                    )}
                    {pending.map((p) => (
                        <Card key={p.callId} size="small" style={{ marginTop: 8 }} title={`Needs agent approval: ${p.tool}`}>
                            <Button onClick={() => decide(p)}>Dismiss</Button>
                        </Card>
                    ))}
                </Card>
                <Card
                    size="small"
                    title="Monitors"
                    extra={
                        <Button size="small" onClick={refreshMonitors}>
                            Refresh
                        </Button>
                    }
                >
                    <List
                        size="small"
                        dataSource={monitors}
                        locale={{ emptyText: "No monitors yet" }}
                        renderItem={(m) => (
                            <List.Item>
                                <div>
                                    <div>{m.name}</div>
                                    <Typography.Text type="secondary">
                                        every {Math.round(m.interval_seconds / 60)} min · {m.timezone} · {m.keywords.join(", ")}
                                    </Typography.Text>
                                </div>
                            </List.Item>
                        )}
                    />
                </Card>
            </div>
            <SourcesPanel workspaceId={workspaceId} />
            <ReportsPanel workspaceId={workspaceId} />
            <WorkspaceAdminPanel workspaceId={workspaceId} onWorkspacesChanged={() => void listIntelWorkspaces().then(setWorkspaces)} />
        </>
    );
}
