# Tasks

## 1. 引擎侧媒体下载流式透传与短时 Scoped 预签名实现

- [x] 1.1 在 `services/loom-engine/flow/gaps9.ts` 与 `services/loom-engine/flow/server.ts` 中抽取 `authorizeMedia`，扩展支持 `mode="presign"` 短时 Scoped 预签名直链与 `stream=true`（`MEDIA_STREAM_MAX = 512 MiB`、`X-Flow-Media-Stream: 1` 二进制流响应），并在 `services/loom-engine/flow/gaps9.test.ts` 补充 >25 MiB 流式下载、短时预签名签发与跨租户 404 拦截测试，运行 `node --experimental-strip-types --test services/loom-engine/flow/gaps9.test.ts` 验证通过

## 2. 平台侧流式代理、测试与已知限制文档同步

- [x] 2.1 在 `backend/internal/flow/client.go` 新增 `DoStream`，在 `backend/internal/flow/gaps9.go` 的 `GET /api/intelligence/media` 中支持 `X-Flow-Media-Stream: 1` 流式二进制透传、`mode=presign` 预签名转发与 JSON `data_base64` 兼容，在 `backend/internal/flow/gaps9_test.go` 补充流式大文件透传与 `mode=presign` 测试并更新 `docs/flow/known-limitations.md` §4，运行 `cd backend && go test ./internal/flow -run TestGaps9RoutesForwardAndRender -v` 验证通过

