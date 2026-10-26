import { AlertCircle, Code2, Database, FileUp, TableProperties } from "lucide-react";
import { useRef, useState, type ChangeEvent } from "react";

import { useCanvasNodeActions } from "@/components/canvas/canvas-node-action-context";
import { useDataflowNodeResult } from "@/components/canvas/canvas-node-graph-context";
import type { CanvasTheme } from "@/lib/canvas-theme";
import { DATA_SOURCE_FORMAT_LABELS, detectDataSourceFormatFromFilename, type CanvasDataItem } from "@/lib/canvas/dataflow-evaluator";
import type { CanvasDataSourceConfig, CanvasDataSourceFormat, CanvasNodeData } from "@/types/canvas";

type DataSourceNodeContentProps = {
    node: CanvasNodeData;
    theme: CanvasTheme;
};

const FORMAT_OPTIONS: CanvasDataSourceFormat[] = ["json", "csv", "txt", "markdown", "intel"];

const FORMAT_PLACEHOLDERS: Record<CanvasDataSourceFormat, string> = {
    json: '[\n  { "id": "1", "title": "条目 A", "score": 92 },\n  { "id": "2", "title": "条目 B", "score": 68 }\n]',
    csv: "id,title,score\n1,条目 A,92\n2,条目 B,68",
    txt: "每行一条文本记录\n第二条文本记录",
    markdown: "# 章节一\n第一部分内容\n\n# 章节二\n第二部分内容",
    intel: '[\n  { "id": "intel-1", "title": "行业动态", "tags": ["AI", "短剧"] }\n]',
};

function formatPreviewItem(item: CanvasDataItem): string {
    if (typeof item === "string" || typeof item === "number" || typeof item === "boolean") {
        return String(item);
    }
    return JSON.stringify(item);
}

export function DataSourceNodeContent({ node, theme }: DataSourceNodeContentProps) {
    const { updateNode, updateMetadata } = useCanvasNodeActions();
    const result = useDataflowNodeResult(node);
    const fileInputRef = useRef<HTMLInputElement | null>(null);
    const [viewMode, setViewMode] = useState<"edit" | "preview">("edit");

    const ds = node.metadata?.dataSource;
    const format: CanvasDataSourceFormat = ds?.format || result.format || "json";
    const rawText = ds?.rawText ?? node.metadata?.content ?? "";
    const fileName = ds?.fileName || "";

    const patchDataSource = (patch: Partial<CanvasDataSourceConfig>, nextContent?: string) => {
        const mergedConfig: CanvasDataSourceConfig = {
            format,
            rawText,
            ...(fileName ? { fileName } : {}),
            ...patch,
        };
        const contentValue = nextContent !== undefined ? nextContent : (mergedConfig.rawText ?? rawText);
        if (updateNode) {
            updateNode(node.id, (current) => ({
                ...current,
                metadata: {
                    ...current.metadata,
                    content: contentValue,
                    dataSource: {
                        ...(current.metadata?.dataSource || { format }),
                        ...patch,
                    },
                },
            }));
            return;
        }
        updateMetadata?.(node.id, {
            content: contentValue,
            dataSource: mergedConfig,
        });
    };

    const handleFileImport = async (event: ChangeEvent<HTMLInputElement>) => {
        const file = event.currentTarget.files?.[0];
        event.currentTarget.value = "";
        if (!file) return;
        const text = await file.text();
        const detectedFormat = detectDataSourceFormatFromFilename(file.name, text);
        patchDataSource({ format: detectedFormat, rawText: text, fileName: file.name }, text);
    };

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
            <input ref={fileInputRef} type="file" accept=".json,.jsonl,.csv,.tsv,.txt,.md,.markdown" className="hidden" onChange={handleFileImport} />

            {/* 顶栏：格式切换 + 导入文件 + 视图切换 */}
            <div className="flex flex-wrap items-center justify-between gap-1.5 pb-2" style={{ borderBottom: `1px solid ${theme.node.edge}` }}>
                <div className="flex items-center gap-1" role="tablist" aria-label="数据源格式">
                    <Database className="mr-0.5 size-3.5 shrink-0" style={{ color: theme.accent.primary }} />
                    {FORMAT_OPTIONS.map((item) => {
                        const active = format === item;
                        return (
                            <button
                                key={item}
                                type="button"
                                aria-pressed={active}
                                className="rounded-[var(--r-sm)] px-1.5 py-0.5 text-[var(--fs-tiny)] font-medium transition-colors"
                                style={{
                                    background: active ? theme.accent.primary : theme.node.panel,
                                    color: active ? theme.accent.onPrimary : theme.node.muted,
                                }}
                                onClick={() => patchDataSource({ format: item })}
                            >
                                {DATA_SOURCE_FORMAT_LABELS[item]}
                            </button>
                        );
                    })}
                </div>

                <div className="flex items-center gap-1">
                    <button
                        type="button"
                        className="inline-flex items-center gap-1 rounded-[var(--r-sm)] border px-1.5 py-0.5 text-[var(--fs-tiny)] font-medium transition-opacity hover:opacity-90"
                        style={{ background: theme.node.panel, borderColor: theme.node.edge, color: theme.node.text }}
                        onClick={() => fileInputRef.current?.click()}
                        title="导入本地 JSON / CSV / TXT / Markdown 文件"
                    >
                        <FileUp className="size-3" />
                        导入
                    </button>
                    <button
                        type="button"
                        aria-pressed={viewMode === "preview"}
                        className="inline-flex items-center gap-1 rounded-[var(--r-sm)] border px-1.5 py-0.5 text-[var(--fs-tiny)] font-medium transition-colors"
                        style={{
                            background: viewMode === "preview" ? `${theme.accent.primary}22` : theme.node.panel,
                            borderColor: viewMode === "preview" ? theme.accent.primary : theme.node.edge,
                            color: viewMode === "preview" ? theme.accent.primary : theme.node.muted,
                        }}
                        onClick={() => setViewMode((current) => (current === "edit" ? "preview" : "edit"))}
                    >
                        {viewMode === "edit" ? <TableProperties className="size-3" /> : <Code2 className="size-3" />}
                        {viewMode === "edit" ? `预览 (${result.outputItems.length})` : "源码"}
                    </button>
                </div>
            </div>

            {/* 错误提示 */}
            {result.error ? (
                <div className="mt-2 flex items-center gap-1.5 rounded-[var(--r-sm)] px-2.5 py-1.5 text-[var(--fs-tiny)]" style={{ background: "color-mix(in srgb, var(--status-error, #ef4444) 14%, transparent)", color: "var(--status-error, #ef4444)" }}>
                    <AlertCircle className="size-3.5 shrink-0" />
                    <span className="truncate">{result.error}</span>
                </div>
            ) : null}

            {/* 主体区：源码编辑 或 结构化预览 */}
            <div className="mt-2 min-h-0 flex-1 overflow-hidden rounded-[var(--r-md)] border" style={{ background: theme.node.panel, borderColor: theme.node.edge }}>
                {viewMode === "edit" ? (
                    <textarea
                        aria-label="数据源内容"
                        className="thin-scrollbar h-full w-full resize-none border-none bg-transparent p-2.5 font-mono text-xs leading-5 outline-none select-text"
                        style={{ color: theme.node.text }}
                        placeholder={FORMAT_PLACEHOLDERS[format]}
                        value={rawText}
                        onChange={(event) => patchDataSource({ rawText: event.target.value }, event.target.value)}
                    />
                ) : (
                    <div className="thin-scrollbar flex h-full w-full flex-col gap-1.5 overflow-y-auto p-2">
                        {result.outputItems.length ? (
                            result.outputItems.slice(0, 50).map((item, idx) => (
                                <div key={idx} className="flex items-start gap-2 rounded-[var(--r-sm)] px-2 py-1.5 font-mono text-[var(--fs-tiny)]" style={{ background: theme.node.fill, border: `1px solid ${theme.node.edge}` }}>
                                    <span className="shrink-0 font-semibold opacity-50">#{idx + 1}</span>
                                    <span className="min-w-0 flex-1 break-all select-text">{formatPreviewItem(item)}</span>
                                </div>
                            ))
                        ) : (
                            <div className="flex h-full items-center justify-center text-center text-[var(--fs-label)]" style={{ color: theme.node.muted }}>
                                暂无解析出的条目
                            </div>
                        )}
                    </div>
                )}
            </div>

            {/* 底栏：条数统计与字段摘要 */}
            <div className="mt-2 flex items-center justify-between gap-2 text-[var(--fs-tiny)]" style={{ color: theme.node.muted }}>
                <div className="flex min-w-0 items-center gap-1.5">
                    <span className="shrink-0 rounded-[var(--r-sm)] px-1.5 py-0.5 font-semibold tabular-nums" style={{ background: `${theme.accent.primary}1f`, color: theme.accent.primary }}>
                        {result.outputItems.length} 条记录
                    </span>
                    {result.detectedKeys.length ? (
                        <span className="truncate" title={`字段: ${result.detectedKeys.join(", ")}`}>
                            字段: {result.detectedKeys.slice(0, 5).join(", ")}
                        </span>
                    ) : null}
                </div>
                {fileName ? (
                    <span className="max-w-[120px] truncate opacity-75" title={fileName}>
                        {fileName}
                    </span>
                ) : null}
            </div>
        </div>
    );
}
