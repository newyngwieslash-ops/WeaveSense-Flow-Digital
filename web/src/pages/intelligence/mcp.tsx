import { useCallback, useEffect, useState } from "react";
import { Button, Card, Checkbox, Input, InputNumber, List, Select, Space, Tag, Typography, message } from "antd";

import {
    approveMcpConnection,
    createMcpConnection,
    discoverMcpTools,
    listIntelWorkspaces,
    listMcpCalls,
    listMcpConnections,
    resolveMcpCall,
    getMcpOAuth,
    revokeMcpOAuth,
    startMcpOAuth,
    revokeMcpConnection,
    type IntelWorkspace,
    type McpCall,
    type McpConnection,
    type McpTool,
} from "@/services/api/intelligence";

const STATUS_COLOR: Record<string, string> = { approved: "green", pending: "blue", needs_reapproval: "orange", revoked: "red" };

/** P4: managed MCP connections. Credentials are referenced by name only (env:FLOW_MCP_CRED_*, oauth:FLOW_MCP_OAUTH_* or interactive oauthcode:FLOW_MCP_OAUTHCODE_*); values never reach the browser. */
export default function McpConnectionsPage() {
    const [workspaces, setWorkspaces] = useState<IntelWorkspace[]>([]);
    const [workspaceId, setWorkspaceId] = useState<string>();
    const [rows, setRows] = useState<McpConnection[]>([]);
    const [form, setForm] = useState({ name: "", endpoint: "", credentialRef: "", maxCallsPerDay: 0, perCallCredits: 0 });
    const [review, setReview] = useState<{ id: string; tools: McpTool[]; picked: string[] }>();
    const [unknownCalls, setUnknownCalls] = useState<McpCall[]>([]);
    const [oauth, setOauth] = useState<Record<string, { connected: boolean; expiresAt?: string }>>({});

    useEffect(() => {
        void listIntelWorkspaces().then((list) => {
            setWorkspaces(list);
            setWorkspaceId((c) => c ?? list[0]?.id);
        });
    }, []);
    const refresh = useCallback(() => {
        if (workspaceId) {
            void listMcpConnections(workspaceId)
                .then((list) => {
                    setRows(list);
                    for (const c of list.filter((x) => x.credentialRef.startsWith("oauthcode:")))
                        void getMcpOAuth(workspaceId, c.id)
                            .then((st) => setOauth((cur) => ({ ...cur, [c.id]: st })))
                            .catch(() => undefined);
                })
                .catch(() => setRows([]));
            void listMcpCalls(workspaceId, "unknown")
                .then(setUnknownCalls)
                .catch(() => setUnknownCalls([]));
        }
    }, [workspaceId]);
    useEffect(refresh, [refresh]);

    const guard = async (fn: () => Promise<unknown>) => {
        try {
            await fn();
            refresh();
        } catch (err) {
            void message.error(err instanceof Error ? err.message : "request failed");
        }
    };

    return (
        <div style={{ display: "grid", gridTemplateColumns: "360px 1fr", gap: 16, padding: 16 }}>
            <Card size="small" title="New MCP connection">
                <Space direction="vertical" style={{ width: "100%" }}>
                    <Select style={{ width: "100%" }} value={workspaceId} onChange={setWorkspaceId} options={workspaces.map((w) => ({ value: w.id, label: w.name }))} />
                    <Input placeholder="Name" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
                    <Input placeholder="https://mcp.example.com/mcp (streamable HTTP)" value={form.endpoint} onChange={(e) => setForm({ ...form, endpoint: e.target.value })} />
                    <Input placeholder="Credential ref, e.g. env:FLOW_MCP_CRED_ACME" value={form.credentialRef} onChange={(e) => setForm({ ...form, credentialRef: e.target.value })} />
                    <Space>
                        <InputNumber min={0} addonBefore="max calls/day" value={form.maxCallsPerDay} onChange={(v) => setForm({ ...form, maxCallsPerDay: Number(v) || 0 })} />
                        <InputNumber min={0} addonBefore="credits/call" value={form.perCallCredits} onChange={(v) => setForm({ ...form, perCallCredits: Number(v) || 0 })} />
                    </Space>
                    <Button
                        type="primary"
                        disabled={!workspaceId || !form.endpoint}
                        onClick={() =>
                            void guard(() =>
                                createMcpConnection(workspaceId!, {
                                    name: form.name,
                                    endpoint: form.endpoint,
                                    credentialRef: form.credentialRef || undefined,
                                    costRule: { max_calls_per_day: form.maxCallsPerDay, per_call_credits: form.perCallCredits },
                                }),
                            )
                        }
                    >
                        Create (pending operator approval)
                    </Button>
                </Space>
            </Card>
            <Card size="small" title="Connections">
                <List
                    dataSource={rows}
                    locale={{ emptyText: "No MCP connections" }}
                    renderItem={(c) => (
                        <List.Item
                            actions={[
                                <Button key="review" size="small" disabled={c.status === "revoked"} onClick={() => void guard(async () => setReview({ id: c.id, tools: await discoverMcpTools(workspaceId!, c.id), picked: c.allowedTools ?? [] }))}>
                                    Review tools
                                </Button>,
                                <Button key="revoke" size="small" danger disabled={c.status === "revoked"} onClick={() => void guard(() => revokeMcpConnection(workspaceId!, c.id))}>
                                    Revoke
                                </Button>,
                                ...(c.credentialRef.startsWith("oauthcode:")
                                    ? [
                                          <Button
                                              key="oauth"
                                              size="small"
                                              onClick={() =>
                                                  void guard(async () => {
                                                      const { authorizeUrl } = await startMcpOAuth(workspaceId!, c.id);
                                                      window.location.assign(authorizeUrl);
                                                  })
                                              }
                                          >
                                              {oauth[c.id]?.connected ? "Re-authorize" : "Authorize"}
                                          </Button>,
                                          <Button key="oauth-revoke" size="small" disabled={!oauth[c.id]?.connected} onClick={() => void guard(() => revokeMcpOAuth(workspaceId!, c.id))}>
                                              Revoke OAuth
                                          </Button>,
                                      ]
                                    : []),
                            ]}
                        >
                            <div style={{ width: "100%" }}>
                                <Tag color={STATUS_COLOR[c.status]}>{c.status}</Tag>
                                <b>{c.name || c.endpoint}</b> v{c.version}
                                <div>
                                    <Typography.Text type="secondary">
                                        {c.endpoint} · cred {c.credentialRef || "none"} · {c.timeoutMs} ms · cap {c.outputCap} · allowed: {(c.allowedTools ?? []).join(", ") || "—"}
                                    </Typography.Text>
                                </div>
                                {c.statusReason && <Typography.Text type="warning">{c.statusReason}</Typography.Text>}
                                {review?.id === c.id && (
                                    <Card size="small" style={{ marginTop: 8 }} title="Operator approval: allowed tools (schema hashes are frozen)">
                                        <Checkbox.Group value={review.picked} onChange={(v) => setReview({ ...review, picked: v as string[] })}>
                                            <Space direction="vertical">
                                                {review.tools.map((t) => (
                                                    <Checkbox key={t.name} value={t.name}>
                                                        {t.name}{" "}
                                                        <Typography.Text type="secondary">
                                                            {t.description} · {t.hash.slice(0, 10)}
                                                        </Typography.Text>
                                                    </Checkbox>
                                                ))}
                                            </Space>
                                        </Checkbox.Group>
                                        <div style={{ marginTop: 8 }}>
                                            <Button type="primary" size="small" onClick={() => void guard(async () => (await approveMcpConnection(workspaceId!, c.id, review.picked), setReview(undefined)))}>
                                                Approve selected
                                            </Button>
                                        </div>
                                    </Card>
                                )}
                            </div>
                        </List.Item>
                    )}
                />
            </Card>
            <Card size="small" title={`Unknown-outcome calls (${unknownCalls.length})`} style={{ gridColumn: "1 / -1" }}>
                <Typography.Paragraph type="secondary">
                    Calls whose result was lost and could not be reconciled automatically. Check the remote system, then resolve: executed calls are charged once; not-executed calls may be retried under the same key.
                </Typography.Paragraph>
                <List
                    size="small"
                    dataSource={unknownCalls}
                    locale={{ emptyText: "No unresolved calls" }}
                    renderItem={(call) => (
                        <List.Item>
                            <Space wrap>
                                <Tag color="orange">{call.status}</Tag>
                                <span>{call.tool}</span>
                                <Typography.Text type="secondary">
                                    {call.idemKey} · attempts {call.attempts} · {call.updatedAt}
                                </Typography.Text>
                                <Button size="small" onClick={() => void guard(() => resolveMcpCall(workspaceId!, call.id, "succeeded"))}>
                                    Mark executed
                                </Button>
                                <Button size="small" onClick={() => void guard(() => resolveMcpCall(workspaceId!, call.id, "not_executed"))}>
                                    Mark not executed
                                </Button>
                            </Space>
                        </List.Item>
                    )}
                />
            </Card>
        </div>
    );
}
