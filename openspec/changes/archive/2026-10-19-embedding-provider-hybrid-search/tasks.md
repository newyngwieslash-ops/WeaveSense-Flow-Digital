# Tasks

## 1. OpenAI 兼容 Embedding 提供者与混合向量检索实现

- [x] 1.1 在 `services/loom-engine/flow/gaps9.ts` 中实现 `openaiEmbedder`、`cosineSimilarity` 并扩展 `embedderFromEnv` 支持 `EMBED_PROVIDER=openai`；在 `services/loom-engine/flow/engine.ts` 的 `searchDocuments` 中实现可选混合向量 + FTS 检索（按租户、工作区、当前修订与 `embedder.name` 隔离）

## 2. 单元测试与已知限制文档更新

- [x] 2.1 在 `services/loom-engine/flow/gaps9.test.ts` 中补充 `openaiEmbedder`、混合向量检索召回、模型切换隔离与跨租户向量不可见测试，更新 `docs/flow/known-limitations.md` §3，并运行 `node --experimental-strip-types --test services/loom-engine/flow/gaps9.test.ts` 验证通过

