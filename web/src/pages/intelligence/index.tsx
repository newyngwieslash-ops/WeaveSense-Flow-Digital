import { useCallback, useEffect, useState } from "react";
import { Button, Card, Input, List, Select, Space, Tag, Typography, message } from "antd";
import { EmptyState } from "@/components/ui/product/empty-state/empty-state";

import {
    cachedConversations,
    callTool,
    createConversation,
    decideApproval,
    listConversations,
    listIntelWorkspaces,
    listMessages,
    listMonitors,
    postMessage,
    type FlowConversation,
    type FlowMessage,
    type IntelMonitor,
    type IntelWorkspace,
    type ToolResult,
} from "@/services/api/intelligence";

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

    const send = async () => {
        if (!text.trim()) return;
        const conv = await ensureConversation();
        await postMessage(conv.id, text.trim());
        setText("");
        await refreshMessages(conv);
    };

    const runTool = async (name: string, args: Record<string, unknown>) => {
        const conv = await ensureConversation();
        const result = await callTool(conv.id, newCallId(), name, args);
        if (result.status === "waiting_approval") setPending((p) => [...p, result]);
        if (result.status === "failed") void message.error(result.error || "tool failed");
        await refreshMessages(conv);
        return result;
    };

    const decide = async (r: ToolResult, approve: boolean) => {
        if (!r.approvalId) return;
        await decideApproval(r.approvalId, approve);
        setPending((p) => p.filter((x) => x.approvalId !== r.approvalId));
        refreshMonitors();
        if (active) await refreshMessages(active);
    };

    return (
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
                </div>
                <Space.Compact style={{ width: "100%" }}>
                    <Input value={text} onChange={(e) => setText(e.target.value)} onPressEnter={() => void send()} placeholder="Message" />
                    <Button type="primary" onClick={() => void send()}>
                        Send
                    </Button>
                    <Button onClick={() => void runTool("intel_search_documents", { query: text })} disabled={!text.trim()}>
                        Search
                    </Button>
                </Space.Compact>
                {pending.map((p) => (
                    <Card key={p.approvalId} size="small" style={{ marginTop: 8 }} title={`Approval needed: ${p.tool}`}>
                        <Space>
                            <Button type="primary" onClick={() => void decide(p, true)}>
                                Approve
                            </Button>
                            <Button danger onClick={() => void decide(p, false)}>
                                Reject
                            </Button>
                        </Space>
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
    );
}
