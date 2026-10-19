# Proposal: 情报报告导出扩展支持 PDF 与 DOCX 格式（Task B1）

## Why

当前情报报告导出接口（`GET /api/intelligence/reports/:id/export` 与引擎侧 `GET /v1/reports/:id/export`）仅支持 `md`、`html`、`json` 三种纯文本格式（登记于 `docs/flow/known-limitations.md` §1 与 `prd-audit-9.md` §5.2 偏差项 D）。在实际情报投研与汇报场景中，用户需要将包含中文标题、正文、分级论断（`source_statement` / `cross_source_verified` / `model_inference`）、证据引用链（含文档版本、片段 SHA-256、URL、原文摘录）以及覆盖范围（`coverage`）的报告直接导出为标准可打印的 **PDF**（`.pdf`）或可编辑的 **Word 文档**（`.docx`）。

## What Changes

- **Loom Engine 报告渲染器扩展（`services/loom-engine/flow/gaps9.ts`）**：
  - 在 `renderReport(r, format)` 中新增 `pdf`（`application/pdf`）与 `docx`（`application/vnd.openxmlformats-officedocument.wordprocessingml.document`）分支；
  - 采用零外部重型二进制依赖的纯 Node 实现生成符合规范的二进制载荷：
    - **DOCX**：生成标准 OpenXML（OPC ZIP）结构（含 `[Content_Types].xml`、`_rels/.rels`、`word/document.xml`、`word/styles.xml`），原生支持 UTF-8 中文排版、标题层级、论断标签、引用列表与摘录段落；
    - **PDF**：生成标准 `%PDF-1.7` 文档（含内置 CIDFont `STSong-Light` / `UniGB-UTF16-H` CJK 双字节编码与 `Helvetica` ASCII 字体、分页布局、XMP/Info 元数据与交叉引用表），完整呈现中文标题、正文、论断与证据链；
  - 对二进制格式同时返回 `content_base64`（供平台网关安全解码透传）与可读 `content` 摘要/文本，非法格式统一报错 `format must be md, html, json, pdf or docx`（HTTP 400）。
- **Go 平台导出代理二进制透传（`backend/internal/flow/gaps9.go`）**：
  - 在 `GET /api/intelligence/reports/:id/export` 中优先检查引擎响应中的 `content_base64`；若存在则 Base64 解码后以原始二进制字节返回（带 `Content-Disposition: attachment` 与 `X-Content-Type-Options: nosniff`），同时完整保留 `md` / `html` / `json` 文本透传与跨租户 404（A02）语义。
- **前端情报报告面板与 API 类型同步（`web/src/services/api/intelligence.ts`、`web/src/pages/intelligence/reports-panel.tsx`）**：
  - 将 `exportReport` 的 `format` 联合类型扩展为 `"md" | "html" | "json" | "pdf" | "docx"`；
  - 在报告详情卡片右上角提供 `MD`、`HTML`、`JSON`、`PDF`、`DOCX` 五种导出按钮。
- **文档与测试同步**：
  - 补充 `services/loom-engine/flow/gaps9.test.ts`、`backend/internal/flow/gaps9_test.go`、`web/test/intelligence-gaps9.test.ts` 中对 `pdf` / `docx` 导出、中文内容、二进制头校验及跨租户 404 的测试；
  - 更新 `docs/flow/known-limitations.md` §1 状态说明。

## Capabilities

### New Capabilities

### Modified Capabilities
- `intelligence-workspace`: 扩展情报工作台报告导出能力，支持 `md`、`html`、`json`、`pdf`、`docx` 五种格式及跨租户作用域隔离。

## Impact

- 引擎：`services/loom-engine/flow/gaps9.ts`、`services/loom-engine/flow/reports.ts`、`services/loom-engine/flow/gaps9.test.ts`
- 平台后端：`backend/internal/flow/gaps9.go`、`backend/internal/flow/gaps9_test.go`
- 前端：`web/src/services/api/intelligence.ts`、`web/src/pages/intelligence/reports-panel.tsx`、`web/test/intelligence-gaps9.test.ts`
- 文档：`docs/flow/known-limitations.md`
