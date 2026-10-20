# Tasks

## 1. 画布会话挂载只读情报检索与 MCP 工具及执行链打通

- [x] 1.1 在 `backend/internal/app/cloud_agent.go` 与 `backend/internal/app/cloud_agent_flow.go` 中扩展 `flowMCPToolsFor` 与新增 `flowCanvasIntelToolsFor`（快照 `intel_search_documents`、`intel_read_document`、`intel_list_sources`），并在 `backend/internal/app/cloud_agent_tools.go` 的 `compileCanvasAgentTools` 中挂载只读情报工具与 `FlowMCPTools`，通过 `go test ./internal/app -run "TestCloudAgentToolSchemaStaysCompact|TestCloudAgentToolTableMatchesRuntimeDispatch"` 验证
- [x] 1.2 在 `backend/internal/app/cloud_agent_flow.go` 中修复画布会话执行 `intel_*` / `mcp_*` 工具的作用域解析（无显式 `FlowConversationID` 时按 `SubjectCanvas` 自动确保目录会话 `FlowAgentConversation`）、`executeIntelToolIn` 切至 `s.flowRunChain` 执行器、以及 `advanceCloudAgentIntelTool` 放行本地/MCP 工具，新增画布会话调用只读情报与 MCP 工具的集成单测并通过 `go test ./internal/app -run "TestCanvasAgentIntelAndMCP|TestCanvasConversationContinuedFromIntelligence"` 验证

## 2. 后端注册 `skill` 画布节点能力与前端数据流求值支持

- [x] 2.1 在 `backend/internal/canvas/capability/builtin.go` 与 `descriptor.go` 中注册 `skill` 节点能力（含 `skillId`、`skillName`、`skillDescription` 与 `content` 补丁字段），并在 `backend/internal/app/cloud_agent_tools.go` 完善 `fromHandleId` / `toHandleId` 端口描述，通过 `go test ./internal/canvas/capability/... ./internal/app -run "TestCloudAgentCanvas|TestCloudAgentToolSchemaStaysCompact"` 验证
- [x] 2.2 在前端 `web/src/lib/canvas/dataflow-evaluator.ts`、`canvas-resource-references.ts` 与 `canvas-node-generation.ts` 中支持 `CanvasNodeType.Skill` 节点参与低代码数据流求值（组合上游记录与技能模板向后透传并传递空分支门控），并在 `web/test/canvas-dataflow.test.ts` 补齐 `data-source -> logic -> skill -> downstream` 单测，通过 `cd web && bun test test/canvas-dataflow.test.ts` 与 `bun run build` 验证
