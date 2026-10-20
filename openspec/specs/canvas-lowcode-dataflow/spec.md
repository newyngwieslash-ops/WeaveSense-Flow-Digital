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
