# Proposal

## Why

当前画布已具备 `data-source` 与 `logic` 低代码数据流建模能力，但在 `compileCloudAgentTools` 中，情报会话（`intel`）与画布会话（`canvas`）的工具表处于隔离状态：画布 Agent 无法在画布会话内直接查询工作区情报库或调用已授权的 MCP 工具来动态填充数据源节点，同时后端能力注册表尚未注册前端已有的 `skill`（技能）节点，导致 Agent 只能在自身上下文中读取技能，而无法在画布上摆放显式的 `skill` 节点参与连线建模。打通这两步后，画布 Pi Agent 即可在单一画布会话内完成「搜集情报/调用 MCP + 结合知识库/挂载 Skill 节点 + 数据集筛选/交并差/If-Else 分支建模」的完整闭环。

## What Changes

- **画布会话内打通只读情报查询与 MCP 工具调用**：
  - 在画布运行准入（`CreateCloudAgentRun`）与 `flowMCPToolsFor` 中支持 `SubjectCanvas` 作用域解析（兼容带 `flowConversationId` 的镜像画布会话与普通画布会话，按用户个人工作区与角色权限快照已授权的 `mcp_*` 工具及只读情报工具）。
  - 在画布工具表 `compileCanvasAgentTools` 中按会话快照挂载只读情报检索工具（`intel_search_documents`、`intel_read_document`、`intel_list_sources`）及非只读模式下的已批准 MCP 工具（`req.FlowMCPTools`）。
  - 修复 `executeIntelToolIn` 与 `advanceCloudAgentIntelTool` 在画布会话中的分派与会话作用域解析：当画布会话调用 `intel_*` 或 `mcp_*` 工具时，使用 `flowRunChain` 执行器并在无显式 `flowConversationId` 时自动绑定画布对应的目录会话作用域，同时本地/MCP 工具不强依赖远程 `flowEngine` 实例。
- **后端注册 `skill` 节点能力并接入前端低代码数据流**：
  - 在 `backend/internal/canvas/capability/builtin.go` 的 `BuiltinRegistry()` 中注册 `skill` 节点类型（`Type: "skill"`，支持 `content`、`skillId`、`skillName`、`skillCategory`、`skillOutputMode` 等可编辑补丁字段，支持接收上游文本/数据源/逻辑节点输入并连接到下游文本/分镜/图片/视频/音频节点）。
  - 完善 `backend/internal/app/cloud_agent_tools.go` 中 `canvas_apply_ops` 的 `fromHandleId` 与 `toHandleId` 参数 Schema 说明，明确列出逻辑节点的 `true` / `false` 与集合节点的 `input_a` / `input_b` 端口。
  - 在前端 `web/src/lib/canvas/dataflow-engine.ts` 中支持 `CanvasNodeType.Skill` 节点参与数据流求值（将上游流入的数据集与技能模板结合并透传给下游节点）。

## Capabilities

### New Capabilities

### Modified Capabilities
- `canvas-lowcode-dataflow`: 扩展画布低代码数据流与 Agent 建模能力，新增画布会话内只读情报检索与 MCP 工具调用支持，以及画布 `skill` 技能节点的 Agent 创建/更新/连线与数据流求值能力。

## Impact

- **Backend**:
  - `backend/internal/canvas/capability/builtin.go`（新增 `skill` 节点能力描述与 PatchFields）
  - `backend/internal/app/cloud_agent.go`、`backend/internal/app/cloud_agent_flow.go`、`backend/internal/app/cloud_agent_tools.go`、`backend/internal/app/cloud_agent_runtime_tools.go`（画布会话挂载只读情报与 MCP 工具、作用域解析与执行链分派、端口 Schema 描述补齐）
  - `backend/internal/app/*_test.go`、`backend/internal/canvas/capability/*_test.go`（补齐单元测试与回归测试）
- **Frontend**:
  - `web/src/lib/canvas/dataflow-engine.ts`、`web/test/canvas-dataflow.test.ts`（`skill` 节点参与数据流透传与模板拼装）
