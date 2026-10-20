# Spec: intelligence-canvas-handoff

## ADDED Requirements

### Requirement: 情报一键转交画布项目与结构化节点生成
系统 SHALL 支持将情报工作台中的深度报告（`report`）、情报文档（`document`）或热点话题（`topic`）一键转交至创作画布，自动创建或更新画布项目并注入结构化剧本大纲节点、视觉锚点节点及可选的数据流节点，同时支持在画布侧预填云端 Agent 创作指令。

#### Scenario: 从情报报告或文档创建画布交接项目
- **WHEN** 已登录工作空间成员调用 `POST /api/v1/intel/canvas-handoff` 或在情报面板点击「送入画布创作」提交交接请求
- **THEN** 服务端创建对应的画布项目并写入结构化初始节点与连线，返回新建项目的 `project_id`、节点数量与画布跳转路由；前端支持提取交接上下文并预填画布 Agent 提示词。

### Requirement: 情报列表空状态六类原因归因与面板交互一致性
系统 SHALL 在情报总览与情报库为空时按 PRD §12.2 诊断展示具体空列表原因（无采集源、采集源已暂停、采集失败、筛选条件过严、时间范围无数据、本轮采集零新增），并提供准确的中文标题与操作引导。

#### Scenario: 多条件并存时的空列表原因优先级判定
- **WHEN** 情报文档列表为空且存在激活过滤条件、暂停源或采集失败源
- **THEN** `classifyEmptyDocumentsReason` 按确定性优先级返回对应原因枚举及中文说明文案，并在 `OverviewView` 与 `LibraryView` 中渲染。
