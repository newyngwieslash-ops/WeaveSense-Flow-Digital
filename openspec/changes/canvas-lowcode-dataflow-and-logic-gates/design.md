# Design: 画布低代码数据源节点与逻辑门节点（Data Source + Logic Gates）

## Context
画布需要支持结构化数据流编排（Data Source → Logic Gate → 文本/图片/视频生成节点），同时兼容现有画布单端口连线协议与云端 Agent 工具调用。

## Technical Decisions
1. **纯函数无环数据流求值器（`web/src/lib/canvas/dataflow-evaluator.ts`）**：
   - 将数据源解析（JSON/CSV/Markdown/TXT/Intel）、集合运算（`intersect`/`union`/`difference`）、条件判断（`if_else`）和字段提取（`extract_field`）封装为无副作用的 `evaluateCanvasDataflow(nodes, connections)`，内置访问栈环路检测，碰到循环连线时安全中断并标记 `cycleDetected`，不阻塞画布渲染。
2. **向后兼容的多端口连线几何模型**：
   - 在 `CanvasConnection` 上扩展可选 `sourcePort`（`default` | `true` | `false`）与 `targetPort`（`default` | `input_a` | `input_b`），未指定时退化为原有单端口几何中心，确保历史画布存档零迁移无缝兼容。
3. **前后端节点能力对齐**：
   - 在 `backend/internal/canvas/capability/builtin.go` 与 `cloud_agent_tools_canvas.go` 中注册 `data-source` 和 `logic` 节点描述子，使云端 Agent 与情报交接（Canvas Handoff）可以直接编排数据流画布。
