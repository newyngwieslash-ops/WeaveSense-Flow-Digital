# Proposal: embedding-provider-hybrid-search

## Why
根据 PRD §6.4 / §7.3 与 `docs/flow/known-limitations.md` §3，当前 `flow_derived_jobs` 与 `flow_document_embeddings` 表结构及派生作业循环已就绪，但嵌入提供者仅有 `none`（跳过）和 `hash`（本地特征哈希），缺少真实的 OpenAI 兼容 HTTP Embedding 提供者实现，且 `flow_document_embeddings` 中的向量尚未接入 `Engine.searchDocuments` 混合检索链路。补齐 OpenAI 兼容提供者与可选混合向量检索，可在保持默认零成本（`EMBED_PROVIDER=none`）的同时，支持真实语义向量检索与跨模型/跨租户隔离。

## What Changes
1. **真实 OpenAI 兼容 HTTP Embedding 提供者（`services/loom-engine/flow/gaps9.ts`）**：
   - 新增 `openaiEmbedder(o)`：调用 OpenAI 兼容 `POST /v1/embeddings` 接口（支持自定义 `baseUrl`、`apiKey`、`model`、`dims` 与 L2 归一化），校验返回向量数量与维度。
   - 扩展 `embedderFromEnv` 支持 `EMBED_PROVIDER=openai`（读取 `EMBED_BASE_URL`、`EMBED_API_KEY`、`EMBED_MODEL`、`EMBED_DIMS`），保留 `none` 与 `hash`。
   - 新增 `cosineSimilarity(a, b)` 纯函数。
2. **可选混合向量 + FTS 检索（`services/loom-engine/flow/engine.ts`）**：
   - `Engine` 新增 `embedder: Embedder | null = embedderFromEnv()` 与 `vectorSearchEnabled = process.env.VECTOR_SEARCH_ENABLED === "true"`。
   - `Engine.searchDocuments` 支持 `vector?: boolean`（或 `this.vectorSearchEnabled`）：开启且配置 `embedder` 时，对当前修订且匹配 `embedder.name` 的 `flow_document_embeddings`（严格限制在当前 `tenant_id` 与 `workspace_id`）计算余弦相似度，与 FTS 召回结果做混合排序（`rank = fts_rank + vector_score * 0.5`），支持召回纯语义匹配（词面未直接命中）的分块，同时确保切换模型（`provider` 变化）或跨租户向量互不干扰。
3. **测试与文档更新（`services/loom-engine/flow/gaps9.test.ts`、`docs/flow/known-limitations.md`）**：
   - 在 `gaps9.test.ts` 中补充 `openaiEmbedder`（通过 mock `fetchImpl` 验证请求载荷与 L2 归一化向量入库）、混合检索召回与排序、模型切换隔离及跨租户向量不可见测试。
   - 更新 `docs/flow/known-limitations.md` §3 说明。

## Impact
- **Affected specs**: `intelligence-workspace`
- **Affected code**:
  - `services/loom-engine/flow/gaps9.ts`
  - `services/loom-engine/flow/engine.ts`
  - `services/loom-engine/flow/gaps9.test.ts`
  - `docs/flow/known-limitations.md`
