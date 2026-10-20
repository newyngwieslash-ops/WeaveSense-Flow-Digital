# Design

## Context

参见 `proposal.md`。目前后端在 `compileCloudAgentTools` 中按 `cloudAgentIntelSubject(req)` 将工具表一分为二：
1. 情报会话（`intelligence_workspace` / `topic`）走 `cloudAgentIntelTools`，具备 `intel_*` 工具与 `req.FlowMCPTools`（`mcp_*`）。
2. 画布会话（`canvas`）走 `compileCanvasAgentTools`，具备画布读写、分镜、预演、媒体生成与技能文件读取（`skill_search` / `skill_read_file`），但：
   - 未注入只读情报检索工具与 `req.FlowMCPTools`。
   - `executeIntelToolIn` 依赖 `req.FlowConversationID` 非空，且调用了 `s.agentRunChain(runID, req).Exec.Execute(...)`（在画布会话下会返回 `canvasRunChain` 而非 `flowRunChain`，导致无法执行 `intel_*` / `mcp_*` 工具）。
   - `advanceCloudAgentIntelTool` 强行检查 `s.flowEngine != nil`，导致仅配置 `s.flowMCP` 或调用本地 `intel_*` 工具时被拦截。
   - `backend/internal/canvas/capability/builtin.go` 未注册 `skill` 节点，Agent 无法在画布上摆放显式技能节点参与连线。

## Goals / Non-Goals

**Goals:**
- 让画布会话中的 Pi Agent 能够按需调用只读情报检索工具（`intel_search_documents`、`intel_read_document`、`intel_list_sources`）和工作区已授权的 `mcp_*` 工具，将检索结果直接用于构建或更新 `data-source` / `logic` 节点。
- 在不突破静态工具 Schema 体积预算（`TestCloudAgentToolSchemaStaysCompact`）且不破坏画布/情报工具边界守卫（`TestCloudAgentToolTableMatchesRuntimeDispatch`、`TestCanvasConversationContinuedFromIntelligenceUsesCanvasTools`）的前提下完成集成。
- 在后端能力注册表注册 `skill` 节点，并在前端 `evaluateCanvasDataflow` 中支持 `skill` 节点对上游数据集与技能模板的组合透传。

**Non-Goals:**
- 不向画布会话暴露情报台的后台写配置工具（如 `intel_apply_monitor`、`intel_apply_schedule`、`intel_collect_once`、`intel_handoff_to_canvas`）。
- 不新增外部 MCP 客户端协议实现（直接复用现有的 `flow.MCPGateway`）。

## Decisions

### 1. 画布会话按需挂载只读情报工具与 `FlowMCPTools`
- **决策**：
  - 在 `CloudAgentRequest` 中除原有的 `FlowMCPTools []map[string]any` 外，新增 `FlowIntelTools []map[string]any`（或在 `s.flowCanvasIntelToolsFor(userID, req)` 中快照只读情报工具 `intel_search_documents`、`intel_read_document`、`intel_list_sources`）。
  - 当 `CreateCloudAgentRun` 准入画布运行时：
    - 若 `s.flowMCP != nil` 且 `req.PermissionMode != "read_only"`，通过 `s.flowMCPToolsFor(userID, req)` 解析画布所属用户的个人工作区（或镜像会话工作区）权限，若具备 `intel.mcp.use` 则快照 `req.FlowMCPTools`。
    - 若 `s.flowEngine != nil`（或用户工作区具备情报读取权限且配置了引擎），快照只读情报检索工具 `intel_search_documents`、`intel_read_document`、`intel_list_sources` 到 `req.FlowIntelTools`。
  - 在 `compileCanvasAgentTools` 末尾追加 `req.FlowIntelTools` 以及（非 `read_only` 时）`req.FlowMCPTools`。
- **理由**：
  - 静态 Schema 预算测试（`TestCloudAgentToolSchemaStaysCompact`）使用纯值 `CloudAgentRequest`（无 `FlowIntelTools` / `FlowMCPTools`），不会因动态挂载工具而膨胀；
  - `TestCloudAgentToolTableMatchesRuntimeDispatch` 通过 AST 扫描 `add(...)` 字面量并过滤 `flow.IsTool(name)`，动态追加 `flow.Catalog` 子集与 `mcp_*` 完全符合其守卫契约；
  - `TestCanvasConversationContinuedFromIntelligenceUsesCanvasTools` 在未挂载 `flowEngine` 时不会暴露 `intel_*`，即使挂载也仅暴露只读检索工具，绝不暴露 `intel_apply_monitor` 等写工具。

### 2. 画布会话执行 `intel_*` / `mcp_*` 工具的会话作用域与执行链修复
- **决策**：
  - 在 `cloudAgentFlowConversationIn(db, userID, req)` 中，若 `req.FlowConversationID == ""` 且 `req.CanvasID != ""`，通过 `flow.ResolveSubject(db, userID, flow.AgentSubject{Kind: flow.SubjectCanvas, ID: req.CanvasID})` 解析用户个人工作区 `SubjectScope`，并按确定性 ID `flow.CanvasConversationIndexID(userID, req.CanvasID, "canvas-agent")` 获取或创建一条 `FlowAgentConversation`（`SubjectKind: flow.SubjectCanvas, SubjectID: req.CanvasID`），使 `flow_agent_tool_calls` 审计记录、幂等键与租户 RLS 完整闭合。
  - 在 `executeIntelToolIn` 中固定调用 `s.flowRunChain(runID, req).Exec.Execute(...)` 而非 `s.agentRunChain(runID, req).Exec.Execute(...)`，确保画布会话调用 `intel_*` / `mcp_*` 时进入 `IntelToolExecutor`。
  - 在 `advanceCloudAgentIntelTool` 中，当调用的是 `mcp_*` 工具（`flow.IsMCPToolName`）或具有 `local` 实现的工具时，检查 `s.flowMCP != nil`（对 MCP 工具），仅对需要远程引擎路由的 `intel_*` 工具检查 `s.flowEngine != nil`。

### 3. 后端注册 `skill` 节点能力与前端数据流求值联动
- **决策**：
  - 在 `backend/internal/canvas/capability/builtin.go` 注册 `Type: "skill"`（`Label: "技能"`，`InputKind: "text"`，`Connection: {CanSource: true, CanTarget: true, AcceptedInputKinds: ["text", "image", "video", "audio", "character"]}`，`CanUpdate: true`）。
  - `PatchFields` 开放 `content`（技能模板正文）、`skillId`（`metadata.skillId`）、`skillName`（`metadata.skillSnapshot.name`）、`skillCategory`（`metadata.skillSnapshot.category`）、`skillOutputMode`（`metadata.skillSnapshot.outputMode`）以及标题与坐标字段；`CreateMetadata` 同时初始化 `metadata.content` 与 `metadata.skillSnapshot.template`。
  - 在前端 `web/src/lib/canvas/dataflow-engine.ts` 中，当求值 `CanvasNodeType.Skill` 节点时，收集上游输入的数据条目与文本，并结合技能节点的 `skillSnapshot.template` / `content` 生成带技能指令包裹的输出条目与 `summaryText`，使 `data-source -> logic -> skill -> image/video/text` 形成完整数据流。

## Risks / Trade-offs

- **[Risk] 静态工具 Schema 体积接近 36,000 字节上限** → `skill` 节点的 `PatchFields` 复用已有的 `content`、`title`、`x`、`y`，仅新增轻量字段（如 `skillId`、`skillName`），并精简 `fromHandleId`/`toHandleId` 描述字数，确保 `TestCloudAgentToolSchemaStaysCompact` 保持在 36,000 字节预算内。
- **[Risk] 画布普通会话无 `FlowConversationID` 时调用 MCP/情报工具的外键与租户校验** → 通过 `flow.CanvasConversationIndexID(userID, req.CanvasID, "canvas-agent")` 在用户 RLS 事务内幂等确保 `FlowAgentConversation` 存在，保证 `FlowAgentToolCall` 审计与幂等重放完全一致。
