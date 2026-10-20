# Proposal: 受管 MCP 工具列表复合键缓存与失效机制（PRD §9.3）

## Why
根据 PRD §9.3 要求，受管 MCP 网关在缓存连接工具列表与 Agent 工具定义时，必须携带 `(tenant_id, connection_id, secret_ref, schema_hash, policy_version)` 复合作用域键，并在连接审批、撤销或 Schema 漂移时立即失效，严禁跨租户、跨凭据或跨权限版本共享工具缓存。此前 `backend/internal/flow/mcp.go` 每次请求均直接反序列化或重连而未建立带复合键的缓存层，在 PRD 审计中被记为 `D`（实现方式偏差）。

## What Changes
- 在 `backend/internal/flow/mcp.go` 中新增 `MCPToolCacheKey`（包含 `TenantID`、`ConnectionID`、`SecretRef`、`SchemaHash`、`PolicyVersion` 五元组）与并发安全的 `MCPToolListCache`。
- 在 `AgentTools` / `AgentToolsForPolicy` 与 `Discover` / `DiscoverCached` 中接入复合键缓存，并对缓存读写结果执行深拷贝，防止调用方原地修改污染缓存。
- 在 `Approve`、`Revoke` 以及 `Execute` 检测到 Schema 漂移（`needs_reapproval`）时递增连接 `version` 并调用 `InvalidateCache(tenantID, connectionID)` 立即驱逐对应连接缓存。
- 在 `backend/internal/flow/mcp_test.go` 中新增 `TestMCPToolCacheCompositeKeyAndInvalidation` 单元测试，并同步更新 `docs/implementation-status.md`。

## Impact
- Affected specs: `mcp-tool-list-cache`
- Affected code:
  - `backend/internal/flow/mcp.go`
  - `backend/internal/flow/mcp_test.go`
  - `docs/implementation-status.md`
