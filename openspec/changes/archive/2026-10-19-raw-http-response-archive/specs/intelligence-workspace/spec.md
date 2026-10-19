## ADDED Requirements

### Requirement: Traceable Raw HTTP Response Byte Archiving
采集引擎在执行情报源抓取时，MUST 支持捕获连接器收到的原始 HTTP 响应字节与状态元数据，计算完整原始响应字节的 SHA-256 摘要，并在配置对象存储写入器时按租户/工作区隔离键（`tenants/{tenant_id}/workspaces/{workspace_id}/raw-runs/{source_id}/{run_id}`）归档原始字节；读取归档对象时 MUST 先校验租户/工作区归属与 SHA-256 完整性。

#### Scenario: Raw HTTP response bytes archived with digest on collection run
- **WHEN** 采集作业从 RSS、Telegram 或 WeChat 连接器获取到包含原始 HTTP 响应体（`raw_response`）的批次并提交入库
- **THEN** `flow_collection_runs` MUST 记录完整原始响应字节的 `raw_response_sha256`、`raw_response_bytes` 与 `raw_response_meta`，并在配置 `objectStorePut` 时将原始字节写入 `tenants/{tenant_id}/workspaces/{workspace_id}/raw-runs/{source_id}/{run_id}` 且记录 `raw_response_key`

#### Scenario: Scoped raw response download verifies tenant ownership and SHA-256 integrity
- **WHEN** 调用方请求下载指定采集 run 的原始 HTTP 响应归档
- **THEN** 引擎 MUST 先校验该 run 属于当前租户与工作区且 `raw_response_key` 位于当前租户/工作区前缀内；跨租户或未归档请求 MUST 返回 `404` 且不触达对象存储；若对象存储返回字节的 SHA-256 与 `raw_response_sha256` 不一致，MUST 返回 `502`
