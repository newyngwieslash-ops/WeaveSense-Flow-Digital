import { useCallback, useEffect, useState } from "react";
import { Button, Card, Form, Input, List, Select, Tag, message } from "antd";
import { AppModal } from "@/components/ui/product/app-modal/app-modal";

import { createTopic, listSources, listTopics, updateTopic, type IntelSource, type IntelTopic, type TopicInput } from "@/services/api/intelligence";
import { buildTopicHandoffDraft, CanvasHandoffModal, type CanvasHandoffDraft } from "./canvas-handoff-modal";

/** PRD §5.2 可保存专题: list, create and edit saved research topics; opening one starts a topic-scoped conversation. */
export function TopicsPanel({
    workspaceId,
    onOpen,
    initialTopics = [],
    onNavigate,
}: {
    workspaceId?: string;
    onOpen?: (t: IntelTopic) => void;
    initialTopics?: IntelTopic[];
    onNavigate?: (url: string) => void;
}) {
    const [topics, setTopics] = useState<IntelTopic[]>(initialTopics);
    const [sources, setSources] = useState<IntelSource[]>([]);
    const [editing, setEditing] = useState<IntelTopic | "new">();
    const [handoffDraft, setHandoffDraft] = useState<CanvasHandoffDraft>();
    const [form] = Form.useForm<{ title: string; question: string; keywords: string[]; sourceIds: string[]; notes: string }>();
    const refresh = useCallback(() => {
        if (!workspaceId) return;
        void listTopics(workspaceId)
            .then(setTopics)
            .catch(() => setTopics([]));
        void listSources(workspaceId)
            .then(setSources)
            .catch(() => setSources([]));
    }, [workspaceId]);
    useEffect(refresh, [refresh]);
    const open = (t: IntelTopic | "new") => {
        setEditing(t);
        form.setFieldsValue(t === "new" ? { title: "", question: "", keywords: [], sourceIds: [], notes: "" } : { title: t.title, question: t.question, keywords: t.keywords, sourceIds: t.sourceIds, notes: t.notes });
    };
    const save = async () => {
        if (!workspaceId || !editing) return;
        const v = await form.validateFields();
        const input: TopicInput = { title: v.title, question: v.question, keywords: v.keywords, sourceIds: v.sourceIds, notes: v.notes };
        try {
            if (editing === "new") await createTopic(workspaceId, input);
            else await updateTopic(workspaceId, editing.id, input, editing.version);
            setEditing(undefined);
            refresh();
        } catch (e) {
            const status = (e as { status?: number }).status;
            void message.error(status === 409 ? "专题已被他人修改，已刷新" : status === 403 ? "没有编辑权限" : "保存失败");
            refresh();
        }
    };
    return (
        <Card
            size="small"
            title="专题"
            extra={
                <Button size="small" onClick={() => open("new")}>
                    新建
                </Button>
            }
        >
            <List
                size="small"
                dataSource={topics}
                locale={{ emptyText: "还没有保存的专题" }}
                renderItem={(t) => (
                    <List.Item
                        actions={[
                            <Button key="handoff" size="small" type="link" onClick={() => setHandoffDraft(buildTopicHandoffDraft(t))}>
                                投送到画布
                            </Button>,
                            <Button key="edit" size="small" type="link" onClick={() => open(t)}>
                                编辑
                            </Button>,
                            ...(onOpen
                                ? [
                                      <Button key="open" size="small" type="link" onClick={() => onOpen(t)}>
                                          对话
                                      </Button>,
                                  ]
                                : []),
                        ]}
                    >
                        <div>
                            <div>{t.title}</div>
                            {t.keywords.map((k) => (
                                <Tag key={k}>{k}</Tag>
                            ))}
                        </div>
                    </List.Item>
                )}
            />
            <AppModal open={!!editing} title={editing === "new" ? "新建专题" : "编辑专题"} onOk={() => void save()} onCancel={() => setEditing(undefined)} okText="保存" cancelText="取消">
                <Form form={form} layout="vertical">
                    <Form.Item name="title" label="标题" rules={[{ required: true, max: 200 }]}>
                        <Input />
                    </Form.Item>
                    <Form.Item name="question" label="研究问题">
                        <Input.TextArea rows={2} />
                    </Form.Item>
                    <Form.Item name="keywords" label="关键词">
                        <Select mode="tags" />
                    </Form.Item>
                    <Form.Item name="sourceIds" label="来源范围">
                        <Select mode="multiple" options={sources.map((s) => ({ value: s.id, label: s.title || s.url }))} />
                    </Form.Item>
                    <Form.Item name="notes" label="备注">
                        <Input.TextArea rows={3} />
                    </Form.Item>
                </Form>
            </AppModal>
            <CanvasHandoffModal workspaceId={workspaceId} draft={handoffDraft} onClose={() => setHandoffDraft(undefined)} onNavigate={onNavigate} />
        </Card>
    );
}
