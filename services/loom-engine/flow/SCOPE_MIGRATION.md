# DT018：Loom 显式范围切换

本变更不修改上游迁移 0006；0019 收紧 27 张具有 `tenant_id` 的表。
普通 `pgDb` 的每条语句进入事务并设置 `SET LOCAL app.tenant_id`，没有有效范围时在取得连接前拒绝。
建池时及每次事务准入核对 current/session user、超级用户/BYPASS/建角色等权限、数据库/表所有权及特权角色成员资格；
错误配置 migration/system DSN 不能伪装普通池。system scanner 建池也核对专用角色及无业务表/列 CRUD。
原始应用数据库连接本身也受 FORCE RLS 约束：空范围读不到租户行、不能插入租户行。
`pgliteDb` 仍是隔离 fixture/admin 适配器，不能当作真实运行角色隔离的证据。

## 已盘点调用路径

| 入口 | 原隐式系统行为 | 显式替代 |
|---|---|---|
| `server.ts` | 验证 JWT 后 `withTenant` | 保留，不持系统连接 |
| `main.ts` collection / `Engine.claim` | 全租户队列、租约、fencing | worker 的受限扫描返回租户，每租户一次原 claim/execute |
| scheduler / `schedulerTick` | 全租户到期窗口、授权复核、预算预留 | 每租户原短事务及回调；不把网络包进扫描事务 |
| outbox / `dispatchOutbox` | 全租户事件顺序投递 | 每租户 sequence 顺序，仍至少一次；平台按 event_id 去重 |
| reconcile / closeBudgets | 未知提交结果对账、预算结算 | 每租户保留原幂等账本引用与状态机 |
| media-retry / asr / ocr / derived | 全租户附件及衍生任务 | enrich（或默认 worker）每租户处理原任务 |
| briefs / `Reports.briefTick` | 全租户定时简报 | research（或默认 scheduler）每租户处理；异步 HTTP 研究继承已验租户 ALS |
| migration | 主进程用 runtime 自动 DDL | 独立 `tools/migrate.ts`，专用迁移凭据 |
| legacy-import | 无范围源读取、目标迁移/写入 | 显式离线源凭据；目标预先迁移，CLI 指定单租户后普通连接写入 |
| media-keys | 全租户迁移计划 | 必须指定 `FLOW_OPERATOR_TENANT_ID`，原乐观比对及对象复制核验保留 |
| metrics | JWT 前可选口令的全库聚合 | main 私有后台逐租户聚合快照；HTTP 必须既有 METRICS_TOKEN，仅读快照 |

## 角色与安装

Bootstrap 预建 `flow_intel_system`（LOGIN、NOINHERIT、无 BYPASSRLS）和
`flow_intel_guard`（NOLOGIN、NOINHERIT、无 BYPASSRLS）。只有 migration 角色可 SET ROLE guard，
不继承其权限。应用角色、system 角色均不得成为 guard/migration 成员。

`install-system-role.sql` 必须在单个迁移事务中执行。guard 仅有 27 表 `tenant_id` 列 SELECT
和仅适用于 guard 的 SELECT policy；函数 `public.flow_dispatch_tenants(text,integer)` 使用固定
`search_path=pg_catalog`、全限定表名、固定 SQL、最多 100 个 ID，PUBLIC 执行权限撤销。
system 仅有函数 EXECUTE，不得直接读正文、列 tenant_id 或写任何业务表。应用角色不能执行扫描函数。
这不是租户激活授权；Loom 不跨库查询平台租户状态，原平台 grant 复核仍必须接通。

## 维护窗口切换（不得新旧空范围 worker 混跑）

1. 停止旧 Loom API/worker/scheduler，保留可验证备份与原版本回退材料。
2. Bootstrap/升级脚本创建上述角色并设置独立秘密。系统连接不得进入 HTTP 请求数据库对象。
3. 单独运行 `LOOM_MIGRATION_DATABASE_URL=... node --experimental-strip-types flow/tools/migrate.ts`；
   它执行有序迁移，再安装函数。也可用现有部署迁移作业显式调用同一 adapter/SQL。
4. 保留 `LOOM_ROLE=api` 的 HTTP+collection/outbox，保留 `all` 的原循环组合；新增 `worker`
   仅作为可选独立后台部署。scheduler 保留，`LOOM_SPLIT_ROLES=true` 时另起 enrich/research。
   所有运行角色拒绝自动迁移及 migration URL，避免迁移 owner 进入请求运行链。
5. 后台角色配置普通 `DATABASE_URL` 加独立 `LOOM_SYSTEM_DATABASE_URL`。system 对象只暴露固定 scan，
   不暴露通用 query/tx，且拒绝从已有租户请求上下文调用。每个循环独立分页 cursor；失败租户不会阻塞后续租户。
6. 运行实际 PostgreSQL 缺范围/系统权限/连接复用负例及旧业务回归，再恢复任务。

按 SECURITY_TENANCY §4/5，隔离的是数据库身份、连接池和显式操作能力，不强制新增 OS 进程边界。
同一进程的 scanner 是 main 闭包私有对象，只能执行固定租户 ID 查询；Engine/HTTP handler
仅拿到普通 Db。HTTP/Agent 参数、角色或租户上下文不能选择系统池；scan 明确拒绝已有租户上下文。
仓库实际旧部署调用方是 `docker-compose.flow.yml`（api/scheduler/enrich/research）及农业 compose，
需要增加独立系统 DSN 和迁移作业，但不得因此丢失原 api/all 后台任务。

## 事务与回退边界

原业务写入、cursor、outbox 的同一事务没有拆开。扫描只返回租户 ID，不 claim 业务行；原 tenant
事务仍执行 SKIP LOCKED、fencing、唯一键和预算幂等引用。每租户内部事件顺序保留，不声称跨租户全局投递顺序。
公平性改为有界按租户轮转，不再依赖跨租户业务表扫描；实际租户内并发上限仍在原 claim 中检查。

全局 metrics 复用既有运维 `METRICS_TOKEN`：未配置 503、缺失/错误 bearer 401，普通用户/Agent JWT 不被当作运维口令。
HTTP handler 不持 scanner，也不从 URL/请求头读取租户或系统角色选择器；只有 main 后台周期调用扫描。
快照整个采样完成后原子替换，失败清空、超过 60 秒不可用返回 503；不以假零或无限旧值报告健康。
响应包含快照年龄且 no-store。数量求和、年龄取最大、进程 uptime 不重复相加；无 tenant ID/正文标签。

若切换失败，停止农业上线并保持 fail-closed。不得回退到空 scope 全权限或关闭 RLS；
可以回到具有显式范围适配的已验证代码/备份，恢复前核对 outbox 与未决预算，禁止直接改余额。
原版空范围程序不能与严格策略直接混用，因此原版二进制回退需要维护窗口和受审恢复方案，不能在线无条件 rollback。

## 验证口径

`tenant-dispatch.test.ts` 验证上下文/分页/角色配置；`scope-postgres.test.ts` 由
`scripts/agriculture/native-loom-test.py` 在独立 SCRAM PostgreSQL、实际非超级用户角色上运行。
fixture feed 不触达外部源。报告只保存固定测试名、状态与源码摘要；私密 DSN/原始日志不进入交付证据。
真实 PostgreSQL 测试另含非空简报生成、附件重试与 outbox（媒体传输是明确 fixture）；
ASR/OCR 空队列 tick 仅证明显式范围下可调用，不能代替真实媒体模型/渠道验收。
