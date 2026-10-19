# Tasks: command-outbox-async-dispatcher

- [x] 1. 在 `backend/internal/model/models_tenancy.go` 新增 `FlowCommandOutbox` 模型，并在 `backend/internal/database/flow_rls.go` 与 `migrations.go`（v67）注册建表及 RLS
- [x] 2. 新增 `backend/internal/flow/command_outbox.go`，实现 `CommandDispatcher`（同事务入队 Outbox + Projection + Reservation、幂等冲突检测、单条/批量派发、指数退避重试与 `reconciliation_required` 保护）
- [x] 3. 在 `backend/internal/flow/routes.go`、`routes_contract.go`、`tools.go` 与 `backend/cmd/server/main.go` 接入 `CommandDispatcher` 与后台 Worker
- [x] 4. 新增 `backend/internal/flow/command_outbox_test.go` 单元测试并更新 `docs/flow/service-contracts.md` 与 `docs/implementation-status.md`
