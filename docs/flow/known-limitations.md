# 已知简化与本地后续开发说明

本文档记录当前 `main` 相对 PRD 的四处**有意简化**（不算未实现缺口，但本地继续开发时需要知道）。合并基线：PR #48，merge commit `258ad322e85e9944eb5d3e31ffd1d48d9376d8c2`。

相关文档：[`delivery-report.md`](./delivery-report.md)（Round 9）、[`pending-acceptance.md`](./pending-acceptance.md)。

---

## 1. 报告导出仅支持 Markdown / HTML / JSON（无 PDF / DOCX）

**分类：** 做法不同（deviation D）。PRD §5.2 / §10.3 要求可导出报告；未强制指定格式。当前实现提供三种文本格式，不生成 PDF 或 DOCX。

**当前行为：**
- 接口：`GET /api/intelligence/reports/:id/export?format=md|html|json`（平台代理）；引擎侧 `GET /v1/reports/:id/export`。
- 前端报告面板只提供 `md` / `html` / `json` 三个按钮。
- `format` 为其他值时返回 400：`format must be md, html or json`。
- 跨租户/跨工作区的报告 id 导出为 404（A02）。

**相关代码路径：**
| 路径 | 作用 |
|---|---|
| `services/loom-engine/flow/gaps9.ts` → `renderReport` | 渲染 md / html / json |
| `services/loom-engine/flow/reports.ts` → `exportReport` | 按 scope 取报告后调用 `renderReport` |
| `services/loom-engine/flow/server.ts` | `GET /v1/reports/:id/export` |
| `backend/internal/flow/gaps9.go`（及路由注册） | 平台转发导出 |
| `web/src/services/api/intelligence.ts` → `exportReport` | 前端类型限定为 `"md" \| "html" \| "json"` |
| `web/src/pages/intelligence/reports-panel.tsx` | 导出按钮 |
| `services/loom-engine/flow/gaps9.test.ts` | 导出与跨租户 404 测试 |

**建议本地后续步骤：**
1. 选定库：例如服务端用 Chromium/Puppeteer 或 `md-to-pdf` 出 PDF；用 `docx` / pandoc 出 DOCX。
2. 在 `renderReport` 增加 `pdf` / `docx` 分支，或抽独立 worker，避免 Loom Engine 主进程拉起重型依赖。
3. 扩展前端类型与按钮；为大体积附件考虑异步任务 + 下载链接（可复用媒体下载的权限模型）。
4. 补测试：格式合法性、跨租户 404、中文标题与引用列表渲染。

---

## 2. 采集保存的是规范化条目 + digest，不是原始 HTTP 响应字节

**分类：** 做法不同（deviation D）。PRD §6.3 要求可追溯的原始响应；当前保存的是连接器解析后的条目 JSON（上限 64 KiB）及其全文 sha256，以及整次 run 的 batch digest。Feed 的原始 HTTP body 不落库。

**当前行为：**
- 每个文档修订写入 `raw_item`（规范化 connector item 的 JSON，过大则截断为 head + size）和 `raw_digest`（完整 canonical JSON 的 sha256）。
- 每次采集 run 写入 `raw_digest`（各条目 digest 列表的摘要）和 `raw_items`（条目数）。
- RSS / Telegram / WeChat 等连接器在入库前已解析为统一 item 结构；HTTP 原始字节在连接器层即丢弃。

**相关代码路径：**
| 路径 | 作用 |
|---|---|
| `services/loom-engine/flow/gaps9.ts` → `RAW_ITEM_MAX`、`rawItem` | 64 KiB 上限与 digest |
| `services/loom-engine/flow/engine.ts` → `execute` 入库路径 | `INSERT INTO flow_document_revisions (... raw_item, raw_digest ...)`；更新 `flow_collection_runs.raw_digest` / `raw_items` |
| 引擎迁移 `0016`（及后续） | `raw_item` / `raw_digest` 列 |
| `services/loom-engine/flow/gaps9.test.ts` | §6.3 修订与 run digest 断言 |
| 各连接器（如 `connectors.ts`、RSS/Telegram/WeChat 实现） | 产出规范化 `ConnectorItem`，不回传原始字节 |

**建议本地后续步骤：**
1. 若需合规/审计级原始响应：在连接器层把 HTTP status、headers、body（或压缩归档）写入对象存储，DB 只存 `object_key` + sha256。
2. 与现有 Telegram 媒体路径对齐：`tenants/{tenant}/workspaces/{workspace}/...`，并走同一套 RLS / 下载鉴权。
3. 注意体积与保留策略（TTL、按租户配额）；默认仍可只保留 digest，原始字节按需开启。
4. 补测试：原始对象可按权限下载；篡改 body 后 digest 不匹配应失败。

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

## 4. 平台代理的媒体下载单文件上限 25 MiB

**分类：** 做法不同 / 工程约束。PRD §10.4 要求先鉴权再下载对象存储中的媒体；当前由平台代理字节（不向浏览器签发桶 URL），单文件上限 `MEDIA_DOWNLOAD_MAX = 25 * 1024 * 1024`，超限返回 413。

**当前行为：**
- `GET /api/intelligence/media?document_id&object_key`：校验租户/工作区前缀、文档引用且 `status=stored`，再对内部 MinIO 做 SigV4 预签名 GET，校验 sha256 后 base64 返回。
- `Cache-Control: private, no-store`；跨工作区/未存储/路径穿越 → 404，从不触达对象存储。
- Content-Length 或实际字节数超过 25 MiB → `413 object too large to proxy`。

**相关代码路径：**
| 路径 | 作用 |
|---|---|
| `services/loom-engine/flow/gaps9.ts` → `MEDIA_DOWNLOAD_MAX`、`objectStoreFromEnv`、`downloadMedia`、`presignGet` | 上限、代理读取、鉴权 |
| `services/loom-engine/flow/server.ts` | `GET` 媒体下载路由 |
| `backend/internal/flow/gaps9.go`（及路由） | 平台代理到引擎并回传字节 |
| `services/loom-engine/flow/gaps9.test.ts` | 跨租户/完整性/路径穿越 |
| `docs/flow/pending-acceptance.md` Round 9 | 真实 MinIO 验收待办 |

**建议本地后续步骤：**
1. 大文件改为短时、scoped 的预签名 URL（或分块/Range），浏览器直连对象存储，平台只做鉴权发牌。
2. 若仍走代理：提高上限或改为流式透传（勿整文件进内存 / base64）。
3. 在真实 MinIO 上验收：本工作区可下、外工作区 404、超限 413（见 pending-acceptance）。
4. 与 Telegram 媒体 key 迁移、对象恢复（A18）流程一起做集成演练。

---

## 维护说明

本地补齐上述能力后，请同步更新：
1. 本文档（划掉或改写对应节，并注明 PR / commit）；
2. `delivery-report.md` 的 Round 9「Still deviating」列表；
3. 若引入新的真实环境依赖，写入 `pending-acceptance.md`。
