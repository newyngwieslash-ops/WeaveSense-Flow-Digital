# Design: raw-http-response-archive

## 1. 架构与数据流设计
- **连接器层（`rss.ts` / `connectors.ts`）**：
  - `fetchFeed`、`fetchTelegram`、`fetchWechat` 在完成 HTTP 抓取后，将原始响应文本/字节、HTTP 状态码与 `Content-Type` 封装为 `raw_response: { status, content_type, headers, body }` 挂在 `ConnectorBatch` 上返回，同时完全保留现有的 `items` 解析逻辑。
- **引擎归档层（`gaps9.ts` / `engine.ts`）**：
  - 新增 `scopedRawRunKey(s, sourceId, runId)` 生成 `tenants/${s.tenant_id}/workspaces/${s.workspace_id}/raw-runs/${sourceId}/${runId}`。
  - 新增 `presignPut` 与 `objectStoreWriterFromEnv`（当环境变量 `MINIO_ENDPOINT` 等齐全且开启归档或注入 `objectStorePut` 时，使用 SigV4 `PUT` 写入私有桶）。
  - `Engine.execute` 在开启事务写入 `flow_collection_runs` 前调用 `archiveRawResponse`，将 `raw_response_key`、`raw_response_sha256`、`raw_response_bytes`、`raw_response_meta` 随采集完成状态一同原子落库。
- **鉴权下载与防篡改校验（`gaps9.ts` / `server.ts`）**：
  - `downloadRawResponse(db, store, s, { run_id })` 先查询 `flow_collection_runs WHERE tenant_id=$1 AND workspace_id=$2 AND id=$3`，校验 `raw_response_key` 非空、以 `tenants/${s.tenant_id}/workspaces/${s.workspace_id}/raw-runs/` 开头且不含 `..`，随后从对象存储读取字节并比对 `sha256 === raw_response_sha256`，不匹配则抛出 `502`。
