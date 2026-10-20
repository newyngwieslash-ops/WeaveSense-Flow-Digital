import {
    CanvasNodeType,
    type CanvasConnection,
    type CanvasDataSourceConfig,
    type CanvasDataSourceFormat,
    type CanvasLogicComparator,
    type CanvasLogicConfig,
    type CanvasLogicOperator,
    type CanvasNodeData,
} from "@/types/canvas";

export type CanvasDataItem = Record<string, unknown> | string | number | boolean;

export type DataflowNodeResult = {
    nodeId: string;
    format?: CanvasDataSourceFormat;
    operator?: CanvasLogicOperator;
    inputA: CanvasDataItem[];
    inputB: CanvasDataItem[];
    outputItems: CanvasDataItem[];
    trueItems: CanvasDataItem[];
    falseItems: CanvasDataItem[];
    detectedKeys: string[];
    error?: string;
    cycleDetected?: boolean;
};

export type CanvasDataflowEvaluation = {
    nodes: Map<string, DataflowNodeResult>;
    getEdgeItems: (
        fromNodeId: string,
        fromHandleId?: string,
    ) => {
        items: CanvasDataItem[];
        active: boolean;
        isConditionalBranch: boolean;
        branchLabel?: string;
    };
    getEdgeText: (fromNodeId: string, fromHandleId?: string) => string;
};

export const LOGIC_PORT_TOP_RATIO = 0.38;
export const LOGIC_PORT_BOTTOM_RATIO = 0.72;

export const DATA_SOURCE_FORMAT_LABELS: Record<CanvasDataSourceFormat, string> = {
    json: "JSON",
    csv: "CSV",
    txt: "TXT",
    markdown: "Markdown",
    intel: "情报集",
};

export const LOGIC_OPERATOR_LABELS: Record<CanvasLogicOperator, string> = {
    if_else: "If / Else 分支",
    intersect: "交集 (A ∩ B)",
    union: "并集 (A ∪ B)",
    difference: "差集 (A − B)",
    filter: "条件过滤",
    json_extract: "字段提取",
};

export const LOGIC_COMPARATOR_LABELS: Record<CanvasLogicComparator, string> = {
    contains: "包含",
    not_contains: "不包含",
    equals: "等于 (=)",
    not_equals: "不等于 (≠)",
    gt: "大于 (>)",
    lt: "小于 (<)",
    not_empty: "非空",
    is_empty: "为空",
    regex: "正则匹配",
};

export function defaultDataSourceConfig(): CanvasDataSourceConfig {
    return {
        format: "json",
        rawText: '[\n  { "id": "1", "title": "示例条目 A", "score": 90 },\n  { "id": "2", "title": "示例条目 B", "score": 65 }\n]',
    };
}

export function defaultLogicConfig(): CanvasLogicConfig {
    return {
        operator: "if_else",
        fieldPath: "score",
        comparator: "gt",
        compareValue: "80",
        matchKey: "id",
    };
}

export function readNodeLogicConfig(node: CanvasNodeData): CanvasLogicConfig {
    const raw = node.metadata?.logic;
    return {
        operator: raw?.operator || "if_else",
        fieldPath: raw?.fieldPath ?? "",
        comparator: raw?.comparator || "contains",
        compareValue: raw?.compareValue ?? "",
        matchKey: raw?.matchKey ?? "",
    };
}

export function isLogicDualInput(node: CanvasNodeData): boolean {
    if (node.type !== CanvasNodeType.Logic) return false;
    const op = readNodeLogicConfig(node).operator;
    return op === "intersect" || op === "union" || op === "difference";
}

export function isLogicDualOutput(node: CanvasNodeData): boolean {
    if (node.type !== CanvasNodeType.Logic) return false;
    return readNodeLogicConfig(node).operator === "if_else";
}

export function logicNodeHandleY(node: CanvasNodeData, handleId?: string): number | undefined {
    if (node.type !== CanvasNodeType.Logic) return undefined;
    if (handleId === "input_a" || handleId === "true") {
        return node.position.y + node.height * LOGIC_PORT_TOP_RATIO;
    }
    if (handleId === "input_b" || handleId === "false") {
        return node.position.y + node.height * LOGIC_PORT_BOTTOM_RATIO;
    }
    return undefined;
}

export function logicNodeHandleAtY(node: CanvasNodeData, side: "source" | "target", worldY: number, hitRadius = 48): string | undefined {
    if (node.type !== CanvasNodeType.Logic) return undefined;
    const isDual = side === "target" ? isLogicDualInput(node) : isLogicDualOutput(node);
    if (!isDual) return undefined;
    const topY = node.position.y + node.height * LOGIC_PORT_TOP_RATIO;
    const bottomY = node.position.y + node.height * LOGIC_PORT_BOTTOM_RATIO;
    const distTop = Math.abs(worldY - topY);
    const distBottom = Math.abs(worldY - bottomY);
    if (Math.min(distTop, distBottom) > hitRadius) return undefined;
    if (side === "target") {
        return distTop <= distBottom ? "input_a" : "input_b";
    }
    return distTop <= distBottom ? "true" : "false";
}

export function detectDataSourceFormatFromFilename(fileName: string, content = ""): CanvasDataSourceFormat {
    const lower = fileName.trim().toLowerCase();
    if (lower.endsWith(".json") || lower.endsWith(".jsonl")) return "json";
    if (lower.endsWith(".csv") || lower.endsWith(".tsv")) return "csv";
    if (lower.endsWith(".md") || lower.endsWith(".markdown")) return "markdown";
    if (lower.endsWith(".txt")) return "txt";
    const trimmed = content.trim();
    if ((trimmed.startsWith("[") && trimmed.endsWith("]")) || (trimmed.startsWith("{") && trimmed.endsWith("}"))) {
        return "json";
    }
    return "txt";
}

function normalizeScalarValue(raw: string): string | number | boolean {
    const trimmed = raw.trim();
    if (trimmed === "true") return true;
    if (trimmed === "false") return false;
    if (trimmed !== "" && /^-?\d+(?:\.\d+)?$/.test(trimmed)) {
        const num = Number(trimmed);
        if (Number.isFinite(num)) return num;
    }
    return trimmed;
}

function splitCsvLine(line: string): string[] {
    const cells: string[] = [];
    let current = "";
    let inQuotes = false;
    for (let i = 0; i < line.length; i += 1) {
        const ch = line[i];
        if (ch === '"') {
            if (inQuotes && line[i + 1] === '"') {
                current += '"';
                i += 1;
            } else {
                inQuotes = !inQuotes;
            }
            continue;
        }
        if (ch === "," && !inQuotes) {
            cells.push(current.trim());
            current = "";
            continue;
        }
        current += ch;
    }
    cells.push(current.trim());
    return cells;
}

export function parseCsvItems(text: string): CanvasDataItem[] {
    const lines = text
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter(Boolean);
    if (!lines.length) return [];
    if (lines.length === 1) {
        return splitCsvLine(lines[0]).filter(Boolean);
    }
    const headers = splitCsvLine(lines[0]).map((h, idx) => h || `col_${idx + 1}`);
    return lines.slice(1).map((line) => {
        const cells = splitCsvLine(line);
        const row: Record<string, unknown> = {};
        headers.forEach((header, idx) => {
            row[header] = normalizeScalarValue(cells[idx] ?? "");
        });
        return row;
    });
}

export function parseMarkdownItems(text: string): CanvasDataItem[] {
    const trimmed = text.trim();
    if (!trimmed) return [];
    const lines = trimmed.split(/\r?\n/);
    const bulletItems = lines
        .map((line) => {
            const match = /^(?:[-*+]|\d+\.)\s+(.+)$/.exec(line.trim());
            return match ? match[1].trim() : null;
        })
        .filter((item): item is string => Boolean(item));
    if (bulletItems.length >= 2) {
        return bulletItems.map((item, idx) => ({ id: String(idx + 1), text: item, title: item }));
    }

    const sections: CanvasDataItem[] = [];
    let currentTitle = "";
    let currentLines: string[] = [];
    const flushSection = () => {
        const body = currentLines.join("\n").trim();
        if (currentTitle || body) {
            sections.push({
                id: String(sections.length + 1),
                title: currentTitle || body.slice(0, 40),
                content: body || currentTitle,
            });
        }
    };
    for (const line of lines) {
        const headingMatch = /^#{1,4}\s+(.+)$/.exec(line.trim());
        if (headingMatch) {
            flushSection();
            currentTitle = headingMatch[1].trim();
            currentLines = [];
        } else {
            currentLines.push(line);
        }
    }
    flushSection();
    if (sections.length > 1 || currentTitle) return sections;

    return trimmed
        .split(/\r?\n\s*\r?\n/)
        .map((block) => block.trim())
        .filter(Boolean);
}

export function parseJsonItems(text: string): { items: CanvasDataItem[]; error?: string } {
    const trimmed = text.trim();
    if (!trimmed) return { items: [] };
    try {
        const parsed: unknown = JSON.parse(trimmed);
        if (Array.isArray(parsed)) {
            return { items: parsed.map(toCanvasDataItem) };
        }
        if (parsed && typeof parsed === "object") {
            const record = parsed as Record<string, unknown>;
            for (const key of ["items", "rows", "data", "records", "list"]) {
                if (Array.isArray(record[key])) {
                    return { items: (record[key] as unknown[]).map(toCanvasDataItem) };
                }
            }
            return { items: [record] };
        }
        return { items: [toCanvasDataItem(parsed)] };
    } catch (err) {
        return {
            items: [],
            error: err instanceof Error ? `JSON 解析失败：${err.message}` : "JSON 格式不合法",
        };
    }
}

function toCanvasDataItem(value: unknown): CanvasDataItem {
    if (value === null || value === undefined) return "";
    if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") return value;
    if (Array.isArray(value)) return { items: value, text: JSON.stringify(value) };
    return value as Record<string, unknown>;
}

export function parseDataSourceNode(node: CanvasNodeData): { items: CanvasDataItem[]; format: CanvasDataSourceFormat; error?: string } {
    const ds = node.metadata?.dataSource;
    const raw = (ds?.rawText ?? node.metadata?.content ?? node.metadata?.prompt ?? "").trim();
    const format: CanvasDataSourceFormat = ds?.format || detectDataSourceFormatFromFilename(ds?.fileName || "", raw);
    if (!raw) return { items: [], format };

    if (format === "json" || format === "intel") {
        const res = parseJsonItems(raw);
        if (!res.error || format === "json") {
            return { items: res.items, format, error: res.error };
        }
        // 情报文本若不是 JSON，按段落/行拆分为结构化情报条目
        const lines = raw
            .split(/\r?\n/)
            .map((line) => line.trim())
            .filter(Boolean);
        return {
            items: lines.map((line, index) => ({ id: String(index + 1), title: line, text: line })),
            format,
        };
    }
    if (format === "csv") {
        return { items: parseCsvItems(raw), format };
    }
    if (format === "markdown") {
        return { items: parseMarkdownItems(raw), format };
    }
    // txt：优先尝试 JSON 数组，否则按非空行拆分
    if ((raw.startsWith("[") && raw.endsWith("]")) || (raw.startsWith("{") && raw.endsWith("}"))) {
        const jsonAttempt = parseJsonItems(raw);
        if (!jsonAttempt.error && jsonAttempt.items.length > 0) {
            return { items: jsonAttempt.items, format };
        }
    }
    const lines = raw
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter(Boolean);
    return { items: lines, format };
}

export function formatSkillNodePrompt(node: CanvasNodeData): string {
    const skill = node.metadata?.skillSnapshot;
    if (skill) {
        return [
            `【技能：${skill.name}】`,
            skill.description ? `用途：${skill.description}` : "",
            `执行模板：\n${skill.template}`,
            skill.outputContract ? `输出约束：\n${skill.outputContract}` : "",
            "请严格执行该技能，只输出结果，不要输出解释性套话。",
        ]
            .filter(Boolean)
            .join("\n\n");
    }
    const name = (node.metadata?.skillName || "").trim();
    const description = (node.metadata?.skillDescription || "").trim();
    const prompts = (node.metadata?.skillPrompts || []).map((p) => String(p).trim()).filter(Boolean);
    const content = (node.metadata?.content || node.metadata?.prompt || "").trim();
    const body = content || prompts.join("\n");
    if (!name && !description) return body;
    return [
        `【技能：${name || node.title || "Skill 节点"}】`,
        description ? `用途：${description}` : "",
        body ? `执行模板：\n${body}` : "",
    ]
        .filter(Boolean)
        .join("\n\n");
}

export function extractNodeItems(node: CanvasNodeData): { items: CanvasDataItem[]; format?: CanvasDataSourceFormat; error?: string } {
    if (node.type === CanvasNodeType.DataSource) {
        return parseDataSourceNode(node);
    }
    if (node.type === CanvasNodeType.Skill) {
        const formatted = formatSkillNodePrompt(node);
        return { items: formatted ? [formatted] : [], format: "txt" };
    }
    if (node.type === CanvasNodeType.Script) {
        const rows = node.metadata?.storyboard?.rows || [];
        return {
            items: rows.map((row) => ({
                id: row.id,
                shotNumber: row.shotNumber,
                durationSeconds: row.durationSeconds,
                plotDescription: row.plotDescription,
                dialogue: row.dialogue,
                imageGenerationPrompt: row.imageGenerationPrompt,
                videoMotionPrompt: row.videoMotionPrompt,
            })),
        };
    }
    if (node.type === CanvasNodeType.BatchTable) {
        const rows = node.metadata?.batchTable?.rows || [];
        return {
            items: rows.map((row, idx) => ({
                id: row.id || String(idx + 1),
                prompt: row.prompt,
                enabled: row.enabled,
                ...(row.cells || {}),
            })),
        };
    }
    if (node.type === CanvasNodeType.Image || node.type === CanvasNodeType.Video || node.type === CanvasNodeType.Audio) {
        const url = node.metadata?.content || "";
        const storageKey = node.metadata?.storageKey || "";
        if (!url && !storageKey) return { items: [] };
        return {
            items: [
                {
                    id: node.id,
                    type: node.type,
                    title: node.title || node.id,
                    url,
                    storageKey,
                },
            ],
        };
    }
    if (node.type === CanvasNodeType.Markdown) {
        const raw = (node.metadata?.content || "").trim();
        return { items: parseMarkdownItems(raw), format: "markdown" };
    }
    const raw = (node.metadata?.content || node.metadata?.prompt || node.metadata?.skillSnapshot?.template || "").trim();
    if (!raw) return { items: [] };
    if ((raw.startsWith("[") && raw.endsWith("]")) || (raw.startsWith("{") && raw.endsWith("}"))) {
        const jsonRes = parseJsonItems(raw);
        if (!jsonRes.error && jsonRes.items.length > 0) {
            return { items: jsonRes.items, format: "json" };
        }
    }
    const lines = raw
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter(Boolean);
    return { items: lines.length > 1 ? lines : [raw], format: "txt" };
}

export function readDataItemField(item: CanvasDataItem, fieldPath?: string): unknown {
    const cleanPath = (fieldPath || "").trim();
    if (!cleanPath) {
        if (item && typeof item === "object") {
            const rec = item as Record<string, unknown>;
            for (const key of ["text", "content", "title", "name", "prompt", "value", "id"]) {
                if (rec[key] !== undefined && rec[key] !== null && String(rec[key]).trim() !== "") {
                    return rec[key];
                }
            }
            return JSON.stringify(item);
        }
        return item;
    }
    if (!item || typeof item !== "object") {
        return item;
    }
    const segments = cleanPath.split(".").map((s) => s.trim()).filter(Boolean);
    let cursor: unknown = item;
    for (const seg of segments) {
        if (cursor === null || cursor === undefined || typeof cursor !== "object") return undefined;
        cursor = (cursor as Record<string, unknown>)[seg];
    }
    return cursor;
}

function itemScalarString(value: unknown): string {
    if (value === null || value === undefined) return "";
    if (typeof value === "string") return value;
    if (typeof value === "number" || typeof value === "boolean") return String(value);
    return JSON.stringify(value);
}

export function itemIdentityKey(item: CanvasDataItem, matchKey?: string): string {
    const cleanKey = (matchKey || "").trim();
    if (cleanKey && item && typeof item === "object") {
        const extracted = readDataItemField(item, cleanKey);
        return itemScalarString(extracted).trim();
    }
    if (item && typeof item === "object") {
        const rec = item as Record<string, unknown>;
        const sortedEntries = Object.keys(rec)
            .sort()
            .map((k) => [k, rec[k]]);
        return JSON.stringify(sortedEntries);
    }
    return String(item).trim();
}

export function evaluateLogicCondition(
    item: CanvasDataItem,
    comparator: CanvasLogicComparator = "contains",
    fieldPath?: string,
    compareValue = "",
): { matched: boolean; error?: string } {
    const rawValue = readDataItemField(item, fieldPath);
    const actualStr = itemScalarString(rawValue).trim();
    const targetStr = compareValue.trim();

    if (comparator === "is_empty") {
        const empty =
            rawValue === null ||
            rawValue === undefined ||
            actualStr === "" ||
            (Array.isArray(rawValue) && rawValue.length === 0);
        return { matched: empty };
    }
    if (comparator === "not_empty") {
        const empty =
            rawValue === null ||
            rawValue === undefined ||
            actualStr === "" ||
            (Array.isArray(rawValue) && rawValue.length === 0);
        return { matched: !empty };
    }
    if (comparator === "contains") {
        if (Array.isArray(rawValue)) {
            return { matched: rawValue.some((entry) => itemScalarString(entry).toLowerCase().includes(targetStr.toLowerCase())) };
        }
        return { matched: actualStr.toLowerCase().includes(targetStr.toLowerCase()) };
    }
    if (comparator === "not_contains") {
        const res = evaluateLogicCondition(item, "contains", fieldPath, compareValue);
        return { matched: !res.matched };
    }
    if (comparator === "equals") {
        const actualNum = Number(actualStr);
        const targetNum = Number(targetStr);
        if (actualStr !== "" && targetStr !== "" && Number.isFinite(actualNum) && Number.isFinite(targetNum)) {
            return { matched: actualNum === targetNum };
        }
        return { matched: actualStr === targetStr };
    }
    if (comparator === "not_equals") {
        const res = evaluateLogicCondition(item, "equals", fieldPath, compareValue);
        return { matched: !res.matched };
    }
    if (comparator === "gt" || comparator === "lt") {
        const actualNum = Number(actualStr);
        const targetNum = Number(targetStr);
        if (Number.isFinite(actualNum) && Number.isFinite(targetNum) && actualStr !== "" && targetStr !== "") {
            return { matched: comparator === "gt" ? actualNum > targetNum : actualNum < targetNum };
        }
        return { matched: comparator === "gt" ? actualStr > targetStr : actualStr < targetStr };
    }
    if (comparator === "regex") {
        if (!targetStr) return { matched: Boolean(actualStr) };
        try {
            const re = new RegExp(targetStr, "i");
            return { matched: re.test(actualStr) };
        } catch (err) {
            return {
                matched: false,
                error: err instanceof Error ? `正则表达式无效：${err.message}` : "正则表达式无效",
            };
        }
    }
    return { matched: false };
}

export function evaluateSetOperation(
    operator: "intersect" | "union" | "difference",
    itemsA: CanvasDataItem[],
    itemsB: CanvasDataItem[],
    matchKey?: string,
): CanvasDataItem[] {
    const keysB = new Set(itemsB.map((item) => itemIdentityKey(item, matchKey)).filter(Boolean));
    const seen = new Set<string>();
    const result: CanvasDataItem[] = [];

    if (operator === "intersect") {
        for (const item of itemsA) {
            const key = itemIdentityKey(item, matchKey);
            if (!key || !keysB.has(key) || seen.has(key)) continue;
            seen.add(key);
            result.push(item);
        }
        return result;
    }

    if (operator === "difference") {
        for (const item of itemsA) {
            const key = itemIdentityKey(item, matchKey);
            if (!key || keysB.has(key) || seen.has(key)) continue;
            seen.add(key);
            result.push(item);
        }
        return result;
    }

    // union: A ∪ B
    for (const item of [...itemsA, ...itemsB]) {
        const key = itemIdentityKey(item, matchKey);
        if (!key || seen.has(key)) continue;
        seen.add(key);
        result.push(item);
    }
    return result;
}

export function collectDetectedKeys(items: CanvasDataItem[]): string[] {
    const keys = new Set<string>();
    for (const item of items.slice(0, 25)) {
        if (item && typeof item === "object" && !Array.isArray(item)) {
            for (const key of Object.keys(item)) {
                keys.add(key);
                if (keys.size >= 16) return [...keys];
            }
        }
    }
    return [...keys];
}

export function evaluateLogicNode(node: CanvasNodeData, inputA: CanvasDataItem[], inputB: CanvasDataItem[]): DataflowNodeResult {
    const config = readNodeLogicConfig(node);
    const { operator, fieldPath, comparator = "contains", compareValue = "", matchKey } = config;
    const allInputs = [...inputA, ...inputB];
    const detectedKeys = collectDetectedKeys(allInputs);

    if (operator === "intersect" || operator === "union" || operator === "difference") {
        const outputItems = evaluateSetOperation(operator, inputA, inputB, matchKey);
        return {
            nodeId: node.id,
            operator,
            inputA,
            inputB,
            outputItems,
            trueItems: outputItems,
            falseItems: [],
            detectedKeys,
        };
    }

    if (operator === "if_else") {
        const trueItems: CanvasDataItem[] = [];
        const falseItems: CanvasDataItem[] = [];
        let firstError: string | undefined;
        for (const item of allInputs) {
            const res = evaluateLogicCondition(item, comparator, fieldPath, compareValue);
            if (res.error && !firstError) firstError = res.error;
            if (res.matched) trueItems.push(item);
            else falseItems.push(item);
        }
        return {
            nodeId: node.id,
            operator,
            inputA: allInputs,
            inputB: [],
            outputItems: trueItems,
            trueItems,
            falseItems,
            detectedKeys,
            error: firstError,
        };
    }

    if (operator === "filter") {
        const outputItems: CanvasDataItem[] = [];
        let firstError: string | undefined;
        for (const item of allInputs) {
            const res = evaluateLogicCondition(item, comparator, fieldPath, compareValue);
            if (res.error && !firstError) firstError = res.error;
            if (res.matched) outputItems.push(item);
        }
        return {
            nodeId: node.id,
            operator,
            inputA: allInputs,
            inputB: [],
            outputItems,
            trueItems: outputItems,
            falseItems: [],
            detectedKeys,
            error: firstError,
        };
    }

    // json_extract
    const extracted: CanvasDataItem[] = [];
    for (const item of allInputs) {
        const val = readDataItemField(item, fieldPath);
        if (val === undefined || val === null || val === "") continue;
        if (Array.isArray(val)) {
            val.forEach((v) => {
                if (v !== undefined && v !== null && v !== "") extracted.push(toCanvasDataItem(v));
            });
        } else {
            extracted.push(toCanvasDataItem(val));
        }
    }
    return {
        nodeId: node.id,
        operator,
        inputA: allInputs,
        inputB: [],
        outputItems: extracted,
        trueItems: extracted,
        falseItems: [],
        detectedKeys,
    };
}

export function formatDataItemsForPrompt(items: CanvasDataItem[]): string {
    if (!items.length) return "";
    const allPrimitive = items.every((item) => typeof item !== "object" || item === null);
    if (allPrimitive) {
        return items.map((item) => String(item)).join("\n");
    }
    return JSON.stringify(items, null, 2);
}

export function evaluateCanvasDataflow(nodes: CanvasNodeData[], connections: CanvasConnection[]): CanvasDataflowEvaluation {
    const nodeById = new Map(nodes.map((node) => [node.id, node]));
    const incomingByTarget = new Map<string, CanvasConnection[]>();
    for (const conn of connections) {
        if (!nodeById.has(conn.fromNodeId) || !nodeById.has(conn.toNodeId)) continue;
        if (conn.relation === "batch-output" || conn.relation === "storyboard-output") continue;
        const list = incomingByTarget.get(conn.toNodeId) || [];
        list.push(conn);
        incomingByTarget.set(conn.toNodeId, list);
    }

    const results = new Map<string, DataflowNodeResult>();
    const visitState = new Map<string, "visiting" | "visited">();

    const readEdgeItemsInternal = (conn: CanvasConnection): CanvasDataItem[] => {
        const sourceResult = evaluateNode(conn.fromNodeId);
        const sourceNode = nodeById.get(conn.fromNodeId);
        if (sourceNode?.type === CanvasNodeType.Logic && isLogicDualOutput(sourceNode)) {
            if (conn.fromHandleId === "false") return sourceResult.falseItems;
            return sourceResult.trueItems;
        }
        return sourceResult.outputItems;
    };

    const evaluateNode = (nodeId: string): DataflowNodeResult => {
        const cached = results.get(nodeId);
        if (cached) return cached;

        const state = visitState.get(nodeId);
        if (state === "visiting") {
            const cycleResult: DataflowNodeResult = {
                nodeId,
                inputA: [],
                inputB: [],
                outputItems: [],
                trueItems: [],
                falseItems: [],
                detectedKeys: [],
                cycleDetected: true,
                error: "检测到循环连线，已中止求值",
            };
            results.set(nodeId, cycleResult);
            return cycleResult;
        }

        visitState.set(nodeId, "visiting");
        const node = nodeById.get(nodeId);
        if (!node) {
            const empty: DataflowNodeResult = {
                nodeId,
                inputA: [],
                inputB: [],
                outputItems: [],
                trueItems: [],
                falseItems: [],
                detectedKeys: [],
            };
            visitState.set(nodeId, "visited");
            results.set(nodeId, empty);
            return empty;
        }

        if (node.type === CanvasNodeType.Logic) {
            const incoming = incomingByTarget.get(nodeId) || [];
            const dualInput = isLogicDualInput(node);
            const inputA: CanvasDataItem[] = [];
            const inputB: CanvasDataItem[] = [];
            let cyclicUpstream = false;

            incoming.forEach((conn, index) => {
                const upstreamRes = evaluateNode(conn.fromNodeId);
                if (upstreamRes.cycleDetected) cyclicUpstream = true;
                const items = readEdgeItemsInternal(conn);
                if (!dualInput) {
                    inputA.push(...items);
                    return;
                }
                if (conn.toHandleId === "input_b") {
                    inputB.push(...items);
                } else if (conn.toHandleId === "input_a") {
                    inputA.push(...items);
                } else if (index === 0) {
                    inputA.push(...items);
                } else {
                    inputB.push(...items);
                }
            });

            if (cyclicUpstream || results.get(nodeId)?.cycleDetected) {
                const cycleRes: DataflowNodeResult = {
                    nodeId,
                    operator: readNodeLogicConfig(node).operator,
                    inputA,
                    inputB,
                    outputItems: [],
                    trueItems: [],
                    falseItems: [],
                    detectedKeys: collectDetectedKeys([...inputA, ...inputB]),
                    cycleDetected: true,
                    error: "检测到循环连线，已中止求值",
                };
                visitState.set(nodeId, "visited");
                results.set(nodeId, cycleRes);
                return cycleRes;
            }

            const evaluated = evaluateLogicNode(node, inputA, inputB);
            visitState.set(nodeId, "visited");
            results.set(nodeId, evaluated);
            return evaluated;
        }

        if (node.type === CanvasNodeType.Skill) {
            const incoming = incomingByTarget.get(nodeId) || [];
            if (incoming.length > 0) {
                const inputA: CanvasDataItem[] = [];
                let cyclicUpstream = false;
                let hasLogicOrSkillUpstream = false;
                let anyActiveUpstream = false;

                incoming.forEach((conn) => {
                    const upstreamRes = evaluateNode(conn.fromNodeId);
                    if (upstreamRes.cycleDetected) cyclicUpstream = true;
                    const sourceNode = nodeById.get(conn.fromNodeId);
                    if (sourceNode?.type === CanvasNodeType.Logic || sourceNode?.type === CanvasNodeType.Skill) {
                        hasLogicOrSkillUpstream = true;
                    }
                    const items = readEdgeItemsInternal(conn);
                    if (items.length > 0) anyActiveUpstream = true;
                    inputA.push(...items);
                });

                if (cyclicUpstream || results.get(nodeId)?.cycleDetected) {
                    const cycleRes: DataflowNodeResult = {
                        nodeId,
                        inputA,
                        inputB: [],
                        outputItems: [],
                        trueItems: [],
                        falseItems: [],
                        detectedKeys: collectDetectedKeys(inputA),
                        cycleDetected: true,
                        error: "检测到循环连线，已中止求值",
                    };
                    visitState.set(nodeId, "visited");
                    results.set(nodeId, cycleRes);
                    return cycleRes;
                }

                if (hasLogicOrSkillUpstream && !anyActiveUpstream) {
                    const emptyRes: DataflowNodeResult = {
                        nodeId,
                        inputA: [],
                        inputB: [],
                        outputItems: [],
                        trueItems: [],
                        falseItems: [],
                        detectedKeys: [],
                    };
                    visitState.set(nodeId, "visited");
                    results.set(nodeId, emptyRes);
                    return emptyRes;
                }

                const skillPrompt = formatSkillNodePrompt(node);
                const upstreamText = formatDataItemsForPrompt(inputA);
                const combined = [skillPrompt, upstreamText ? `【上游输入数据】\n${upstreamText}` : ""].filter(Boolean).join("\n\n");
                const outputItems: CanvasDataItem[] = combined ? [combined] : inputA;
                const skillRes: DataflowNodeResult = {
                    nodeId,
                    format: "txt",
                    inputA,
                    inputB: [],
                    outputItems,
                    trueItems: outputItems,
                    falseItems: [],
                    detectedKeys: collectDetectedKeys(inputA),
                };
                visitState.set(nodeId, "visited");
                results.set(nodeId, skillRes);
                return skillRes;
            }
        }

        const parsed = extractNodeItems(node);
        const nodeResult: DataflowNodeResult = {
            nodeId,
            format: parsed.format,
            inputA: [],
            inputB: [],
            outputItems: parsed.items,
            trueItems: parsed.items,
            falseItems: [],
            detectedKeys: collectDetectedKeys(parsed.items),
            error: parsed.error,
        };
        visitState.set(nodeId, "visited");
        results.set(nodeId, nodeResult);
        return nodeResult;
    };

    for (const node of nodes) {
        if (node.type === CanvasNodeType.DataSource || node.type === CanvasNodeType.Logic || node.type === CanvasNodeType.Skill) {
            evaluateNode(node.id);
        }
    }

    const getEdgeItems = (fromNodeId: string, fromHandleId?: string) => {
        const node = nodeById.get(fromNodeId);
        const res = evaluateNode(fromNodeId);
        if (node?.type === CanvasNodeType.Logic && isLogicDualOutput(node)) {
            const isFalse = fromHandleId === "false";
            const items = isFalse ? res.falseItems : res.trueItems;
            return {
                items,
                active: items.length > 0 && !res.cycleDetected,
                isConditionalBranch: true,
                branchLabel: isFalse ? "False" : "True",
            };
        }
        return {
            items: res.outputItems,
            active: res.outputItems.length > 0 && !res.cycleDetected,
            isConditionalBranch: false,
        };
    };

    const getEdgeText = (fromNodeId: string, fromHandleId?: string) => {
        const { items, active } = getEdgeItems(fromNodeId, fromHandleId);
        if (!active) return "";
        return formatDataItemsForPrompt(items);
    };

    return {
        nodes: results,
        getEdgeItems,
        getEdgeText,
    };
}

/**
 * 检查目标节点上游是否连接了未命中的逻辑分支或异常逻辑节点。
 * 若某个目标节点连接了至少一个逻辑节点，且所有连入的逻辑分支均为空（0 条命中）或报错，
 * 则拦截下游 AI 生成并给出明确原因。
 */
export function findBlockedLogicBranchInput(
    nodeId: string,
    nodes: CanvasNodeData[],
    connections: CanvasConnection[],
): { node: CanvasNodeData; reason: string } | null {
    const hasLogicOrDataSource = nodes.some((node) => node.type === CanvasNodeType.Logic || node.type === CanvasNodeType.DataSource);
    if (!hasLogicOrDataSource) return null;

    const nodeById = new Map(nodes.map((node) => [node.id, node]));
    const evaluation = evaluateCanvasDataflow(nodes, connections);

    // 覆盖“生成节点 -> 配置节点 <- 逻辑节点”、“生成节点 <- Skill 节点 <- 逻辑节点”以及直接连线“生成节点 <- 逻辑节点”等拓扑
    const targetIds = new Set<string>([nodeId]);
    for (const conn of connections) {
        if (conn.fromNodeId === nodeId && nodeById.get(conn.toNodeId)?.type === CanvasNodeType.Config) {
            targetIds.add(conn.toNodeId);
        }
    }
    let expanded = true;
    while (expanded) {
        expanded = false;
        for (const conn of connections) {
            if (targetIds.has(conn.toNodeId) && !targetIds.has(conn.fromNodeId) && nodeById.get(conn.fromNodeId)?.type === CanvasNodeType.Skill) {
                targetIds.add(conn.fromNodeId);
                expanded = true;
            }
        }
    }

    const logicEdges = connections.filter(
        (conn) => targetIds.has(conn.toNodeId) && nodeById.get(conn.fromNodeId)?.type === CanvasNodeType.Logic,
    );
    if (!logicEdges.length) return null;

    let anyActive = false;
    let firstBlocked: { node: CanvasNodeData; reason: string } | null = null;

    for (const edge of logicEdges) {
        const logicNode = nodeById.get(edge.fromNodeId)!;
        const result = evaluation.nodes.get(logicNode.id);
        if (result?.cycleDetected) {
            return { node: logicNode, reason: "存在循环连线" };
        }
        if (result?.error) {
            return { node: logicNode, reason: result.error };
        }
        const edgeStatus = evaluation.getEdgeItems(edge.fromNodeId, edge.fromHandleId);
        if (edgeStatus.active) {
            anyActive = true;
        } else if (!firstBlocked) {
            const reason = edgeStatus.isConditionalBranch
                ? `${edgeStatus.branchLabel || "True"} 分支当前未命中（0 条数据）`
                : "当前运算结果为空（0 条数据）";
            firstBlocked = { node: logicNode, reason };
        }
    }

    return anyActive ? null : firstBlocked;
}
