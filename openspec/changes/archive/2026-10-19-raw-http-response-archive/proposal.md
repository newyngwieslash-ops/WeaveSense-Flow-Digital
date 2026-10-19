# Proposal: raw-http-response-archive

## Why
根据 PRD §6.3 与 `docs/flow/known-limitations.md` §2，当前采集仅保存解析后的规范化 `ConnectorItem` JSON（上限 64 KiB）及其 SHA-256 摘要，连接器层获取到的原始 HTTP 响应字节（RSS/Atom XML、Telegram HTML/JSON、WeChat Atom XML）在解析后即丢弃。为满足合规与审计级原始响应可追溯要求，需在连接器与引擎层捕获原始 HTTP 响应字节、计算完整 SHA-256 摘要、按租户/工作区隔离前缀（`tenants/{tenant_id}/workspaces/{workspace_id}/raw-runs/{source_id}/{run_id}`）归档至对象存储，并提供带租户鉴权与 SHA-256 完整性校验的下载能力。

## What Changes
1. **连接器原始响应捕获（`services/loom-engine/src/lib/server/rss.ts`、`services/loom-engine/flow/connectors.ts`、`services/loom-engine/flow/engine.ts`）**：
   - `FeedFetch` 与 `ConnectorBatch` 新增可选 `raw_response`（含 HTTP `status`、`content_type`、`headers`、`body` 原始字节/文本及 `sha256`）。
   - RSS、Telegram（Telethon worker JSON / `t.me/s` HTML）、WeChat（Atom 网关 XML）连接器在请求成功后保留原始 HTTP 响应负载并随 `ConnectorBatch` 返回。
2. **对象存储归档、数据库记录与带鉴权下载（`services/loom-engine/flow/migrations/0017_raw_response_archive.sql`、`services/loom-engine/flow/gaps9.ts`、`services/loom-engine/flow/engine.ts`、`services/loom-engine/flow/server.ts`）**：
   - `flow_collection_runs` 新增 `raw_response_key`、`raw_response_sha256`、`raw_response_bytes`、`raw_response_meta` 列（同时保留原有 `raw_digest` 与 `raw_items`）。
   - `gaps9.ts` 新增 `presignPut`、`objectStoreWriterFromEnv`、`scopedRawRunKey`、`archiveRawResponse` 与 `downloadRawResponse`（校验租户/工作区前缀、采集 run 归属与 SHA-256 完整性，篡改返回 502，跨租户返回 404）。
   - `server.ts` 新增 `GET /v1/collection-runs/:id/raw-response` 路由。
3. **测试与文档同步（`services/loom-engine/flow/gaps9.test.ts`、`docs/flow/known-limitations.md`）**：
   - 在 `gaps9.test.ts` 补充原始 HTTP 响应字节归档、按租户权限下载、跨租户 404 拒绝与篡改 SHA-256 校验失败（502）测试。
   - 更新 `docs/flow/known-limitations.md` §2 状态说明。

## Impact
- **Affected specs**: `intelligence-workspace`
- **Affected code**:
  - `services/loom-engine/src/lib/server/rss.ts`
  - `services/loom-engine/flow/connectors.ts`
  - `services/loom-engine/flow/migrations/0017_raw_response_archive.sql`
  - `services/loom-engine/flow/gaps9.ts`
  - `services/loom-engine/flow/engine.ts`
  - `services/loom-engine/flow/server.ts`
  - `services/loom-engine/flow/gaps9.test.ts`
  - `docs/flow/known-limitations.md`
