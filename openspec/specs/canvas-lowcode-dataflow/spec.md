# canvas-lowcode-dataflow Specification

## Purpose
TBD - created by archiving change canvas-lowcode-dataflow-and-logic-gates. Update Purpose after archive.

## Requirements

### Requirement: 画布低代码数据源节点与多格式结构化解析
画布 SHALL 提供 `data-source` 节点类型，支持解析 JSON、CSV、Markdown、TXT 及结构化情报（`intel`）数据，向相连的下游逻辑门或生成节点输出结构化记录集合与摘要文本。

#### Scenario: 解析多种格式的数据源节点
- **WHEN** 用户或云端 Agent 在画布中创建 `data-source` 节点并输入 JSON、CSV、Markdown、TXT 或情报结构化内容
- **THEN** 数据流求值器解析出标准化的记录数组与字段列表，并在节点面板实时展示条目计数与字段预览。

### Requirement: 画布逻辑门节点、双端口分流与下游生成门控
画布 SHALL 提供 `logic` 节点类型，支持双输入端口（`input_a`、`input_b`）集合运算（`intersect`、`union`、`difference`）、`if_else` 双输出端口（`true`、`false`）条件路由、嵌套字段提取（`extract_field`）以及环路检测；当条件分支未命中或门控关闭时，SHALL 阻止对应下游生成节点触发生成任务。

#### Scenario: 条件分流与环路安全求值
- **WHEN** `logic` 节点配置为 `if_else` 并连接至下游不同端口，或画布连线存在有向环路
- **THEN** 求值器将满足与不满足条件的记录分别路由至 `true` 和 `false` 端口並门控未激活分支，遇到环路时安全终止递归而不发生死循环。

### Requirement: 画布会话内直接调用只读情报检索与已授权 MCP 工具
画布 Agent 运行时 SHALL 支持在画布会话（`subject.kind = "canvas"`）中按用户所属工作区权限暴露并执行只读情报检索工具（`intel_search_documents`、`intel_read_document`、`intel_list_sources`）及工作区已授权的远程 MCP 工具（`mcp_*`，仅在非 `read_only` 模式下），使 Agent 能够在画布内实时检索情报或调用外部 MCP 数据并写入画布 `data-source` 节点。

#### Scenario: 画布会话调用只读情报检索与 MCP 工具
- **WHEN** 已登录用户在画布会话中启动云端 Agent 运行，且其所属工作区拥有情报读取权限或已批准的 MCP 连接
- **THEN** Agent 工具表包含只读情报检索工具与已批准的 `mcp_*` 工具（只读模式除外），且工具调用通过租户 RLS 与能力快照校验后返回结果，不会暴露情报台独占的写配置/投送工具（如 `intel_apply_monitor`、`intel_handoff_to_canvas`）。

### Requirement: 画布技能节点（Skill Node）注册、Agent 建模与数据流求值
系统 SHALL 在画布能力注册表中提供 `skill` 节点类型，允许云端 Agent 通过 `canvas_apply_ops` 创建、更新和连线 `skill` 节点（支持配置技能标识、名称、类别、输出模式与模板正文）；当前端数据流求值器遇到连接在数据源/逻辑节点与下游节点之间的 `skill` 节点时，SHALL 将上游结构化数据集与技能模板组合并透传至下游节点。

#### Scenario: Agent 在画布中创建并连线技能节点参与数据流
- **WHEN** 云端 Agent 使用 `canvas_apply_ops` 新增或更新 `nodeType: "skill"` 节点，并将上游 `data-source` 或 `logic` 节点连接到该 `skill` 节点再连接至下游生成节点
- **THEN** 服务端校验通过并持久化 `skill` 节点及连线，前端数据流求值器输出融合技能模板与上游有效记录的数据包供下游生成使用。
