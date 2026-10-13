import { useCallback, useEffect, useState } from "react";
import { Button, Card, Input, List, Select, Space, Tag, Typography, message } from "antd";

import { createSource, listConnectorSamples, listSources, revokeSource, type ConnectorSample, type IntelSource, type SourceKind } from "@/services/api/intelligence";

const KIND_HINT: Record<SourceKind, string> = {
    rss: "Feed URL (https://…)",
    telegram: "Channel handle or t.me link (public channel)",
    wechat: "Gateway feed id (configured in the WeChat auth gateway)",
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
            void message.error(err instanceof Error ? err.message : "create failed");
        }
    };

    return (
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 16, padding: "0 16px 16px" }}>
            <Card size="small" title="Sources">
                <Space.Compact style={{ width: "100%", marginBottom: 8 }}>
                    <Select
                        style={{ width: 120 }}
                        value={kind}
                        onChange={setKind}
                        options={[
                            { value: "rss", label: "RSS" },
                            { value: "telegram", label: "Telegram" },
                            { value: "wechat", label: "WeChat OA" },
                        ]}
                    />
                    <Input value={value} onChange={(e) => setValue(e.target.value)} placeholder={KIND_HINT[kind]} onPressEnter={() => void add()} />
                    <Button type="primary" onClick={() => void add()}>
                        Add
                    </Button>
                </Space.Compact>
                {kind === "telegram" && <Typography.Text type="secondary">Text only: media attachments are marked unsupported and not downloaded.</Typography.Text>}
                <List
                    size="small"
                    dataSource={sources}
                    locale={{ emptyText: "No sources" }}
                    renderItem={(s) => (
                        <List.Item
                            actions={
                                s.status === "active" && workspaceId
                                    ? [
                                          <Button key="revoke" size="small" danger onClick={() => void revokeSource(workspaceId, s.id).then(refresh)}>
                                              Revoke
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
            <Card size="small" title="Connector acceptance samples">
                <List
                    size="small"
                    dataSource={samples}
                    locale={{ emptyText: "No Telegram/WeChat collection recorded yet" }}
                    renderItem={(s) => (
                        <List.Item>
                            <div>
                                <Tag>{s.kind}</Tag>
                                <Tag color={s.mode === "live" ? "green" : "default"}>{s.mode}</Tag>
                                {s.items} items via {s.collector}
                                {s.provider ? ` (${s.provider})` : ""}
                                {s.media_unsupported > 0 && <Tag color="gold">{s.media_unsupported} media unsupported</Tag>}
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
