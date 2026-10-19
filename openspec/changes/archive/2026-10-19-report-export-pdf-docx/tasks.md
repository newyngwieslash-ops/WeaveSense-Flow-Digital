# Tasks

## 1. 引擎与平台侧 PDF / DOCX 报告导出实现

- [x] 1.1 在 `services/loom-engine/flow/gaps9.ts` 的 `renderReport`（及 `reports.ts` 注释）中新增 `pdf` 与 `docx` 格式生成逻辑（支持中文排版、论断与证据链、Base64 二进制传输），并在 `services/loom-engine/flow/gaps9.test.ts` 补充 PDF/DOCX 结构、中文标题与引用内容、非法格式 400 及跨租户 404 测试，运行 `node --experimental-strip-types --test services/loom-engine/flow/gaps9.test.ts` 验证通过
- [x] 1.2 在 `backend/internal/flow/gaps9.go` 的 `GET /reports/:id/export` 中支持 `content_base64` 二进制解码透传（保留 `content` 文本兼容与 `Content-Disposition` 响应头），在 `backend/internal/flow/gaps9_test.go` 补充 PDF/DOCX 二进制透传与跨租户 404 测试，运行 `cd backend && go test ./internal/flow -run TestGaps9RoutesForwardAndRender` 验证通过

## 2. 前端报告导出按钮与已知限制文档同步

- [x] 2.1 在 `web/src/services/api/intelligence.ts` 中将 `exportReport` 的 `format` 类型扩展为 `"md" | "html" | "json" | "pdf" | "docx"`（并导出 `ReportExportFormat`），在 `web/src/pages/intelligence/reports-panel.tsx` 补齐 `PDF` 与 `DOCX` 导出按钮，在 `web/test/intelligence-gaps9.test.ts` 补充导出格式契约断言，同步更新 `docs/flow/known-limitations.md` §1，运行 `cd web && bun test test/intelligence-gaps9.test.ts && bun run lint && bun run build` 验证通过
