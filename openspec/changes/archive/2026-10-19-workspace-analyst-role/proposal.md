# Proposal: 补齐工作空间 `analyst` 角色枚举与权限映射（PRD §10.1）

## Why
PRD §10.1 规定工作空间角色体系包含 `owner`、`admin`、`analyst`（及 `editor`）与 `viewer`。此前 `backend/internal/flow/scope.go`、`orgs.go` 与 `p7.go` 仅接受 `admin | editor | viewer`（`prd-audit-9.md` 第 129 项标记为 `D`），导致邀请成员或修改成员角色时传入 `analyst` 会被拒绝，且 `ScopesForRole("analyst")` 仅返回只读权限。

## What Changes
1. **权限映射（`backend/internal/flow/scope.go`）**：
   - 在 `ScopesForRole(role string)` 中支持 `"owner"`、`"admin"`、`"editor"` 与 `"analyst"`，赋予 `analyst` 与 `editor` 对齐的采集、监控、计划、报告、研究及 MCP 工具使用权限，同时在工作空间管理接口（`CreateInvite`、`SetMemberRole`、`RemoveMember`、`ResolveReservation`、`ResolveMCPCall`）中保持仅 `admin` / `owner` 可执行管理操作。
2. **组织邀请与成员角色变更校验（`backend/internal/flow/orgs.go`、`backend/internal/flow/p7.go`）**：
   - `CreateInvite` 允许邀请 `"admin" | "editor" | "analyst" | "viewer"` 角色。
   - `SetMemberRole` 允许将工作空间成员角色设置为 `"analyst"`（租户级角色映射为 `"member"`，工作空间级角色持久化为 `"analyst"`），并支持 `owner` 作用域执行成员管理。
3. **单元测试与文档同步（`backend/internal/flow/p7_test.go`、`docs/implementation-status.md`）**：
   - 在 `TestP7MembersRoleEditing` 中补充 `analyst` 角色邀请、角色切换、`ScopesForRole("analyst")` 写权限校验与管理操作拒绝测试。
   - 更新 `docs/implementation-status.md`。

## Capabilities

### New Capabilities
- `flow-workspace-roles`: 工作空间 `owner / admin / analyst / editor / viewer` 角色枚举、邀请与权限 Scope 映射（PRD §10.1）。

### Modified Capabilities

## Impact
- 仅影响 `backend/internal/flow/scope.go`、`backend/internal/flow/orgs.go`、`backend/internal/flow/p7.go`、`backend/internal/model/models_tenancy.go`、`backend/internal/flow/p7_test.go` 及 `docs/implementation-status.md`。
