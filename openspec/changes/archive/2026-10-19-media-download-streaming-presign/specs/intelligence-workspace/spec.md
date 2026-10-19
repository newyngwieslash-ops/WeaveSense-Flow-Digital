# intelligence-workspace Delta Specification

## ADDED Requirements

### Requirement: 情报媒体下载支持突破 25 MiB 的流式透传与短时 Scoped 预签名直链
情报媒体下载接口（`GET /api/intelligence/media` 与引擎侧 `GET /v1/media`）MUST 先执行租户/工作区前缀校验、路径穿越拦截、文档归属与 `status=stored` 引用核验（未通过一律返回 HTTP 404 且不触达对象存储）；鉴权通过后 MUST 支持二进制流式透传（`X-Flow-Accept-Stream: 1` 或 `stream=1`，突破 25 MiB Base64 代理限制并校验 SHA-256）以及短时 Scoped 预签名直链模式（`mode=presign`，签发绑定当前租户/工作区对象 Key 的短时 SigV4 URL）。

#### Scenario: 大于 25 MiB 的已存储媒体通过流式透传完成完整性校验与下载
- **WHEN** 已授权用户请求下载本工作区文档引用的 >25 MiB 已存储媒体对象（或平台通过流式协议代理）
- **THEN** 引擎与平台无需将文件转为 JSON Base64，在校验 SHA-256 一致后直接流式返回原始二进制字节及 `Cache-Control: private, no-store`

#### Scenario: 鉴权通过后签发短时 Scoped 预签名直链且拒绝跨工作区请求
- **WHEN** 已授权用户以 `mode=presign` 请求本工作区已存储媒体，或任意用户请求跨租户/跨工作区/未存储媒体
- **THEN** 本工作区请求返回包含有效短时 SigV4 `presigned_url` 与 `expected_sha256` 的受限凭据，跨租户/跨工作区/未存储请求返回 HTTP 404 且绝不生成预签名 URL
