# Design: 受管 MCP 工具列表复合键缓存与失效机制（PRD §9.3）

## Context
PRD §9.3 要求 MCP 工具列表缓存必须绑定租户、连接、凭据引用、Schema 哈希与权限版本，防止多租户或权限变更后命中过期工具定义，同时不能削弱 A14（上游工具 Schema 变化未重新审批前阻断执行）的安全保证。

## Technical Decisions
1. **五元组复合缓存键（`MCPToolCacheKey`）**：
   - 键结构严格包含 `(TenantID, ConnectionID, SecretRef, SchemaHash, PolicyVersion)`，其中 `PolicyVersion` 融合连接自身版本号 `c.Version`（每次审批、撤销或 Schema 漂移均递增）与运行期策略版本 `PolicyVersion`。任意维度变化均生成新键，绝不跨作用域复用。
2. **保留 `Execute` 实时 Schema 校验并触发联动失效**：
   - `Execute` 仍对上游 MCP 会话校验工具实时 Schema Hash，一旦发现漂移立即将连接置为 `needs_reapproval`、递增 `version` 并调用 `InvalidateCache(c.TenantID, c.ID)` 清空该连接的全部缓存条目，确保 A14 零退化。
