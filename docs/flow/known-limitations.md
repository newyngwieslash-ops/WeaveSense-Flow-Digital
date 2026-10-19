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

## 3. ~~Embed 作业已接线，但仅有 `none` / `hash` 提供者；未用于检索~~（已升级：OpenAI 兼容 HTTP Embedding 提供者 + 可选混合向量检索）

**状态：** 已完成（OpenSpec 变更 `embedding-provider-hybrid-search`）。PRD §6.4 派生 embed 作业与 §7.3 可选向量检索已完整打通，支持 `none`（默认跳过）、`hash`（本地特征哈希）与 `openai`（真实 OpenAI 兼容 `/v1/embeddings`）三种提供者，并在 `Engine.searchDocuments` 中支持 FTS + 向量余弦相似度混合检索。

**当前行为：**
- 采集/enrichment 提交时按 content hash 入队 `flow_derived_jobs`（`embed` 与 `analyze`），内容未变绝不重复计费。
- `embedderFromEnv` 支持 `EMBED_PROVIDER=openai`（读取 `EMBED_BASE_URL`、`EMBED_API_KEY`、`EMBED_MODEL`、`EMBED_DIMS`），调用 `/v1/embeddings` 并对返回向量执行 L2 归一化后写入 `flow_document_embeddings`（`provider` 记为 `openai:<model>`）。
- `Engine.searchDocuments` 默认保持纯 FTS/子串检索（符合 PRD「向量检索为可选项」）；当传入 `vector: true` 或设置环境变量 `VECTOR_SEARCH_ENABLED=true` 且配置有效 `embedder` 时，自动按 `tenant_id`、`workspace_id`、`current_revision` 与当前 `embedder.name` 过滤向量并计算余弦相似度，以 `rank = fts_rank + vector_score * 0.5` 做混合排序，同时严格隔离跨租户与旧模型向量。

**相关代码路径：**
| 路径 | 作用 |
|---|---|
| `services/loom-engine/flow/gaps9.ts` → `embedderFromEnv`、`openaiEmbedder`、`hashEmbedder`、`cosineSimilarity`、`derivedTick` | 提供者、L2 归一化与派生作业循环 |
| `services/loom-engine/flow/engine.ts` → `searchDocuments` | FTS + 可选混合向量检索与排序 |
| `services/loom-engine/flow/gaps9.test.ts` | `openaiEmbedder`、混合检索语义召回、跨模型与跨租户隔离测试 |

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
