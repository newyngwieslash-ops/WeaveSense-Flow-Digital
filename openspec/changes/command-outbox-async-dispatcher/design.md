# Design: 平台侧手动命令 Outbox 与异步投递器（PRD §11.3）

## Context
PRD §11.3 规定手动采集与研究命令在平台侧需满足：
1. 平台数据库同事务写入操作投影（`FlowOperationProjection`）、预算预留（`FlowBudgetReservation`，如适用）与命令 Outbox（`FlowCommandOutbox`）；
2. 投递器（`CommandDispatcher`）将 Outbox 中的命令携带 `X-Idempotency-Key` 派发至 Loom Engine；
3. Loom Engine 按 `(tenant_id, idempotency_key)` 幂等去重并创建 Job；
4. 遇到未知状态或超限失败时转入对账状态（`reconciliation_required`），不盲目无限重发。

## Technical Decisions
1. **表结构与多租户隔离（`flow_command_outbox`）**：
   - 主键 `ID`（`cmd_...`），唯一索引 `ux_flow_cmd_outbox_idem(tenant_id, idempotency_key)`（当调用方未传幂等键时，自动生成唯一键 `auto:<cmd_id>`，同时保留原始请求头语义以便 Engine 校验）。
   - 将 `flow_command_outbox` 加入 `flowRLSTables`，通过 Migration `v67` 自动建表并开启 PostgreSQL `FORCE ROW LEVEL SECURITY`。
2. **同事务入队（`CommandDispatcher.Enqueue`）**：
   - 计算 `RequestHash = sha256(command + "\x00" + workspace_id + "\x00" + method + "\x00" + path + "\x00" + payload)`；
   - 在 `db.Transaction` 内查询是否存在相同 `(tenant_id, idempotency_key)`：
     - 若存在且 `RequestHash` 不一致，返回 `ErrCommandConflict`（对应 HTTP 409）；
     - 若存在且 `RequestHash` 一致，直接返回已有记录与 `replayed = true`；
   - 若指定 `ReserveMicro > 0`，在同一数据库上下文创建/复用 `FlowBudgetReservation`；
   - 插入初始 `FlowOperationProjection`（`Status: "queued"`）与 `FlowCommandOutbox`（`Status: "pending"`）。
3. **幂等派发与后台重试（`DispatchOne` / `DispatchDue` / `RunCommandDispatcher`）**：
   - 默认请求路径在 `Enqueue` 成功后立即调用 `DispatchOne` 执行首轮投递，确保 Engine 在线时延迟为零且完全兼容现有同步断言测试；
   - 支持 `X-Flow-Outbox-Mode: async` 请求头，入队后立即返回 `202 Accepted`（含 `operation_id` 与 `outbox_id`），由后台 `RunCommandDispatcher` 异步派发；
   - 当 Engine 返回 5xx 或网络错误时，Outbox 记录进入 `retry_wait` 并按指数退避计算 `next_attempt_at`；当重试次数达到 `MaxAttempts` 时标记为 `reconciliation_required`。
