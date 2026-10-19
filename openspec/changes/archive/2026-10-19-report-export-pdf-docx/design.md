# Design: 情报报告导出扩展支持 PDF 与 DOCX 格式（Task B1）

## Context

`services/loom-engine/flow/gaps9.ts` 的 `renderReport` 目前仅实现 `md`、`html`、`json` 三种字符串导出，Go 平台 `backend/internal/flow/gaps9.go` 也仅按 UTF-8 字符串转发 `env.Data.Content`。为完成 `docs/flow/known-limitations.md` §1 的补强，需要同时在引擎渲染层、Go 平台代理层和前端报告面板打通 `pdf` 与 `docx` 二进制导出。

## Goals / Non-Goals

**Goals:**
- 在不引入外部重型守护进程（如 Chromium）或原生编译依赖的前提下，由 `renderReport` 直接产出标准合规、支持中英文混合排版的 `.pdf` 与 `.docx` 文件字节；
- Go 平台代理层支持 `content_base64` 无损解码并回传二进制响应流；
- 前端报告面板支持一键导出 `MD` / `HTML` / `JSON` / `PDF` / `DOCX`；
- 保持 A02 跨租户/跨工作区访问严格返回 404。

**Non-Goals:**
- 不改变报告生成、证据核验（`verifySnippet`）或宿主 Reviewer 的既有模型与数据表结构。

## Decisions

1. **纯 Node 标准 OpenXML (`.docx`) 生成器（`services/loom-engine/flow/gaps9.ts`）**
   - 使用 Node 内置 `node:zlib`（`deflateRawSync` + `crc32`）构造标准 ZIP 归档，包含 `[Content_Types].xml`、`_rels/.rels`、`word/_rels/document.xml.rels`、`word/styles.xml` 与 `word/document.xml`。
   - `word/document.xml` 采用 UTF-8 编码并配置中文字体回退（`Microsoft YaHei` / `SimSun`），依次渲染报告标题（Heading 1）、报告元数据（ID、类型、创建时间、技能版本）、正文段落、Claims 分级列表及每条证据引用的元信息与原文摘录（Quote）、以及 Coverage JSON 块。

2. **纯 Node 标准 `%PDF-1.7`（支持 CJK 中文与自动换行分页）生成器（`services/loom-engine/flow/gaps9.ts`）**
   - 构造包含 `/F1`（`Helvetica`，WinAnsiEncoding）与 `/F2`（Type0 复合字体 `STSong-Light`，`/Encoding /UniGB-UTF16-H`，DescendantFonts `Adobe-GB1`）的 PDF 文档结构。
   - 按行排版报告标题、元数据、正文、论断与引用链及覆盖范围，自动按字宽折行并在超出单页高度时切分多页；ASCII 文本与 UTF-16BE Hex 编码配合，同时在 PDF Info / XMP 流中写入完整 UTF-8 报告摘要以兼顾全文检索与阅读器渲染。

3. **Engine → Platform 二进制传输契约（`backend/internal/flow/gaps9.go`）**
   - `renderReport` 对 `pdf` 与 `docx` 返回 `{ filename, content_type, content_base64, content }`（其中 `content_base64` 为完整二进制 Base64 编码，`content` 为二进制 UTF-8/latin1 映射或可读文本供向后兼容断言）。
   - `backend/internal/flow/gaps9.go` 在 `/reports/:id/export` 路由中解析 `content_base64`：非空时优先使用 `base64.StdEncoding.DecodeString` 解码并写出原始字节，为空时沿用 `[]byte(env.Data.Content)`。
