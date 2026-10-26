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
const STATUS_LABEL: Record<string, string> = { approved: "已批准", pending: "待审批", needs_reapproval: "需重审", revoked: "已吊销" };

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
            void message.error(err instanceof Error ? err.message : "请求失败");
        }
    };

    return (
        <div style={{ display: "grid", gridTemplateColumns: "360px 1fr", gap: 16, padding: 16 }}>
            <Card size="small" title="新建 MCP 连接">
                <Space direction="vertical" style={{ width: "100%" }}>
                    <Select style={{ width: "100%" }} value={workspaceId} onChange={setWorkspaceId} options={workspaces.map((w) => ({ value: w.id, label: w.name }))} />
                    <Input placeholder="连接名称" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
                    <Input placeholder="https://mcp.example.com/mcp（Streamable HTTP）" value={form.endpoint} onChange={(e) => setForm({ ...form, endpoint: e.target.value })} />
                    <Input placeholder="凭据引用（如 env:FLOW_MCP_CRED_ACME）" value={form.credentialRef} onChange={(e) => setForm({ ...form, credentialRef: e.target.value })} />
                    <Space>
                        <InputNumber min={0} addonBefore="每日上限" value={form.maxCallsPerDay} onChange={(v) => setForm({ ...form, maxCallsPerDay: Number(v) || 0 })} />
                        <InputNumber min={0} addonBefore="单次积分" value={form.perCallCredits} onChange={(v) => setForm({ ...form, perCallCredits: Number(v) || 0 })} />
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
                        创建（待管理员审批）
                    </Button>
                </Space>
            </Card>
            <Card size="small" title="已配置连接">
                <List
                    dataSource={rows}
                    locale={{ emptyText: "暂无 MCP 连接" }}
                    renderItem={(c) => (
                        <List.Item
                            actions={[
                                <Button key="review" size="small" disabled={c.status === "revoked"} onClick={() => void guard(async () => setReview({ id: c.id, tools: await discoverMcpTools(workspaceId!, c.id), picked: c.allowedTools ?? [] }))}>
                                    审查工具
                                </Button>,
                                <Button key="revoke" size="small" danger disabled={c.status === "revoked"} onClick={() => void guard(() => revokeMcpConnection(workspaceId!, c.id))}>
                                    吊销
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
                                              {oauth[c.id]?.connected ? "重新授权" : "OAuth 授权"}
                                          </Button>,
                                          <Button key="oauth-revoke" size="small" disabled={!oauth[c.id]?.connected} onClick={() => void guard(() => revokeMcpOAuth(workspaceId!, c.id))}>
                                              撤销授权
                                          </Button>,
                                      ]
                                    : []),
                            ]}
                        >
                            <div style={{ width: "100%" }}>
                                <Tag color={STATUS_COLOR[c.status]}>{STATUS_LABEL[c.status] ?? c.status}</Tag>
                                <b>{c.name || c.endpoint}</b> v{c.version}
                                <div>
                                    <Typography.Text type="secondary">
                                        {c.endpoint} · 凭据 {c.credentialRef || "无"} · 超时 {c.timeoutMs} ms · 输出上限 {c.outputCap} · 已放行工具：{(c.allowedTools ?? []).join(", ") || "—"}
                                    </Typography.Text>
                                </div>
                                {c.statusReason && <Typography.Text type="warning">{c.statusReason}</Typography.Text>}
                                {review?.id === c.id && (
                                    <Card size="small" style={{ marginTop: 8 }} title="管理员审批：选择允许调用的工具（Schema 哈希已锁定）">
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
                                                批准所选工具
                                            </Button>
                                        </div>
                                    </Card>
                                )}
                            </div>
                        </List.Item>
                    )}
                />
            </Card>
            <Card size="small" title={`结果未决调用（${unknownCalls.length}）`} style={{ gridColumn: "1 / -1" }}>
                <Typography.Paragraph type="secondary">以下调用因网络或远端中断未能自动对账。请核实远端系统后人工确认：标记「已执行」将按单次计费并固化结果；标记「未执行」后允许使用相同幂等键重试。</Typography.Paragraph>
                <List
                    size="small"
                    dataSource={unknownCalls}
                    locale={{ emptyText: "暂无未决调用" }}
                    renderItem={(call) => (
                        <List.Item>
                            <Space wrap>
                                <Tag color="orange">{call.status}</Tag>
                                <span>{call.tool}</span>
                                <Typography.Text type="secondary">
                                    {call.idemKey} · 重试 {call.attempts} 次 · {call.updatedAt}
                                </Typography.Text>
                                <Button size="small" onClick={() => void guard(() => resolveMcpCall(workspaceId!, call.id, "succeeded"))}>
                                    标记已执行
                                </Button>
                                <Button size="small" onClick={() => void guard(() => resolveMcpCall(workspaceId!, call.id, "not_executed"))}>
                                    标记未执行
                                </Button>
                            </Space>
                        </List.Item>
                    )}
                />
            </Card>
        </div>
    );
}
