# intelligence-workspace Delta Specification

## ADDED Requirements

### Requirement: 情报报告支持五种格式（MD / HTML / JSON / PDF / DOCX）导出与跨租户隔离
情报报告导出接口（`GET /api/intelligence/reports/:id/export?format=...` 与引擎侧 `GET /v1/reports/:id/export`）MUST 支持 `md`、`html`、`json`、`pdf`、`docx` 五种导出格式，并在前端报告详情面板提供对应导出入口；导出内容 MUST 包含报告标题、元数据、正文、论断列表及其引用证据链（文档修订版本、证据 ID、URL 与片段 SHA-256）；跨租户或跨工作区的报告 ID 请求 MUST 返回 HTTP 404（A02）。

#### Scenario: 导出包含中文标题与引用链的 PDF 与 DOCX 报告
- **WHEN** 已授权工作区成员对本工作区报告调用 `exportReport` 并指定 `format="pdf"` 或 `format="docx"`
- **THEN** 引擎与平台返回对应的二进制文件流（PDF 以 `%PDF-1.` 开头且 `Content-Type: application/pdf`，DOCX 为包含 `word/document.xml` 的合法 OpenXML ZIP 且 `Content-Type: application/vnd.openxmlformats-officedocument.wordprocessingml.document`），其中包含报告标题、论断与引用证据信息

#### Scenario: 非法导出格式与跨租户导出被拒绝
- **WHEN** 请求传入未受支持的 `format`（如 `exe`）或尝试导出其他租户/工作区的报告 ID
- **THEN** 非法格式返回 HTTP 400（`format must be md, html, json, pdf or docx`），跨租户/跨工作区报告 ID 返回 HTTP 404
