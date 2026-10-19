# intelligence-workspace Specification

## Purpose
规范情报工作台（`/intelligence`）的会话缓存账号隔离、工作区成员角色投影与变更一致性、跨子视图 Agent 协作可达性，以及中文文案与 UI-Kit 组件规范。

## Requirements

### Requirement: 情报会话缓存按登录账号 Scope 隔离
前端情报会话目录的 `localStorage` 热启动缓存 MUST 按当前登录用户 ID（或匿名空态）隔离存储键；切换账号或未登录时 MUST NOT 读取或展示其他账号缓存的会话列表。

#### Scenario: 同浏览器切换账号不串会话缓存
- **WHEN** 用户 A 登录并拉取会话列表写入缓存后退出，用户 B 在同一浏览器登录进入 `/intelligence`
- **THEN** `cachedConversations` 只读取用户 B 专属缓存键（初始为空数组），不会展示用户 A 的会话列表

#### Scenario: 未登录状态不读写共享会话缓存
- **WHEN** 当前用户状态为空（未登录）
- **THEN** `cachedConversations` 返回空数组，且不会污染任意已登录用户的缓存键

### Requirement: 工作区成员角色准确投影与编辑一致
工作区成员查询接口（`GET /api/intelligence/members`）MUST 返回成员在当前工作区的有效角色（租户 `owner` 保持 `owner`，其余成员优先取当前工作区 `WorkspaceMembership.Role`：`admin`、`editor`、`viewer`）；成员角色修改接口（`PATCH /api/intelligence/members/:userId`）MUST 接受 `admin`、`editor`（兼容 `member`）、`viewer` 并同步更新工作区角色，使前端刷新后角色回显一致。

#### Scenario: 将成员设为 viewer 后刷新仍显示 viewer
- **WHEN** 工作区管理员将某成员角色修改为 `viewer` 并重新调用 `ListMembers`
- **THEN** 该成员返回的 `role` 为 `"viewer"`，且其丧失写权限（如 `intel.reports.write`）

#### Scenario: 支持 editor 与 member 角色设置并回显为 editor
- **WHEN** 工作区管理员将某成员角色修改为 `editor`（或兼容传入 `member`）并重新调用 `ListMembers`
- **THEN** 该成员返回的 `role` 为 `"editor"`，与邀请成员的角色枚举（`admin` / `editor` / `viewer`）保持一致

### Requirement: 情报页跨视图 Agent 可达性与中文 UI-Kit 规范
情报工作台（`/intelligence`）MUST 在全部 9 个导航子视图下保持「情报助手 & 监控」能力可达（专题视图内嵌展示，其余视图可通过顶栏入口打开抽屉或侧栏）；界面文案 MUST 统一为中文且移除过期的 Telegram 媒体不支持提示；提示横幅、未读/状态徽章、弹窗与抽屉 MUST 使用 UI-Kit 的 `Callout`、`StatusBadge`、`AppModal`、`AppDrawer`，不再直接引入 `antd` 的 `Badge`、`Alert` 或原生 `Modal`。

#### Scenario: 非专题视图下唤醒情报助手
- **WHEN** 用户处于「总览」「信源」或「关键词」等非专题 Tab
- **THEN** 顶栏显示「情报助手 / 监控」入口按钮，点击可在不离开当前视图的情况下打开当前工作区作用域的对话与监控面板

#### Scenario: 情报页面零 antd Alert/Badge/原生 Modal 残留
- **WHEN** 扫描 `web/src/pages/intelligence/` 下的组件导入
- **THEN** 不存在从 `antd` 直接导入 `Alert`、`Badge` 或 `Modal` 的代码，统一消费 `@/components/ui/*`

### Requirement: 情报报告支持五种格式（MD / HTML / JSON / PDF / DOCX）导出与跨租户隔离
情报报告导出接口（`GET /api/intelligence/reports/:id/export?format=...` 与引擎侧 `GET /v1/reports/:id/export`）MUST 支持 `md`、`html`、`json`、`pdf`、`docx` 五种导出格式，并在前端报告详情面板提供对应导出入口；导出内容 MUST 包含报告标题、元数据、正文、论断列表及其引用证据链（文档修订版本、证据 ID、URL 与片段 SHA-256）；跨租户或跨工作区的报告 ID 请求 MUST 返回 HTTP 404（A02）。

#### Scenario: 导出包含中文标题与引用链的 PDF 与 DOCX 报告
- **WHEN** 已授权工作区成员对本工作区报告调用 `exportReport` 并指定 `format="pdf"` 或 `format="docx"`
- **THEN** 引擎与平台返回对应的二进制文件流（PDF 以 `%PDF-1.` 开头且 `Content-Type: application/pdf`，DOCX 为包含 `word/document.xml` 的合法 OpenXML ZIP 且 `Content-Type: application/vnd.openxmlformats-officedocument.wordprocessingml.document`），其中包含报告标题、论断与引用证据信息

#### Scenario: 非法导出格式与跨租户导出被拒绝
- **WHEN** 请求传入未受支持的 `format`（如 `exe`）或尝试导出其他租户/工作区的报告 ID
- **THEN** 非法格式返回 HTTP 400（`format must be md, html, json, pdf or docx`），跨租户/跨工作区报告 ID 返回 HTTP 404

### Requirement: 情报媒体下载支持突破 25 MiB 的流式透传与短时 Scoped 预签名直链
情报媒体下载接口（`GET /api/intelligence/media` 与引擎侧 `GET /v1/media`）MUST 先执行租户/工作区前缀校验、路径穿越拦截、文档归属与 `status=stored` 引用核验（未通过一律返回 HTTP 404 且不触达对象存储）；鉴权通过后 MUST 支持二进制流式透传（`X-Flow-Accept-Stream: 1` 或 `stream=1`，突破 25 MiB Base64 代理限制并校验 SHA-256）以及短时 Scoped 预签名直链模式（`mode=presign`，签发绑定当前租户/工作区对象 Key 的短时 SigV4 URL）。

#### Scenario: 大于 25 MiB 的已存储媒体通过流式透传完成完整性校验与下载
- **WHEN** 已授权用户请求下载本工作区文档引用的 >25 MiB 已存储媒体对象（或平台通过流式协议代理）
- **THEN** 引擎与平台无需将文件转为 JSON Base64，在校验 SHA-256 一致后直接流式返回原始二进制字节及 `Cache-Control: private, no-store`

#### Scenario: 鉴权通过后签发短时 Scoped 预签名直链且拒绝跨工作区请求
- **WHEN** 已授权用户以 `mode=presign` 请求本工作区已存储媒体，或任意用户请求跨租户/跨工作区/未存储媒体
- **THEN** 本工作区请求返回包含有效短时 SigV4 `presigned_url` 与 `expected_sha256` 的受限凭据，跨租户/跨工作区/未存储请求返回 HTTP 404 且绝不生成预签名 URL

### Requirement: Traceable Raw HTTP Response Byte Archiving
采集引擎在执行情报源抓取时，MUST 支持捕获连接器收到的原始 HTTP 响应字节与状态元数据，计算完整原始响应字节的 SHA-256 摘要，并在配置对象存储写入器时按租户/工作区隔离键（`tenants/{tenant_id}/workspaces/{workspace_id}/raw-runs/{source_id}/{run_id}`）归档原始字节；读取归档对象时 MUST 先校验租户/工作区归属与 SHA-256 完整性。

#### Scenario: Raw HTTP response bytes archived with digest on collection run
- **WHEN** 采集作业从 RSS、Telegram 或 WeChat 连接器获取到包含原始 HTTP 响应体（`raw_response`）的批次并提交入库
- **THEN** `flow_collection_runs` MUST 记录完整原始响应字节的 `raw_response_sha256`、`raw_response_bytes` 与 `raw_response_meta`，并在配置 `objectStorePut` 时将原始字节写入 `tenants/{tenant_id}/workspaces/{workspace_id}/raw-runs/{source_id}/{run_id}` 且记录 `raw_response_key`

#### Scenario: Scoped raw response download verifies tenant ownership and SHA-256 integrity
- **WHEN** 调用方请求下载指定采集 run 的原始 HTTP 响应归档
- **THEN** 引擎 MUST 先校验该 run 属于当前租户与工作区且 `raw_response_key` 位于当前租户/工作区前缀内；跨租户或未归档请求 MUST 返回 `404` 且不触达对象存储；若对象存储返回字节的 SHA-256 与 `raw_response_sha256` 不一致，MUST 返回 `502`

### Requirement: Pluggable OpenAI-Compatible Embedding Provider and Optional Hybrid Vector Search
引擎 MUST 支持可插拔的 OpenAI 兼容 HTTP Embedding 提供者（`EMBED_PROVIDER=openai`）以及本地 `hash` / `none` 提供者；当启用向量检索（`vector=true` 或 `VECTOR_SEARCH_ENABLED=true`）且配置有效 `embedder` 时，`searchDocuments` MUST 在当前租户与工作区范围内结合 FTS 与当前模型（`provider = embedder.name`）的向量余弦相似度进行混合检索排序。

#### Scenario: OpenAI-compatible embedder generates normalized vectors for derived jobs
- **WHEN** 配置 `EMBED_PROVIDER=openai`（含 `EMBED_API_KEY`、`EMBED_MODEL`、`EMBED_DIMS`）并运行 `derivedTick`
- **THEN** 引擎 MUST 调用 `/v1/embeddings` 接口获取文本分块向量、执行 L2 归一化后写入 `flow_document_embeddings`，并将 `flow_derived_jobs` 标记为 `succeeded`（记录 `provider` 为 `openai:<model>`）

#### Scenario: Hybrid vector search recalls semantic matches within tenant and active provider scope
- **WHEN** 调用 `searchDocuments` 且开启 `vector: true`
- **THEN** 引擎 MUST 仅读取当前 `tenant_id`、`workspace_id`、文档 `current_revision` 且 `provider = embedder.name` 的向量计算余弦相似度，与 FTS 结果合并排序；跨租户文档或旧模型（不同 `provider`）的向量 MUST NOT 混入检索结果
