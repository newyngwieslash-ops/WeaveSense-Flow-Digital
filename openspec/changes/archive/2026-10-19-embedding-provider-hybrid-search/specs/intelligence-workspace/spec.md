## ADDED Requirements

### Requirement: Pluggable OpenAI-Compatible Embedding Provider and Optional Hybrid Vector Search
引擎 MUST 支持可插拔的 OpenAI 兼容 HTTP Embedding 提供者（`EMBED_PROVIDER=openai`）以及本地 `hash` / `none` 提供者；当启用向量检索（`vector=true` 或 `VECTOR_SEARCH_ENABLED=true`）且配置有效 `embedder` 时，`searchDocuments` MUST 在当前租户与工作区范围内结合 FTS 与当前模型（`provider = embedder.name`）的向量余弦相似度进行混合检索排序。

#### Scenario: OpenAI-compatible embedder generates normalized vectors for derived jobs
- **WHEN** 配置 `EMBED_PROVIDER=openai`（含 `EMBED_API_KEY`、`EMBED_MODEL`、`EMBED_DIMS`）并运行 `derivedTick`
- **THEN** 引擎 MUST 调用 `/v1/embeddings` 接口获取文本分块向量、执行 L2 归一化后写入 `flow_document_embeddings`，并将 `flow_derived_jobs` 标记为 `succeeded`（记录 `provider` 为 `openai:<model>`）

#### Scenario: Hybrid vector search recalls semantic matches within tenant and active provider scope
- **WHEN** 调用 `searchDocuments` 且开启 `vector: true`
- **THEN** 引擎 MUST 仅读取当前 `tenant_id`、`workspace_id`、文档 `current_revision` 且 `provider = embedder.name` 的向量计算余弦相似度，与 FTS 结果合并排序；跨租户文档或旧模型（不同 `provider`）的向量 MUST NOT 混入检索结果
