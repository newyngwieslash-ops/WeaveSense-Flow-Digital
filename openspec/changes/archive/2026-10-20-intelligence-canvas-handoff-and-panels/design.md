# Design: 情报与画布一键交接（Canvas Handoff）及情报面板体验增强

## Context
源铭振跃兼具「Flow 个人全网情报终端」与「AI 影视与短剧创作画布」两大核心工作区。为了打通「全网热点/情报深度简报 → 短剧剧本大纲/分镜资产生成」的创作闭环，需要提供确定性的后端画布交接接口与前端交互弹窗，同时补齐情报各子面板的空态原因诊断和组件级回归测试。

## Technical Decisions
1. **服务端直接生成初始画布节点图（`POST /api/v1/intel/canvas-handoff`）**：
   - 支持传入 `source_kind`（`report` / `document` / `topic`）、`source_id`、自定义标题、创作指令和是否启用低代码数据流门控。
   - 服务端从情报存储读取对应报告/文档摘要与关键论据，直接初始化包含 `text`（剧本/分镜大纲、视觉风格锚点）、`data-source`（结构化情报 JSON 源）与 `logic`（置信度/情绪门控）的画布节点与边，并返回跳转 URL `/canvas/:projectId?intel_handoff=1`。
2. **前端交接草稿构建与 URL 跳转协同（`canvas-handoff-modal.tsx`）**：
   - 将草稿拼装纯函数（`buildReportHandoffDraft`、`buildDocumentHandoffDraft`、`buildTopicHandoffDraft`、`buildIntelHandoffAgentPrefill`）与 `AppModal` UI 解耦导出，既支持用户在弹窗内编辑交接参数，也便于在 Bun 测试环境下进行纯函数与 SSR 渲染断言。
3. **PRD §12.2 空列表 6 类原因确定性优先级评估（`empty-reason.ts`）**：
   - 按 `filtered_out` → `no_sources` → `source_paused` → `source_failed` → `time_range_empty` → `zero_new` 顺序进行确定性归因，并在 `OverviewView` 与 `LibraryView` 中通过统一 `EmptyState` 呈现中文引导。
