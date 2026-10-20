import { AlertTriangle, CheckCircle2, Filter, GitBranch, Layers, Sparkles, XCircle } from "lucide-react";
import { useState } from "react";

import { useCanvasNodeActions } from "@/components/canvas/canvas-node-action-context";
import { useDataflowNodeResult } from "@/components/canvas/canvas-node-graph-context";
import type { CanvasTheme } from "@/lib/canvas-theme";
import {
    formatDataItemsForPrompt,
    LOGIC_COMPARATOR_LABELS,
    readNodeLogicConfig,
    type CanvasDataItem,
} from "@/lib/canvas/dataflow-evaluator";
import type { CanvasLogicComparator, CanvasLogicConfig, CanvasLogicOperator, CanvasNodeData } from "@/types/canvas";

type LogicNodeContentProps = {
    node: CanvasNodeData;
    theme: CanvasTheme;
};

const OPERATOR_TABS: Array<{ value: CanvasLogicOperator; shortLabel: string; title: string }> = [
    { value: "if_else", shortLabel: "If / Else", title: "按条件分流到 True / False 两个出口" },
    { value: "intersect", shortLabel: "交集 ∩", title: "取输入 A 与输入 B 的共有项 (A ∩ B)" },
    { value: "union", shortLabel: "并集 ∪", title: "合并输入 A 与输入 B 并去重 (A ∪ B)" },
    { value: "difference", shortLabel: "差集 −", title: "保留输入 A 中不在输入 B 里的项 (A − B)" },
    { value: "filter", shortLabel: "过滤", title: "仅保留满足条件的条目" },
    { value: "json_extract", shortLabel: "提取", title: "按字段路径提取属性值列表" },
];

const COMPARATOR_OPTIONS = Object.entries(LOGIC_COMPARATOR_LABELS) as Array<[CanvasLogicComparator, string]>;

function formatCompactItem(item: CanvasDataItem): string {
    if (typeof item === "string" || typeof item === "number" || typeof item === "boolean") {
        return String(item);
    }
    return JSON.stringify(item);
}

export function LogicNodeContent({ node, theme }: LogicNodeContentProps) {
    const { updateNode, updateMetadata } = useCanvasNodeActions();
    const result = useDataflowNodeResult(node);
    const config = readNodeLogicConfig(node);
    const [previewBranch, setPreviewBranch] = useState<"true" | "false">("true");

    const { operator, fieldPath = "", comparator = "contains", compareValue = "", matchKey = "" } = config;
    const isSetOp = operator === "intersect" || operator === "union" || operator === "difference";
    const isConditionOp = operator === "if_else" || operator === "filter";
    const hideCompareValue = comparator === "is_empty" || comparator === "not_empty";

    const patchLogic = (patch: Partial<CanvasLogicConfig>) => {
        const nextLogic: CanvasLogicConfig = {
            ...config,
            ...patch,
        };
        const previewText = formatDataItemsForPrompt(result.outputItems);
        if (updateNode) {
            updateNode(node.id, (current) => ({
                ...current,
                metadata: {
                    ...current.metadata,
                    content: previewText,
                    logic: {
                        ...readNodeLogicConfig(current),
                        ...patch,
                    },
                },
            }));
            return;
        }
        updateMetadata?.(node.id, {
            content: previewText,
            logic: nextLogic,
        });
    };

    const activePreviewItems =
        operator === "if_else"
            ? previewBranch === "false"
                ? result.falseItems
                : result.trueItems
            : result.outputItems;

    return (
        <div
            data-canvas-no-zoom
            data-canvas-wheel-scroll
            className="flex h-full w-full flex-col overflow-hidden p-3 pt-4 text-xs select-none"
            style={{ background: theme.node.fill, color: theme.node.text }}
            onMouseDown={(event) => event.stopPropagation()}
            onPointerDown={(event) => event.stopPropagation()}
            onWheel={(event) => event.stopPropagation()}
        >
            {/* 顶栏：算子切换 */}
            <div className="flex flex-wrap items-center gap-1 pb-2" style={{ borderBottom: `1px solid ${theme.node.edge}` }} role="tablist" aria-label="逻辑算子类型">
                <GitBranch className="mr-0.5 size-3.5 shrink-0" style={{ color: theme.accent.primary }} />
                {OPERATOR_TABS.map((tab) => {
                    const active = operator === tab.value;
                    return (
                        <button
                            key={tab.value}
                            type="button"
                            aria-pressed={active}
                            title={tab.title}
                            className="rounded-[var(--r-sm)] px-1.5 py-0.5 text-[var(--fs-tiny)] font-medium transition-colors"
                            style={{
                                background: active ? theme.accent.primary : theme.node.panel,
                                color: active ? theme.accent.onPrimary : theme.node.muted,
                            }}
                            onClick={() => patchLogic({ operator: tab.value })}
                        >
                            {tab.shortLabel}
                        </button>
                    );
                })}
            </div>

            {/* 环路或错误提示 */}
            {result.error ? (
                <div
                    className="mt-2 flex items-center gap-1.5 rounded-[var(--r-sm)] px-2.5 py-1.5 text-[var(--fs-tiny)]"
                    style={{ background: "color-mix(in srgb, var(--status-error, #ef4444) 14%, transparent)", color: "var(--status-error, #ef4444)" }}
                >
                    <AlertTriangle className="size-3.5 shrink-0" />
                    <span className="truncate">{result.error}</span>
                </div>
            ) : null}

            {/* 规则表单区 */}
            <div className="mt-2 flex flex-col gap-1.5 rounded-[var(--r-md)] border p-2" style={{ background: theme.node.panel, borderColor: theme.node.edge }}>
                {isSetOp ? (
                    <>
                        <div className="flex items-center justify-between gap-2 text-[var(--fs-tiny)]">
                            <span className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 font-medium" style={{ background: theme.node.fill, color: theme.node.text }}>
                                <Layers className="size-3" style={{ color: theme.accent.primary }} />
                                端口 A: {result.inputA.length} 条
                            </span>
                            <span className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 font-medium" style={{ background: theme.node.fill, color: theme.node.text }}>
                                <Layers className="size-3" style={{ color: theme.accent.primary }} />
                                端口 B: {result.inputB.length} 条
                            </span>
                        </div>
                        <div className="flex items-center gap-1.5">
                            <label className="shrink-0 text-[var(--fs-tiny)]" style={{ color: theme.node.muted }}>
                                匹配键
                            </label>
                            <input
                                type="text"
                                aria-label="集合匹配键"
                                placeholder="留空按整行比对，或填字段如 id / title"
                                value={matchKey}
                                onChange={(event) => patchLogic({ matchKey: event.target.value })}
                                className="h-6 min-w-0 flex-1 rounded-[var(--r-sm)] border px-2 font-mono text-[var(--fs-tiny)] outline-none"
                                style={{ background: theme.node.fill, borderColor: theme.node.edge, color: theme.node.text }}
                            />
                        </div>
                    </>
                ) : null}

                {isConditionOp ? (
                    <div className="grid grid-cols-12 gap-1.5">
                        <input
                            type="text"
                            aria-label="条件字段路径"
                            placeholder="字段 (如 score 或留空)"
                            value={fieldPath}
                            onChange={(event) => patchLogic({ fieldPath: event.target.value })}
                            className="col-span-4 h-6 rounded-[var(--r-sm)] border px-2 font-mono text-[var(--fs-tiny)] outline-none"
                            style={{ background: theme.node.fill, borderColor: theme.node.edge, color: theme.node.text }}
                        />
                        <select
                            aria-label="条件比较符"
                            value={comparator}
                            onChange={(event) => patchLogic({ comparator: event.target.value as CanvasLogicComparator })}
                            className={`${hideCompareValue ? "col-span-8" : "col-span-4"} h-6 rounded-[var(--r-sm)] border px-1.5 text-[var(--fs-tiny)] outline-none`}
                            style={{ background: theme.node.fill, borderColor: theme.node.edge, color: theme.node.text }}
                        >
                            {COMPARATOR_OPTIONS.map(([val, label]) => (
                                <option key={val} value={val}>
                                    {label}
                                </option>
                            ))}
                        </select>
                        {!hideCompareValue ? (
                            <input
                                type="text"
                                aria-label="条件比较值"
                                placeholder="比较值"
                                value={compareValue}
                                onChange={(event) => patchLogic({ compareValue: event.target.value })}
                                className="col-span-4 h-6 rounded-[var(--r-sm)] border px-2 font-mono text-[var(--fs-tiny)] outline-none"
                                style={{ background: theme.node.fill, borderColor: theme.node.edge, color: theme.node.text }}
                            />
                        ) : null}
                    </div>
                ) : null}

                {operator === "json_extract" ? (
                    <div className="flex items-center gap-1.5">
                        <label className="shrink-0 text-[var(--fs-tiny)]" style={{ color: theme.node.muted }}>
                            提取路径
                        </label>
                        <input
                            type="text"
                            aria-label="提取字段路径"
                            placeholder="字段路径，如 title 或 meta.author"
                            value={fieldPath}
                            onChange={(event) => patchLogic({ fieldPath: event.target.value })}
                            className="h-6 min-w-0 flex-1 rounded-[var(--r-sm)] border px-2 font-mono text-[var(--fs-tiny)] outline-none"
                            style={{ background: theme.node.fill, borderColor: theme.node.edge, color: theme.node.text }}
                        />
                    </div>
                ) : null}

                {/* 上游检测到的字段快捷点选 */}
                {result.detectedKeys.length > 0 ? (
                    <div className="flex flex-wrap items-center gap-1 pt-0.5">
                        <span className="text-[10px] opacity-55">可用字段:</span>
                        {result.detectedKeys.slice(0, 6).map((key) => {
                            const selectedKey = isSetOp ? matchKey === key : fieldPath === key;
                            return (
                                <button
                                    key={key}
                                    type="button"
                                    className="rounded px-1.5 py-0.2 font-mono text-[10px] transition-opacity hover:opacity-100"
                                    style={{
                                        background: selectedKey ? `${theme.accent.primary}28` : theme.node.fill,
                                        color: selectedKey ? theme.accent.primary : theme.node.muted,
                                        border: `1px solid ${selectedKey ? theme.accent.primary : theme.node.edge}`,
                                    }}
                                    onClick={() => (isSetOp ? patchLogic({ matchKey: key }) : patchLogic({ fieldPath: key }))}
                                >
                                    {key}
                                </button>
                            );
                        })}
                    </div>
                ) : null}
            </div>

            {/* 输出预览与分支状态栏 */}
            <div className="mt-2 flex items-center justify-between gap-1.5 text-[var(--fs-tiny)]">
                {operator === "if_else" ? (
                    <div className="flex w-full items-center justify-between gap-1.5">
                        <button
                            type="button"
                            aria-pressed={previewBranch === "true"}
                            onClick={() => setPreviewBranch("true")}
                            className="inline-flex flex-1 items-center justify-between rounded-[var(--r-sm)] border px-2 py-1 font-medium transition-colors"
                            style={{
                                background: result.trueItems.length > 0 ? "rgba(34, 197, 94, 0.14)" : theme.node.panel,
                                borderColor: previewBranch === "true" ? "#22c55e" : theme.node.edge,
                                color: result.trueItems.length > 0 ? "#22c55e" : theme.node.muted,
                            }}
                        >
                            <span className="inline-flex items-center gap-1">
                                <CheckCircle2 className="size-3" />
                                True 出口
                            </span>
                            <span className="font-mono font-semibold">{result.trueItems.length} 条</span>
                        </button>
                        <button
                            type="button"
                            aria-pressed={previewBranch === "false"}
                            onClick={() => setPreviewBranch("false")}
                            className="inline-flex flex-1 items-center justify-between rounded-[var(--r-sm)] border px-2 py-1 font-medium transition-colors"
                            style={{
                                background: result.falseItems.length > 0 ? "rgba(245, 158, 11, 0.14)" : theme.node.panel,
                                borderColor: previewBranch === "false" ? "#f59e0b" : theme.node.edge,
                                color: result.falseItems.length > 0 ? "#f59e0b" : theme.node.muted,
                            }}
                        >
                            <span className="inline-flex items-center gap-1">
                                <XCircle className="size-3" />
                                False 出口
                            </span>
                            <span className="font-mono font-semibold">{result.falseItems.length} 条</span>
                        </button>
                    </div>
                ) : (
                    <div className="flex w-full items-center justify-between">
                        <span className="inline-flex items-center gap-1 font-medium" style={{ color: theme.node.muted }}>
                            <Filter className="size-3" />
                            输入 {result.inputA.length + result.inputB.length} 条
                        </span>
                        <span
                            className="inline-flex items-center gap-1 rounded-[var(--r-sm)] px-1.5 py-0.5 font-semibold tabular-nums"
                            style={{ background: `${theme.accent.primary}1f`, color: theme.accent.primary }}
                        >
                            <Sparkles className="size-3" />
                            输出 {result.outputItems.length} 条
                        </span>
                    </div>
                )}
            </div>

            {/* 实时求值结果列表 */}
            <div
                className="thin-scrollbar mt-1.5 min-h-0 flex-1 overflow-y-auto rounded-[var(--r-md)] border p-1.5"
                style={{ background: theme.node.panel, borderColor: theme.node.edge }}
            >
                {activePreviewItems.length > 0 ? (
                    <div className="flex flex-col gap-1">
                        {activePreviewItems.slice(0, 30).map((item, idx) => (
                            <div
                                key={idx}
                                className="flex items-start gap-1.5 rounded-[var(--r-sm)] px-2 py-1 font-mono text-[var(--fs-tiny)]"
                                style={{ background: theme.node.fill, border: `1px solid ${theme.node.edge}` }}
                            >
                                <span className="shrink-0 opacity-45">#{idx + 1}</span>
                                <span className="min-w-0 flex-1 break-all select-text">{formatCompactItem(item)}</span>
                            </div>
                        ))}
                    </div>
                ) : (
                    <div className="flex h-full items-center justify-center text-center text-[var(--fs-tiny)]" style={{ color: theme.node.muted }}>
                        {result.inputA.length + result.inputB.length === 0
                            ? isSetOp
                                ? "请连接左侧端口 A 与端口 B 的上游数据源"
                                : "请连接左侧上游数据源或文本节点"
                            : operator === "if_else"
                              ? `${previewBranch === "true" ? "True" : "False"} 分支当前无命中数据（下游将自动跳过）`
                              : "当前条件无匹配结果"}
                    </div>
                )}
            </div>
        </div>
    );
}
