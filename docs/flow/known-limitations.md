# 已知简化与本地后续开发说明

本文档记录当前 `main` 相对 PRD 的四处**有意简化**（不算未实现缺口，但本地继续开发时需要知道）。合并基线：PR #48，merge commit `258ad322e85e9944eb5d3e31ffd1d48d9376d8c2`。

相关文档：[`delivery-report.md`](./delivery-report.md)（Round 9）、[`pending-acceptance.md`](./pending-acceptance.md)。

---

## 1. 报告导出支持 Markdown / HTML / JSON / PDF / DOCX（已补齐）

**状态：** 已在本地通过 OpenSpec `report-export-pdf-docx` 补齐（PRD §5.2 / §10.3）。

**当前行为：**
- 接口：`GET /api/intelligence/reports/:id/export?format=md|html|json|pdf|docx`（平台代理）；引擎侧 `GET /v1/reports/:id/export`。
- 前端报告面板提供 `MD` / `HTML` / `JSON` / `PDF` / `DOCX` 五个导出按钮。
- `pdf` 使用内置 CJK `STSong-Light` (`UniGB-UTF16-H`) 与 `Helvetica` 渲染标准 `%PDF-1.7` 文档；`docx` 生成标准 OpenXML (`.docx`) ZIP 包（含 `word/document.xml` 中文字体与分级论断、引用链及摘录）；平台侧通过 `content_base64` 无损解码回传二进制流。
- `format` 为其他值时返回 400：`format must be md, html, json, pdf or docx`。
- 跨租户/跨工作区的报告 id 导出为 404（A02）。

**相关代码路径：**
| 路径 | 作用 |
|---|---|
| `services/loom-engine/flow/gaps9.ts` → `renderReport` | 渲染 md / html / json / pdf / docx |
| `services/loom-engine/flow/reports.ts` → `exportReport` | 按 scope 取报告后调用 `renderReport` |
| `services/loom-engine/flow/server.ts` | `GET /v1/reports/:id/export` |
| `backend/internal/flow/gaps9.go`（及路由注册） | 平台转发导出（支持 `content_base64` 二进制解码） |
| `web/src/services/api/intelligence.ts` → `exportReport` | 前端类型支持 `"md" \| "html" \| "json" \| "pdf" \| "docx"` |
| `web/src/pages/intelligence/reports-panel.tsx` | 导出按钮 |
| `services/loom-engine/flow/gaps9.test.ts`、`backend/internal/flow/gaps9_test.go` | 导出与跨租户 404 测试 |

---

## 2. ~~采集保存的是规范化条目 + digest，不是原始 HTTP 响应字节~~（已升级：连接器原始 HTTP 响应字节归档 + Scoped 完整性校验下载）

**状态：** 已完成（OpenSpec 变更 `raw-http-response-archive`）。在保留每个修订 `raw_item`（64 KiB 规范化条目）与 `raw_digest` 的基础上，新增了连接器层原始 HTTP 响应字节捕获、按租户/工作区前缀的对象存储归档以及带 SHA-256 完整性校验的下载接口（PRD §6.3）。

**当前行为：**
- RSS / Atom（`rss.ts`）、Telegram（`connectors.ts` Telethon JSON / `t.me/s` HTML）、WeChat（Atom 网关 XML）连接器在抓取时保留原始 HTTP 状态码、响应头、`Content-Type` 与原始响应体（`raw_response`）。
- 每次采集 run 在 `flow_collection_runs` 中记录 `raw_response_sha256`、`raw_response_bytes`、`raw_response_meta`；当配置对象存储写入器（`objectStorePut` / MinIO）时，将原始字节归档至 `tenants/{tenant_id}/workspaces/{workspace_id}/raw-runs/{source_id}/{run_id}` 并记录 `raw_response_key`。
- `GET /v1/collection-runs/:id/raw-response`（`downloadRawResponse`）先校验租户/工作区归属与前缀（跨租户返回 404，不触达对象存储），再校验对象存储返回字节的 SHA-256 与 `raw_response_sha256` 一致（被篡改则返回 502）。

**相关代码路径：**
| 路径 | 作用 |
|---|---|
| `services/loom-engine/src/lib/server/rss.ts`、`services/loom-engine/flow/connectors.ts` | 捕获原始 HTTP 响应体与元数据 |
| `services/loom-engine/flow/migrations/0017_raw_response_archive.sql` | `raw_response_key` / `raw_response_sha256` / `raw_response_bytes` / `raw_response_meta` 列 |
| `services/loom-engine/flow/gaps9.ts` → `presignPut`、`archiveRawResponse`、`downloadRawResponse` | 对象存储归档与带鉴权完整性校验下载 |
| `services/loom-engine/flow/engine.ts` → `execute` | 采集入库时归档原始响应并写入 `flow_collection_runs` |
| `services/loom-engine/flow/server.ts` | `GET /v1/collection-runs/:id/raw-response` |
| `services/loom-engine/flow/gaps9.test.ts` | §6.3 原始 HTTP 响应归档、跨租户 404 与篡改 502 测试 |

---

## 3. Embed 作业已接线，但仅有 `none` / `hash` 提供者；未用于检索（向量检索为 PRD 可选项）

**分类：** 可选能力未完整落地。PRD §6.4 要求可排队的 embed/analyze 派生作业；§7.3 向量检索为可选项。当前作业队列与表结构齐全，嵌入提供者只有 `none`（跳过）和 `hash`（本地特征哈希）；向量**不参与**文档检索。

**当前行为：**
- 采集/ enrichment 提交时按 content hash 入队 `flow_derived_jobs`（`embed` 与 `analyze`）。
- `derivedTick` 循环处理队列：`EMBED_PROVIDER=none`（默认）将 embed 记为 `skipped`；`hash` 写入 `flow_document_embeddings`。
- Analyze 为词法分析（语言、数字事实、监控关键词命中），不调用模型。
- 搜索仍走现有 FTS / 过滤路径，不读 embedding 表。

**相关代码路径：**
| 路径 | 作用 |
|---|---|
| `services/loom-engine/flow/gaps9.ts` → `embedderFromEnv`、`hashEmbedder`、`enqueueDerived`、`derivedTick` | 提供者与作业循环 |
| `services/loom-engine/flow/main.ts` | `EMBED_PROVIDER` 启动 `derived` 循环 |
| `flow_derived_jobs` / `flow_document_embeddings` 表 | 作业与向量存储 |
| `services/loom-engine/flow/gaps9.test.ts` | embed/analyze 幂等与 hash 写入 |
| `.env.flow.example`（若有 `EMBED_PROVIDER`） | 环境变量说明 |

**建议本地后续步骤：**
1. 实现真实提供者（如 OpenAI / 本地 sentence-transformers），在 `embedderFromEnv` 增加分支，配置 `EMBED_PROVIDER`、模型名、维度、API 密钥。
2. 检索侧增加可选向量路径（pgvector 或外部索引），与现有 FTS 做混合排序；默认关闭以符合「可选项」。
3. 按 content hash 去重，避免重复计费；为失败重试与预算预留接现有 billing。
4. 验收：换模型后旧向量是否重建、跨租户向量不可见。

---

## 4. ~~平台代理的媒体下载单文件上限 25 MiB~~（已升级：流式透传最高 512 MiB + 短时 Scoped 预签名 URL）

**状态：** 已完成（OpenSpec 变更 `media-download-streaming-presign`）。PRD §10.4 先鉴权再下载的契约在保持不变的前提下，消除了 Go 平台 4 MiB JSON 缓冲截断与 25 MiB Base64 内存膨胀限制，同时支持短时 Scoped SigV4 预签名 URL 发牌模式。

**当前行为：**
- `GET /api/intelligence/media?document_id&object_key`（默认代理模式）：Go 平台通过 `EngineClient.DoStream` 携带 `X-Flow-Accept-Stream: 1` 请求引擎 `/v1/media`，引擎校验租户/工作区前缀、文档引用且 `status=stored`、SHA-256 完整性后，直接以二进制流（`X-Flow-Media-Stream: 1`，上限 `MEDIA_STREAM_MAX = 512 MiB`，可通过 `MEDIA_DOWNLOAD_MAX_BYTES` 调整）透传至客户端；同时保留对旧版 JSON `data_base64`（25 MiB 上限）响应的向后兼容。
- `GET /api/intelligence/media?document_id&object_key&mode=presign&expires=60`（短时预签名模式）：先执行完全一致的租户/工作区与文档引用鉴权（未授权/路径穿越一律返回 404，绝不签发 URL），通过后返回短时（15~300 秒，默认 60 秒）SigV4 预签名 GET URL（优先使用 `MINIO_PUBLIC_ENDPOINT`），供大文件直连下载。
- `Cache-Control: private, no-store`；跨工作区/未存储/路径穿越 → 404，从不触达对象存储。

**相关代码路径：**
| 路径 | 作用 |
|---|---|
| `services/loom-engine/flow/gaps9.ts` → `MEDIA_STREAM_MAX`、`authorizeMedia`、`downloadMedia`、`presignGet` | 鉴权、512 MiB 流式读取与短时预签名 |
| `services/loom-engine/flow/server.ts` | `GET /v1/media` 二进制流式与 `mode=presign` 响应 |
| `backend/internal/flow/client.go`、`backend/internal/flow/gaps9.go` | `DoStream` 流式透传与 `mode=presign` 转发 |
| `services/loom-engine/flow/gaps9.test.ts`、`backend/internal/flow/gaps9_test.go` | >25 MiB 流式下载、短时预签名与跨租户 404 测试 |

---

## 维护说明

本地补齐上述能力后，请同步更新：
1. 本文档（划掉或改写对应节，并注明 PR / commit）；
2. `delivery-report.md` 的 Round 9「Still deviating」列表；
3. 若引入新的真实环境依赖，写入 `pending-acceptance.md`。
