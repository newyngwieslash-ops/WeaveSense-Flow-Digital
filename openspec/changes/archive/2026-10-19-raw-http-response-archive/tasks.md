# Tasks

## 1. 连接器原始 HTTP 响应捕获与引擎归档实现

- [x] 1.1 新增 `services/loom-engine/flow/migrations/0017_raw_response_archive.sql`，在 `services/loom-engine/src/lib/server/rss.ts`、`services/loom-engine/flow/connectors.ts` 中捕获原始 HTTP 响应字节，并在 `services/loom-engine/flow/gaps9.ts`、`services/loom-engine/flow/engine.ts`、`services/loom-engine/flow/server.ts` 中实现 `archiveRawResponse`、`downloadRawResponse` 与 `GET /v1/collection-runs/:id/raw-response`

## 2. 单元测试与已知限制文档更新

- [x] 2.1 在 `services/loom-engine/flow/gaps9.test.ts` 中补充原始 HTTP 响应字节归档、按租户鉴权下载、跨租户 404 拒绝与篡改 SHA-256 校验失败（502）测试，更新 `docs/flow/known-limitations.md` §2，并运行 `node --experimental-strip-types --test services/loom-engine/flow/gaps9.test.ts` 验证通过

