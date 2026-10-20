# Proposal: 画布低代码数据源节点与逻辑门节点（Data Source + Logic Gates）

## Why
在 AI 影视与短剧创作工作台中，当创作者从情报终端交接结构化数据或导入外部 JSON/CSV/Markdown 素材时，需要在画布内对多路输入进行结构化解析、双输入集合运算（交集、并集、差集）、`If/Else` 条件分流（True/False 双输出端口）、嵌套字段提取与下游生成任务门控。原有画布仅支持静态文本与媒体节点，缺少低代码数据流求值器与多端口几何连接能力。

## What Changes
- **新增画布低代码节点类型与 UI 组件（`web/src/components/canvas/nodes/data-source-node.tsx`、`logic-node.tsx`）**：
  - 新增 `CanvasNodeType.DataSource`（`data-source`）节点：支持 JSON、CSV、Markdown、TXT 与结构化情报（`intel`）格式解析与记录预览。
  - 新增 `CanvasNodeType.Logic`（`logic`）节点：支持集合交集（`intersect`）、并集（`union`）、差集（`difference`）、条件分支（`if_else`，提供 `true` / `false` 双输出端口）与字段提取（`extract_field`），并支持对未通过条件门控的下游生成节点进行阻断。
- **新增无环数据流求值器与多端口连线支持（`web/src/lib/canvas/dataflow-evaluator.ts` 及画布连接控制器）**：
  - 实现防环安全的 DAG 求值器 `evaluateCanvasDataflow` 与逻辑节点双输入（`input_a`、`input_b`）/双输出（`true`、`false`）端口几何计算。
  - 在 `canvas-connections.tsx`、`canvas-node.tsx`、`use-canvas-connection-controller.ts`、`canvas-node-generation.ts`、`project.tsx` 中集成多端口连线渲染、提示词变量注入与生成前门控检查。
- **后端云端 Agent 画布能力注册与自动布局（`backend/internal/canvas/capability/*`、`backend/internal/app/cloud_agent_tools_canvas.go`、`cloud_agent_layout.go`）**：
  - 在后端画布内置节点能力表、自动排版与云端 Agent 工具中同步支持 `data-source` 和 `logic` 节点创建与连接。
  - 更新 `docs/content/docs/progress/pending-test.mdx` 待测试清单。

## Impact
- Affected specs: `canvas-lowcode-dataflow`
- Affected code:
  - `backend/internal/app/cloud_agent_layout.go`
  - `backend/internal/app/cloud_agent_tools_canvas.go`
  - `backend/internal/canvas/capability/builtin.go`
  - `backend/internal/canvas/capability/descriptor.go`
  - `backend/internal/canvas/capability/registry_test.go`
  - `docs/content/docs/progress/pending-test.mdx`
  - `web/src/components/canvas/canvas-connections.tsx`
  - `web/src/components/canvas/canvas-node-content.tsx`
  - `web/src/components/canvas/canvas-node-generation.ts`
  - `web/src/components/canvas/canvas-node-graph-context.ts`
  - `web/src/components/canvas/canvas-node.tsx`
  - `web/src/components/canvas/canvas-workspace-overlays.tsx`
  - `web/src/components/canvas/nodes/data-source-node.tsx`
  - `web/src/components/canvas/nodes/logic-node.tsx`
  - `web/src/constant/canvas.ts`
  - `web/src/lib/canvas/canvas-project-domain.ts`
  - `web/src/lib/canvas/canvas-resource-references.ts`
  - `web/src/lib/canvas/dataflow-evaluator.ts`
  - `web/src/lib/canvas/node-registry/definitions/builtin-nodes.tsx`
  - `web/src/lib/canvas/tool-registry/definitions/add-node-menu-tools.tsx`
  - `web/src/pages/canvas/project.tsx`
  - `web/src/pages/canvas/use-canvas-connection-controller.ts`
  - `web/src/pages/canvas/use-canvas-node-operations.ts`
  - `web/src/types/canvas.ts`
  - `web/test/canvas-dataflow.test.ts`
