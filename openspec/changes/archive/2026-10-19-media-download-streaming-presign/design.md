# Design: 大体积媒体下载流式透传与短时 Scoped 预签名直链（Task B2）

## Context

`services/loom-engine/flow/gaps9.ts` 的 `downloadMedia` 目前固定以 JSON `data_base64` 返回对象内容并设置 `MEDIA_DOWNLOAD_MAX = 25 MiB`，同时 `backend/internal/flow/client.go` 的 `EngineClient.Do` 将响应限制在 4 MiB。为消除 `docs/flow/known-limitations.md` §4 的大文件下载限制，需要支持流式二进制透传与短时 Scoped 预签名直链。

## Goals / Non-Goals

**Goals:**
- 保持 PRD §10.4 的强制前置鉴权（租户/工作区前缀、文档引用、`status=stored`、路径穿越防护）；
- 支持 `mode=presign` 返回短时（默认 60s）Scoped SigV4 预签名直链；
- 支持 Engine → Platform → Client 全链路二进制流式传输（`MEDIA_STREAM_MAX = 512 MiB`），消除 Base64 内存翻倍与 4 MiB / 25 MiB 截断限制；
- 保持既有 `downloadMedia` 单元测试与 JSON `data_base64` 调用方 100% 兼容。

**Non-Goals:**
- 不开放对象存储匿名公开读权限（MinIO 桶仍保持 `anonymous none`）。

## Decisions

1. **统一前置鉴权 `authorizeMedia` 与预签名模式（`services/loom-engine/flow/gaps9.ts`）**
   - `authorizeMedia(db, s, o)` 校验 `key.startsWith("tenants/${s.tenant_id}/workspaces/${s.workspace_id}/")`、`!key.includes("..")`、`flow_documents` 存在且 `media` 数组中包含 `url === key && status === "stored"`；任何一项不满足立即抛出 `HttpError(404, "object not found")`。
   - 当 `o.mode === "presign"` 时，在 `authorizeMedia` 通过后调用 `presignGet` 生成短时（默认 60s，限制在 `15..300s`）预签名 URL，返回 `{ mode: "presign", object_key: key, content_type, expected_sha256, filename, presigned_url, expires_in }`。

2. **大体积流式读取与 HTTP 二进制流响应（`services/loom-engine/flow/gaps9.ts`、`services/loom-engine/flow/server.ts`）**
   - 新增 `MEDIA_STREAM_MAX = 512 * 1024 * 1024`（512 MiB，可通过 `MEDIA_DOWNLOAD_MAX_BYTES` 环境变量覆盖）；`objectStoreFromEnv` 支持可配置上限。
   - `downloadMedia` 在 `o.stream === true`（或 `o.max_bytes` 放宽）时允許最高 `MEDIA_STREAM_MAX` 字节，并在 `o.stream === true` 时返回原始 `bytes` Buffer 而不生成冗长的 `data_base64` 字符串。
   - `server.ts` 的 `GET /v1/media`：
     - 当 `url.searchParams.get("mode") === "presign"` 时返回预签名 JSON；
     - 当 `url.searchParams.get("stream") === "1"` 或 `req.headers["x-flow-accept-stream"] === "1"` 时，返回 `200 OK` 原始二进制流并设置 `X-Flow-Media-Stream: 1`、`X-Flow-Filename`、`X-Flow-Sha256`、`Cache-Control: private, no-store`。

3. **Go 平台 `EngineClient.DoStream` 与 `/api/intelligence/media` 透传（`backend/internal/flow/client.go`、`backend/internal/flow/gaps9.go`）**
   - `EngineClient.DoStream` 签发相同的短时 `ExecutionContext` Bearer Token，附带可选请求头（如 `X-Flow-Accept-Stream: 1`），返回原始 `*http.Response` 供调用方流式读取（上限设为 `MEDIA_STREAM_MAX = 512 MiB`）。
   - `GET /api/intelligence/media`：
     - 若查询参数含 `mode=presign`，转发至引擎并原样返回 JSON 预签名回执；
     - 若引擎响应头含 `X-Flow-Media-Stream: 1`，直接设置 `Content-Disposition`、`X-Content-Type-Options: nosniff`、`Cache-Control: private, no-store` 并通过 `io.Copy` 流式写回客户端；
     - 若引擎返回传统 JSON `data_base64`，解码后写回客户端（保持向后兼容）。
