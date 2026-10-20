# Tasks: 情报与画布一键交接（Canvas Handoff）及情报面板体验增强

- [x] 1. 在 `backend/internal/flow/canvas_handoff.go` 与 `routes.go` 中实现 `POST /api/v1/intel/canvas-handoff` 端点及画布初始节点图生成逻辑，并编写 `canvas_handoff_test.go` 单元测试
- [x] 2. 在 `web/src/services/api/intelligence.ts` 与 `web/src/pages/intelligence/canvas-handoff-modal.tsx` 中实现情报转交画布 API 客户端、弹窗组件及草稿/Agent 预填构建器
- [x] 3. 在 `web/src/pages/intelligence/` 各面板（`index.tsx`、`reports-panel.tsx`、`topics-panel.tsx`、`views.tsx`、`sources-panel.tsx`、`workspace-admin-panel.tsx`、`empty-reason.ts`）中集成画布交接入口与空态分类诊断
- [x] 4. 编写并通过 `web/test/intelligence-panels.test.tsx`、`web/test/intelligence-gaps9.test.ts`、`web/test/intelligence-empty-reason.test.ts` 与 `go -C backend test ./internal/flow/...` 全部测试
