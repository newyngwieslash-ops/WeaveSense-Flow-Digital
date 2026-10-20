# Proposal: 情报与画布一键交接（Canvas Handoff）及情报面板体验增强

## Why
用户在 `/intelligence` 情报终端完成热点监测、文档入库或深度情报简报生成后，往往需要将结构化情报洞察（摘要、证据引用、风险提示、镜头建议等）直接转化为 AI 影视与短剧创作画布（Canvas）中的初始节点图或云端 Agent 预填指令。此前情报终端与画布项目相互独立，缺少一键创建画布项目并注入结构化剧本大纲/视觉风格锚点与数据流节点的能力；同时情报面板在空列表原因归因（PRD §12.2）、工作区管理面板与多标签页交互上也需要进一步收口并补齐组件级单测。

## What Changes
- **后端情报交接画布接口（`backend/internal/flow/canvas_handoff.go` & `routes.go`）**：
  - 新增 `POST /api/v1/intel/canvas-handoff`，支持从情报文档（`document`）、深度报告（`report`）或热点话题（`topic`）构建并持久化新的画布项目（或注入指定项目），自动生成剧本大纲文本节点、视觉风格锚点节点、情报数据源节点（`data-source`）与条件门节点（`logic`）以及节点连线。
  - 在 `zz_cost_basis.go` 与 `routes.go` 中完成路由挂载与权限校验。
- **前端情报一键送入画布弹窗与 Agent 预填（`web/src/pages/intelligence/canvas-handoff-modal.tsx`）**：
  - 新增 `CanvasHandoffModal`、草稿构建函数（`buildReportHandoffDraft` / `buildDocumentHandoffDraft` / `buildTopicHandoffDraft`）、工具返回 URL 提取器与画布页 Agent 预填辅助函数 `buildIntelHandoffAgentPrefill`。
- **前端情报面板增强与空态归因（`web/src/pages/intelligence/*.tsx`、`empty-reason.ts`）**：
  - 在 `empty-reason.ts` 中完善 PRD §12.2 空列表分类归因（`no_sources`、`source_paused`、`source_failed`、`zero_new`、`filtered_out`、`time_range_empty`）及中文标题/详情提示。
  - 在 `reports-panel.tsx`、`topics-panel.tsx`、`views.tsx`、`sources-panel.tsx`、`workspace-admin-panel.tsx`、`index.tsx` 中集成一键转交画布入口、空态分类呈现与角色/配额展示优化。
  - 新增并完善 `web/test/intelligence-panels.test.tsx`、`web/test/intelligence-gaps9.test.ts`、`web/test/intelligence-empty-reason.test.ts` 共 21 项单测。

## Impact
- Affected specs: `intelligence-canvas-handoff`
- Affected code:
  - `backend/internal/flow/canvas_handoff.go`
  - `backend/internal/flow/canvas_handoff_test.go`
  - `backend/internal/flow/routes.go`
  - `backend/internal/flow/zz_cost_basis.go`
  - `web/src/pages/intelligence/canvas-handoff-modal.tsx`
  - `web/src/pages/intelligence/empty-reason.ts`
  - `web/src/pages/intelligence/index.tsx`
  - `web/src/pages/intelligence/reports-panel.tsx`
  - `web/src/pages/intelligence/sources-panel.tsx`
  - `web/src/pages/intelligence/topics-panel.tsx`
  - `web/src/pages/intelligence/views.tsx`
  - `web/src/pages/intelligence/workspace-admin-panel.tsx`
  - `web/src/services/api/intelligence.ts`
  - `web/test/intelligence-empty-reason.test.ts`
  - `web/test/intelligence-gaps9.test.ts`
  - `web/test/intelligence-panels.test.tsx`
