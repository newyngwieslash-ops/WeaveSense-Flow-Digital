import { useEffect, useState } from "react";
import { App, Form, Input, InputNumber, Modal, Select, message } from "antd";

import { updateMonitor, type IntelMonitor, type MonitorUpdate } from "@/services/api/intelligence";

const EXPANSION_LABEL: Record<string, string> = { sources: "扩大来源", budget: "提高预算", frequency: "提高频率", notification_targets: "改变发送目标" };

/** Parses "reapproval_required: sources,budget" from the server's 409 message. */
export function expansionFromError(e: unknown): string[] | undefined {
    const msg = (e as { message?: string }).message ?? "";
    const m = /reapproval_required:\s*([\w,]+)/.exec(msg);
    return m ? m[1].split(",").filter(Boolean) : undefined;
}

/** PRD §5.1 monitor edit: narrowing applies directly; widening sources/budget/frequency/targets asks for an explicit new decision. */
export function MonitorEditor({ workspaceId, monitor, onClose }: { workspaceId?: string; monitor?: IntelMonitor; onClose: (changed: boolean) => void }) {
    const [form] = Form.useForm<{ keywords: string[]; interval_minutes: number; budget_credits: number; timezone: string }>();
    const [saving, setSaving] = useState(false);
    const { modal } = App.useApp();
    useEffect(() => {
        if (monitor)
            form.setFieldsValue({
                keywords: monitor.keywords,
                interval_minutes: Math.round(monitor.interval_seconds / 60),
                budget_credits: (monitor.budget_micro_per_window ?? 0) / 1_000_000,
                timezone: monitor.timezone,
            });
    }, [monitor, form]);
    const submit = async () => {
        if (!workspaceId || !monitor) return;
        const v = await form.validateFields();
        const input: MonitorUpdate = {
            keywords: v.keywords,
            interval_seconds: Math.round(v.interval_minutes * 60),
            budget_micro_per_window: Math.round(v.budget_credits * 1_000_000),
            timezone: v.timezone,
            expected_version: monitor.version,
        };
        setSaving(true);
        try {
            await updateMonitor(workspaceId, monitor.id, input);
            onClose(true);
        } catch (e) {
            const expansion = expansionFromError(e);
            if (!expansion) {
                void message.error((e as { status?: number }).status === 409 ? "监控已被修改，请重新打开" : "保存失败");
                return;
            }
            modal.confirm({
                title: "需要重新决策",
                content: `此修改会${expansion.map((x) => EXPANSION_LABEL[x] ?? x).join("、")}，超出原先批准的范围。确认批准这次变更吗？`,
                okText: "确认批准",
                cancelText: "取消",
                onOk: async () => {
                    await updateMonitor(workspaceId, monitor.id, input, `mon_${monitor.id}_${Date.now().toString(36)}`);
                    onClose(true);
                },
            });
        } finally {
            setSaving(false);
        }
    };
    return (
        <Modal open={!!monitor} title={`编辑监控：${monitor?.name ?? ""}`} confirmLoading={saving} onOk={() => void submit()} onCancel={() => onClose(false)} okText="保存" cancelText="取消">
            <Form form={form} layout="vertical">
                <Form.Item name="keywords" label="关键词" rules={[{ required: true }]}>
                    <Select mode="tags" />
                </Form.Item>
                <Form.Item name="interval_minutes" label="采集间隔（分钟，≥5）" rules={[{ required: true }]}>
                    <InputNumber min={5} />
                </Form.Item>
                <Form.Item name="budget_credits" label="每窗口费用上限（积分）" rules={[{ required: true }]}>
                    <InputNumber min={0} step={0.01} />
                </Form.Item>
                <Form.Item name="timezone" label="时区" rules={[{ required: true }]}>
                    <Input />
                </Form.Item>
            </Form>
        </Modal>
    );
}
