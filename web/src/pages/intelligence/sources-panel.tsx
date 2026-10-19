import { useCallback, useEffect, useState } from "react";
import { Button, Card, Input, List, Select, Space, Tag, Typography, message } from "antd";

import { createSource, listConnectorSamples, listSources, revokeSource, type ConnectorSample, type IntelSource, type SourceKind } from "@/services/api/intelligence";

const KIND_HINT: Record<SourceKind, string> = {
    rss: "订阅源 URL（https://…）",
    telegram: "公开频道句柄或 t.me 链接",
    wechat: "微信网关源 ID（已在公众号采集网关配置）",
};

/** P4: sources of type rss / telegram / wechat, revoke, and connector acceptance samples. */
export function SourcesPanel({ workspaceId }: { workspaceId?: string }) {
    const [sources, setSources] = useState<IntelSource[]>([]);
    const [samples, setSamples] = useState<ConnectorSample[]>([]);
    const [kind, setKind] = useState<SourceKind>("rss");
    const [value, setValue] = useState("");

    const refresh = useCallback(() => {
        if (!workspaceId) return;
        void listSources(workspaceId)
            .then(setSources)
            .catch(() => setSources([]));
        void listConnectorSamples(workspaceId)
            .then(setSamples)
            .catch(() => setSamples([]));
    }, [workspaceId]);
    useEffect(refresh, [refresh]);

    const add = async () => {
        if (!workspaceId || !value.trim()) return;
        const v = value.trim();
        const input = kind === "rss" ? { kind, url: v } : kind === "telegram" ? { kind, config: { handle: v } } : { kind, config: { gateway_source_id: v } };
        try {
            await createSource(workspaceId, input);
            setValue("");
            refresh();
        } catch (err) {
            void message.error(err instanceof Error ? err.message : "添加信源失败");
        }
    };

    return (
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 16, padding: "0 16px 16px" }}>
            <Card size="small" title="信源列表">
                <Space.Compact style={{ width: "100%", marginBottom: 8 }}>
                    <Select
                        style={{ width: 120 }}
                        value={kind}
                        onChange={setKind}
                        options={[
                            { value: "rss", label: "RSS" },
                            { value: "telegram", label: "Telegram" },
                            { value: "wechat", label: "微信公众号" },
                        ]}
                    />
                    <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder={KIND_HINT[kind]} onPressEnter={() => void add()} />
                    <Button type="primary" onClick={() => void add()}>
                        添加
                    </Button>
                </Space.Compact>
                {kind === "telegram" && <Typography.Text type="secondary">支持公开频道消息与受管大小内的媒体附件采集；超限附件将标记为超限跳过。</Typography.Text>}
                <List
                    size="small"
                    dataSource={sources}
                    locale={{ emptyText: "暂无信源" }}
                    renderItem={(s) => (
                        <List.Item
                            actions={
                                s.status === "active" && workspaceId
                                    ? [
                                          <Button key="revoke" size="small" danger onClick={() => void revokeSource(workspaceId, s.id).then(refresh)}>
                                              停用
                                          </Button>,
                                      ]
                                    : []
                            }
                        >
                            <div>
                                <Tag>{s.kind}</Tag>
                                {s.title || s.url} {s.status !== "active" && <Tag color="red">{s.status}</Tag>}
                                {s.last_error && <Typography.Text type="danger"> · {s.last_error}</Typography.Text>}
                            </div>
                        </List.Item>
                    )}
                />
            </Card>
            <Card size="small" title="连接器采样验收">
                <List
                    size="small"
                    dataSource={samples}
                    locale={{ emptyText: "暂无 Telegram / 微信公众号采集记录" }}
                    renderItem={(s) => (
                        <List.Item>
                            <div>
                                <Tag>{s.kind}</Tag>
                                <Tag color={s.mode === "live" ? "green" : "default"}>{s.mode === "live" ? "实采" : "样本"}</Tag>
                                采集 {s.items} 条（通过 {s.collector}
                                {s.provider ? ` / ${s.provider}` : ""}）
                                {s.media_unsupported > 0 && <Tag color="gold">{s.media_unsupported} 个超限/不支持附件</Tag>}
                                <div>
                                    <Typography.Text type="secondary">{new Date(s.recorded_at).toLocaleString()}</Typography.Text>
                                </div>
                            </div>
                        </List.Item>
                    )}
                />
            </Card>
        </div>
    );
}
