# Design: 补齐工作空间 `analyst` 角色枚举与权限映射（PRD §10.1）

## Context
PRD §10.1 要求支持 `owner / admin / analyst / viewer` 角色体系，同时保持现有 `editor` 角色的向后兼容。

## Technical Decisions
1. **`ScopesForRole` 权限集（`backend/internal/flow/scope.go`）**：
   - `owner`、`admin`、`editor`、`analyst` 均拥有完整的业务读写与分析 Scope（`intel.sources.read/write`、`intel.documents.read`、`intel.operations.read`、`intel.monitors.write`、`intel.schedules.write`、`intel.collect`、`intel.reports.write`、`intel.research`、`intel.mcp.use`）。
   - `viewer` 仅拥有只读 Scope（`intel.sources.read`、`intel.documents.read`、`intel.operations.read`）。
   - 组织与工作空间管理操作（邀请、改角色、移除成员、对账裁决）仍严格要求 `scope.Role == "admin" || scope.Role == "owner"`。
2. **邀请与角色持久化（`backend/internal/flow/orgs.go`、`backend/internal/flow/p7.go`）**：
   - `CreateInvite` 校验 `role` 属于 `admin | editor | analyst | viewer`。
   - `SetMemberRole` 支持 `analyst`（对应 `flow_memberships.role = "member"`、`flow_workspace_memberships.role = "analyst"`），`ListMembers` 直接投影工作空间角色 `"analyst"`。
