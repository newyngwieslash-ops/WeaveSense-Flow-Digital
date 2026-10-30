# DT018：既有 Flow 回调与窄入口边界

本说明记录本次实际代码边界，不等于农业全部计费/工作区隔离已验收。

- 只有已注册的五个 `POST /api/internal/flow/{events,authorize,budget/reserve,budget/close,summarize}` 延迟到 handler 验证服务签名。任意路径包含 `/internal/` 不再绕过用户身份验证。缺失请求事务上下文不会回退到全库连接。
- handler 从已验证签名取得 tenant，显式 `TenantTx`；body/query 不决定授权租户。
- `token.go` 的 Sign/Verify 和 Loom `flow/auth.ts` 都要求非空 workspace；Loom `flow/main.ts` 的 platformCall/outbox 将实际工作区写入票据。`docs/flow/service-contracts.md` §1 将 tenant/workspace 明确为对象查找范围。因此 authorize/reserve/summarize 的 body workspace、events 的 event workspace 必须与票据一致；没有新增空 workspace 的 tenant-wide 降级。
- **尚未闭合的既有计费边界**：budget/close 的持久 reservation 没有 workspace 字段。当前只能按已签名 tenant + reservation id + schedule_window purpose 查找并结算/释放，不能宣称已有工作区级持久绑定。本次没有凭空信任新 body workspace。后续计费契约/迁移必须将 reservation 与创建时实际 workspace 绑定、处理旧记录并覆盖同租户跨工作区负例。
- 用户租户发现通过 `flow_system.resolve_user_tenants(trusted_user_id)`；只有真正不存在的个人租户可以初始化，已停用租户/撤销成员不会靠 fallback 复活。
- 旧组织创建使用服务端随机 tenant 的独立事务；接受旧邀请先用 `flow_system.resolve_legacy_invite(token_hash,trusted_user_id)` 窄查，再在目标 tenant 事务锁定并复核有效期、消费状态和当前已验证邮件收件人。农业 product 邀请不能由旧入口消费。
- 旧 MCP scoped helper 改用请求事务，避免额外未 scoped 查询和单连接死锁。测试真实安装生产 TenantMiddleware，而非增加连接数掩盖缺陷。

验证：`go test ./internal/flow -count=1 -timeout=180s` 完整本地回归及针对性测试；PostgreSQL 独立必测根为 `TestFlowPostgresCallbackAndLegacyAdmission`，需要隔离 migration/runtime 角色和 `AGRI_TEST_PG_ISOLATED=1`。未配置 DSN 时明确 skip，不能作为真实 PG 通过证据。实际运行结果由验收证据另行记录。

## 后台 worker 的短事务边界

DT018 的 ModelTasks / CommandDispatcher 仅由服务器组合层注入固定 `BackgroundScanner` 能力。PostgreSQL 顶层批次分别使用 `ScanModelTasks` / `ScanCommands`；返回 tenant 只存于未导出的 `tenantID`，请求 JSON 不能填充。每个租户的读取/claim、回执和结算使用独立短事务，真实 Gateway/Engine HTTP、状态查询、完成通知位于这些后台事务之外。缺 scanner 的 PostgreSQL 批次明确拒绝，不回退全库读取。SQLite 保留原开发路径。

`TestFlowPostgresWorkerShortTransactions` 在真实受限 PostgreSQL 角色、单条 app 连接下，通过本地 HTTP 协议测试端在请求处理中反向查询同一连接，验证连接可用、tenant setting 已清除、无已分配事务；还验证两个租户、未知结果/重试回执的持久化、真实账本和完成通知。HTTP peer 是明确 fixture，非真实模型或 Loom 商业服务验收。

**范围限制**：既有同步 REST `EnqueueAndDispatch` / `ModelTasks.Wait` 可能收到外层请求事务；此次没有偷偷改变同步响应或原子性契约，不宣称所有旧同步路径的网络调用都已脱离事务。该遗留路径需要单独迁移到持久异步请求闭环。
