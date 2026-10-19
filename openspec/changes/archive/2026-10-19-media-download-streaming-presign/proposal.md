# Proposal: 大体积媒体下载流式透传与短时 Scoped 预签名直链（Task B2）

## Why

当前情报内容库的私有对象存储媒体下载链路（`GET /api/intelligence/media` → `GET /v1/media`）存在两处工程瓶颈（登记于 `docs/flow/known-limitations.md` §4）：
1. 引擎侧 `services/loom-engine/flow/gaps9.ts` 将对象整体读入内存并编码为 JSON `data_base64`，硬性限制 `MEDIA_DOWNLOAD_MAX = 25 * 1024 * 1024`（25 MiB），超出即返回 `413 object too large to proxy`；
2. 平台侧 `backend/internal/flow/client.go` 的 `EngineClient.Do` 对响应体设了 `4 MiB` 读取上限（`io.LimitReader(resp.Body, 4<<20)`），导致真实大于 3 MiB 的媒体附件在经过 Base64 膨胀后即被截断，无法支持大体积音视频与文档附件下载。

## What Changes

- **Loom Engine 媒体鉴权与双模式（流式透传 + 短时 Scoped 预签名）支持（`services/loom-engine/flow/gaps9.ts`、`services/loom-engine/flow/server.ts`）**：
  - 抽取统一的前置鉴权函数 `authorizeMedia(db, s, o)`：严格校验 `tenants/{tenant_id}/workspaces/{workspace_id}/` 前缀、拒绝 `..` 路径穿越、校验文档归属及 `media` 引用且 `status === "stored"`；未授权请求一律返回 404，绝不触达对象存储或签发 URL；
  - 新增短时 Scoped 预签名模式（`mode: "presign"` / `presignMedia`）：在通过完整归属与状态鉴权后，针对该受限对象 Key 签发短时（默认 60s，可配置 `15..300s`）SigV4 预签名 GET 直链，返回 `{ mode: "presign", object_key, filename, content_type, expected_sha256, presigned_url, expires_in }`；
  - 扩展流式与大体积代理能力：支持可配置流式下载上限 `MEDIA_STREAM_MAX`（默认 `512 MiB`），在 `GET /v1/media` 收到 `stream=1` 或 `X-Flow-Accept-Stream: 1` 请求头时，完成 SHA-256 完整性校验后直接以二进制流（带 `X-Flow-Media-Stream: 1`、`X-Flow-Filename`、`X-Flow-Sha256`、`Cache-Control: private, no-store`）响应，彻底消除 Base64 膨胀；同时保留默认 JSON `data_base64` 响应以兼容既有调用方。
- **Go 平台流式代理与预签名透传（`backend/internal/flow/client.go`、`backend/internal/flow/gaps9.go`）**：
  - 在 `EngineClient` 新增 `DoStream` 方法，移除 JSON 4 MiB `LimitReader` 限制并携带 `X-Flow-Accept-Stream: 1`；
  - 在 `GET /api/intelligence/media` 中：
    - 若请求 `mode=presign`，转发并返回短时预签名 JSON（`Cache-Control: private, no-store`）；
    - 若引擎返回 `X-Flow-Media-Stream: 1` 二进制流，直接通过 `io.Copy` 流式透传原始字节并设置 `Content-Disposition`、`X-Content-Type-Options: nosniff`、`Cache-Control: private, no-store`；
    - 若引擎（或测试桩）返回 JSON `data_base64` 信封，仍兼容解码回传。
- **测试与文档更新**：
  - 在 `services/loom-engine/flow/gaps9.test.ts` 与 `backend/internal/flow/gaps9_test.go` 补充 >25 MiB 大体积媒体流式下载、短时 Scoped 预签名签发、跨租户/未存储 404 拦截及 SHA-256 篡改 502 校验；
  - 同步更新 `docs/flow/known-limitations.md` §4。

## Capabilities

### New Capabilities

### Modified Capabilities
- `intelligence-workspace`: 情报媒体附件下载支持突破 25 MiB 的二进制流式透传与鉴权后短时 Scoped 预签名直链，保持跨租户/跨工作区 404 隔离与 SHA-256 完整性核验。

## Impact

- 引擎：`services/loom-engine/flow/gaps9.ts`、`services/loom-engine/flow/server.ts`、`services/loom-engine/flow/gaps9.test.ts`
- 平台后端：`backend/internal/flow/client.go`、`backend/internal/flow/gaps9.go`、`backend/internal/flow/gaps9_test.go`
- 文档：`docs/flow/known-limitations.md`
