# Design: embedding-provider-hybrid-search

## 1. OpenAI 兼容 Embedding 提供者（`gaps9.ts`）
- `openaiEmbedder({ baseUrl, apiKey, model, dims, fetchImpl })`：
  - `name`: `openai:${model}`，这样当切换模型时，`flow_document_embeddings` 的主键 `(tenant_id, document_id, revision, chunk_index, provider)` 天然隔离不同模型的向量，不会发生维度或空间混用。
  - 发送 `POST ${endpoint}`（若 `baseUrl` 未以 `/embeddings` 结尾则拼接 `/v1/embeddings`），请求体为 `{ model, input: texts, ...(dims ? { dimensions: dims } : {}) }`。
  - 对返回的每条 embedding 做 L2 归一化（`v.map(x => x / norm)`），使余弦相似度简化为点积并保证数值稳定。

## 2. 混合检索（`engine.ts`）
- `Engine.searchDocuments(s, o)`：
  - 默认 `useVector = Boolean((o.vector ?? this.vectorSearchEnabled) && this.embedder)`。
  - 未启用时完全走原有 SQL 查询，保持零回归。
  - 启用时：
    1. 执行原有 FTS + 子串查询获取词面候选。
    2. 调用 `this.embedder.embed([o.query])` 获取查询向量 `qVec`。
    3. 查询当前租户/工作区、当前修订（`d.current_revision = e.revision`）、当前模型（`e.provider = this.embedder.name`）且满足 `source_ids` / `from` / `to` 过滤条件的分块与向量。
    4. 计算 `cosineSimilarity(qVec, row.vector)`，过滤正向相似分块（`sim > 0.05`），与 FTS 候选按 `(document_id, revision, chunk_index)` 归并，`rank = fts_rank + vector_score * 0.5`，按 `rank DESC, published_at DESC NULLS LAST, document_id, chunk_index` 排序分页返回。
