# Proposal: 平台侧手动命令 Outbox 与异步投递器（PRD §11.3）

## Why
根据 PRD §11.3 与 `docs/flow/service-contracts.md` §4 的契约要求，手动采集（`collection.request`）与研究（`research.request`）命令在平台侧应通过同事务建立操作投影（`FlowOperationProjection`）、预算预留（可选 `FlowBudgetReservation`）与命令 Outbox 记录（`FlowCommandOutbox`），再由投递器（`CommandDispatcher`）幂等派发至 Loom Engine（通过 `X-Idempotency-Key` 在 Engine 侧去重）。此前 `backend/internal/flow/routes.go` 与 `backend/internal/flow/tools.go` 仅直接同步发起 HTTP 请求，缺少平台数据库侧的 `flow_command_outbox` 持久化表与后台重试投递器（`prd-audit-9.md` 第 158 项标记为 `D`）。

## What Changes
1. **新增 `FlowCommandOutbox` 模型、数据库迁移与 RLS 策略**：
   - 在 `backend/internal/model/models_tenancy.go` 新增 `FlowCommandOutbox`（表名 `flow_command_outbox`），包含 `tenant_id`、`workspace_id`、`operation_id`、`command`、`method`、`path`、`idempotency_key`、`request_hash`、`payload`、`reservation_id`、`status`（`pending | dispatching | dispatched | retry_wait | failed | reconciliation_required`）、重试计数与租约时间。
   - 在 `backend/internal/database/flow_rls.go` 的 `flowRLSTables` 注册 `flow_command_outbox`，并在 `backend/internal/database/migrations.go` 新增迁移 `v67`（`flow_command_outbox_v67`）自动建表并启用 PostgreSQL `FORCE ROW LEVEL SECURITY`。
2. **实现平台侧命令 Outbox 事务入队与投递器（`backend/internal/flow/command_outbox.go`）**：
   - `CommandDispatcher.Enqueue`：在单个数据库事务内完成 `(tenant_id, idempotency_key)` 幂等冲突检查（同键不同参返回 `ErrCommandConflict` / HTTP 409）、可选预算预留（`Billing.Reserve`）、初始操作投影（`FlowOperationProjection`，状态 `queued`）及 `FlowCommandOutbox`（状态 `pending`）写入。
   - `CommandDispatcher.DispatchOne` / `DispatchDue` / `RunCommandDispatcher`：带租约领取待派发命令，携带 `X-Idempotency-Key` 调用 Loom Engine；成功时更新 Outbox 为 `dispatched` 并同步投影状态；4xx 确定性失败时释放预留并标记 `failed`；网络异常或 5xx 时指数退避进入 `retry_wait`，超过最大尝试次数或租约超时未知结果时转入 `reconciliation_required`（遵循 PRD §11.3 禁止盲重发原则）。
3. **接入 REST 路由、Agent 工具与服务启动入口**：
   - `backend/internal/flow/routes.go` 与 `routes_contract.go`：`POST /api/intelligence/collection-runs` 与 `POST /api/intelligence/research-runs` 接入 `CommandDispatcher`，支持默认同步派发透传与 `X-Flow-Outbox-Mode: async` 异步入队返回 `202 Accepted`。
   - `backend/internal/flow/tools.go`：`intel_collect_once` 与 `intel_start_research` 经过 `CommandDispatcher` 同事务记录 Outbox 与操作投影后派发至 Engine。
   - `backend/cmd/server/main.go`：当 `flowCfg.Enabled()` 时启动后台 `go flow.RunCommandDispatcher(...)`。
4. **测试与文档更新**：
   - 新增 `backend/internal/flow/command_outbox_test.go` 覆盖同事务创建 Outbox + 投影 + 预留、同键重放与同键不同参 409、Engine 故障退避重试与后台恢复投递、超限转入 `reconciliation_required` 及 REST `X-Flow-Outbox-Mode: async` 行为。
   - 更新 `docs/flow/service-contracts.md` 与 `docs/implementation-status.md`。

## Capabilities

### New Capabilities
- `flow-command-outbox`: 平台侧手动命令 Outbox 事务入队、幂等冲突检测与异步重试投递器（PRD §11.3）。

### Modified Capabilities

## Impact
- 仅影响 `backend/internal/model/models_tenancy.go`、`backend/internal/database/`、`backend/internal/flow/`、`backend/cmd/server/main.go` 及 `docs/` 文档，保持所有既有 API 与工具测试 100% 兼容。
