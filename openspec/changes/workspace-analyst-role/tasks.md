# Tasks: workspace-analyst-role

- [x] 1. 在 `backend/internal/flow/scope.go` 与 `backend/internal/model/models_tenancy.go` 扩展支持 `analyst`（及 `owner`）角色枚举与 Scope 映射
- [x] 2. 在 `backend/internal/flow/orgs.go`（`CreateInvite`）与 `backend/internal/flow/p7.go`（`SetMemberRole`）支持 `analyst` 角色校验与持久化
- [x] 3. 在 `backend/internal/flow/p7_test.go` 补充 `analyst` 角色邀请、切换与权限校验单测，并更新 `docs/implementation-status.md`
