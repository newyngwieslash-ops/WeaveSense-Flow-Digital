# Flow / Loom 独立数据库身份部署（DT018）

本文件描述配置契约，不是已执行生产部署的记录。真实 PostgreSQL 测试、容器构建与生产迁移分别记录，不互相替代。

## 身份与后台执行

- `flow_platform_app` / `flow_intel_app`：普通业务连接；没有可信租户范围时拒绝，不以空范围代表 system。
- `*_migrate`：仅一次性迁移进程。不得放入 API 的普通连接池、HTTP/Agent 参数或运行环境文件。
- `*_system`：独立后台池，只能调用经审计的固定、有限分页租户发现函数；不是业务 CRUD 或 RLS bypass 身份。
- `*_guard`：NOLOGIN / NOBYPASSRLS 函数所有者。迁移身份只有显式 SET 权限而不继承；app/system 不具有该成员关系。

允许同一进程内的固定后台调度器持有私有 system 池，但其能力不交给 handlers、tools 或可由请求选择的数据库上下文。发现租户后，实际业务使用该租户的 app 短事务。独立 worker 部署是运维选项，不是原规格额外要求。不得借进程拆分删除已有影视、数学或知识后台任务。

## 新隔离环境

农业 Compose 使用 `deploy/agriculture/env.example`，独立迁移服务先成功，运行服务才启动。Loom 迁移入口统一为 `flow/tools/migrate.ts`，使用 `LOOM_MIGRATION_DATABASE_URL`；普通 API 只持有 app DSN 与仅供后台的 `LOOM_SYSTEM_DATABASE_URL`。

原 Flow Compose 同样使用独立迁移服务，但现在需要两份私有配置：

1. `.env.flow`：从 `.env.flow.example` 创建，仅供 Compose 插值，含 bootstrap / migration 密码。**不能整个传入运行容器**。
2. `.env.flow.runtime`：从 `.env.flow.runtime.example` 创建，只填写所需运行设置。内部签名密钥须在需要互通的服务一致；不要复制 owner、superuser 或 migration DSN。两份文件均应限制为 0600，不提交 Git。

角色和数据库名固定为迁移契约中的 `flow_platform` / `flow_intel`，不再允许模板把任意 owner 当作 runtime。Loom 的 api、scheduler、enrich、research 组合及各自业务职责保留，不要求新增 OS 进程角色。不要复用旧 `change-me` 密码；独立凭据由授权运维人员配置，本文不创建真实凭据。

## 已存在卷

`init-databases.sh` 是**首次初始化脚本，不能在既有卷上直接重跑**。旧版本把 runtime 作为对象 owner；仅更改密码或 Compose 用户名不能完成升级。

升级须先在隔离恢复副本核验实际角色、对象所有权、RLS 策略和历史迁移账本；创建独立 app/migrate/system/guard 身份、受控转移旧对象与迁移所有权后，使用正式迁移入口执行并核验真实逐角色负例，再切换服务配置。不得用 `DROP OWNED`、关闭 RLS、授予 BYPASSRLS、复用超级用户或重置数据来让启动通过。未提供旧实例时，具体生产实例的迁移和恢复演练仍待授权及实例证据，不宣称已经执行。

## 运维指标

Loom 全局 `/metrics` 复用既有 `METRICS_TOKEN`，未配置时关闭；请求缺失/错误 token 拒绝。响应只读取后台已完成、未过期的运维快照，不从请求触发全租户查询或获取 system 池。失败、尚未完成或过期不伪装为零值成功。

启用 Prometheus 时，分别准备未跟踪的 `deploy/flow/monitoring/loom.token` 与 `platform.token`，与服务中的对应 token 一致；这些是运维凭据，不是浏览器会话或租户授权。
