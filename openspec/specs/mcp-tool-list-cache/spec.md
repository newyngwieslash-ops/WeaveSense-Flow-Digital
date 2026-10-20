# mcp-tool-list-cache Specification

## Purpose
TBD - created by archiving change mcp-tool-list-versioned-cache. Update Purpose after archive.

## Requirements

### Requirement: 受管 MCP 工具列表五元组复合键缓存与联动失效
MCP 网关 SHALL 使用 `(tenant_id, connection_id, secret_ref, schema_hash, policy_version)` 复合键缓存已批准的 Agent 工具定义与工具发现列表，并在连接重新审批、撤销或检测到上游 Schema 漂移时立即使该连接的缓存失效。

#### Scenario: 复合键缓存命中、版本隔离与漂移/撤销失效
- **WHEN** 同一租户与连接在相同凭据引用、Schema 哈希与权限版本下重复获取工具列表，随后发生权限版本变更、Schema 漂移或连接撤销
- **THEN** 相同复合键命中缓存，不同 `policy_version` 隔离为独立缓存键，且在 Schema 漂移或连接撤销后立即清空该连接的缓存条目。
