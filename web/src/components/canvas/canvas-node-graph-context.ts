import { createContext, useContext } from "react";

import { evaluateCanvasDataflow, type CanvasDataflowEvaluation, type DataflowNodeResult } from "@/lib/canvas/dataflow-evaluator";
import type { CanvasNodeData } from "@/types/canvas";

// 扩展节点（对比/图表/调色/数据源/逻辑算子等）要读自己的上游或 DAG 求值结果才能工作，但节点经 CanvasProjectWorldLayers
// 渲染、CanvasNodeContentProps 里只有 node 本身，没有 nodes/connections。
// 与 canvas-node-action-context 同一个理由：通过 Context 注入，避免改动 world-layers 的透传链。
// 无 Provider 时静默降级为单节点自求值或「没有上游」，节点自行显示空状态而不是崩。
export type CanvasNodeGraphContextValue = {
    getUpstreamNodes?: (nodeId: string) => CanvasNodeData[];
    dataflow?: CanvasDataflowEvaluation;
};

export const CanvasNodeGraphContext = createContext<CanvasNodeGraphContextValue>({});

/** 取该节点的直接上游素材节点；无 Provider 或无上游时返回空数组。 */
export function useUpstreamNodes(nodeId: string) {
    const { getUpstreamNodes } = useContext(CanvasNodeGraphContext);
    return getUpstreamNodes?.(nodeId) ?? [];
}

/** 取该节点在当前画布 DAG 中的实时求值结果；无 Provider 时按单节点计算。 */
export function useDataflowNodeResult(node: CanvasNodeData): DataflowNodeResult {
    const { dataflow } = useContext(CanvasNodeGraphContext);
    const fromGraph = dataflow?.nodes.get(node.id);
    if (fromGraph) return fromGraph;
    return evaluateCanvasDataflow([node], []).nodes.get(node.id)!;
}
