# Tasks: 受管 MCP 工具列表复合键缓存与失效机制（PRD §9.3）

- [x] 1. 在 `backend/internal/flow/mcp.go` 中实现 `MCPToolCacheKey`、`MCPToolListCache`、`AgentToolsForPolicy`、`DiscoverCached` 与审批/撤销/漂移联动失效
- [x] 2. 在 `backend/internal/flow/mcp_test.go` 中新增 `TestMCPToolCacheCompositeKeyAndInvalidation` 覆盖复合键命中、跨策略版本隔离、Schema 漂移与撤销失效
- [x] 3. 更新 `docs/implementation-status.md` 并运行 `go -C backend test ./internal/flow/...` 验证通过
