# Flow 缺省拒绝迁移（DT018，分阶段证据）

## 已实现的数据库/provider 切片

平台迁移 84 在旧迁移全部完成后切换已有 Flow/教育租户 RLS 表。历史迁移及其 checksum
不重写；新普通应用连接空 `app.tenant_ids` 不再获得 system 权限。迁移维护权限显式
授予 `flow_platform_migrate`，与空 GUC 无关；普通 app 不得成为 migrate/guard/system 成员。
本切片不声称给原先未启用 RLS 的账号/租户元数据表补齐了全部对象权限。

`flow_platform_guard` 是预建 NOLOGIN/NOINHERIT、无 BYPASSRLS 的固定函数 owner；
`flow_platform_system` 是另一个受限 LOGIN 后台身份，不能作为请求数据库。后台扫描
固定接口及旧任务调用方迁移见下文；不能因为 provider 测试通过便标 DT018 完成。

## 受限后台发现与运行时校验（迁移 85）

`BackgroundScanner` 私有持有独立 system 连接，只公开固定 `Next(ScanKind)`；没有可供 HTTP、
Agent 参数选择连接或执行 SQL 的接口。`flow_system.pending_tenants` 仅返回最多 100 个 tenant ID，
closed kind 列表覆盖 billing、notification、model、command、external wait、math jobs/ledger。
每类任务独立 keyset cursor，避免只处理前 100 个租户。函数 owner 是无登录 guard，system 没有
业务表/列 CRUD、TRUNCATE 或角色继承权限，app 也不能调用全域 scanner。

启动校验拒绝 `current_user != session_user`、危险角色属性、迁移/admission/guard 成员关系，
以及 app 拥有数据库/业务表的配置。scanner 额外扫描全部 public relations 的表和列权限，
拒绝误授予其他业务表的权限，不仅检查 provider 表。新部署和已有卷都不能靠角色名称冒充安全配置。

main 保留同一进程原运行链，但只向后台对象注入私有 scanner；HTTP/工具仍使用 app 池。
ModelTasks、CommandDispatcher、Notifier 和 Waits 按租户短事务领取/读写，provider/Engine/邮件/
continuation 外部调用不持有这些数据库事务，完成后另开同租户事务记录 receipt。未知结果、
重试和结算继续原状态机。真实单连接 PostgreSQL 回调探针验证外部调用期间可复用连接且 scope 已清除。

**数学既有边界**：`edumath.SyncOnce`/`ReconcileAll` 只将跨租户发现替换成固定 scanner；
`Sync`、`HandleMathEvent` 以及既有 StartJob/CancelJob 调用的部分 HTTP 仍在原租户事务内。
这不是新增长事务，但尚未完成这部分调度/网络拆分，不能声称全部数学 I/O 已移出事务。

## 数学旧入口保持可用但不恢复空 scope

`resolve_edu_user_tenants` 只为可信平台会话或已验证 artifact 签名中的 user ID 返回当前有效
教育角色所处的 active 租户；学生无需假造平台组织成员。已撤销/过期角色、disabled 用户或学校
不会获得 scope。普通数学 API 必须使用该 scope 的请求事务，遗漏 middleware 默认拒绝。

签名文件仍使用既有 `edu-artifact` HMAC、artifact/user/expiry 绑定和最多 5 分钟链接。
只有精确 GET `/api/edu/files/:id` 进入专用签名流程；校验后以签名绑定用户的 scope 开短事务，
按精确 artifact ID 查询并执行 `canFetch` 当前授权复核，结束数据库事务后再下载字节。
签名失效、角色撤销等拒绝不调用 artifact HTTP；不声称已经开始的传输可被追溯取消。

班级邀请和名册 claim 使用 hash+active actor/学校/班级绑定的固定 resolver，仅返回 tenant ID，
随后 scoped transaction 锁定原记录，重新检查状态、身份、次数/expiry 并执行原业务操作。
班级 code 已达到次数上限时，原 active 已入班学生的幂等重复请求仍保留，但新学生不能占用。
service math event 继续先校验原签名和租户绑定，再执行 `TenantTx`，不是浏览器身份入口。

## 同事务 provider 计量

PostgreSQL 不再调用 `withSystemContext` 清空租户 GUC。`provider_exists(text)` 只返回
是否配置账户的布尔值，并要求非空租户上下文；app 没有 provider 表直接读写权限。
安全定义者触发器绑定真实 reservation INSERT 或终态 UPDATE，在**同一数据库事务**
更新 provider 月度 reserved/consumed；不另建账本、不调用另一连接池。

- 必须是匹配 GUC 的 tenant；预留金额、provider、account、parent、创建时间、价格版本冻结。
- INSERT 只接受正数预留/`reserved` 状态，锁定 provider 并按旧上限算法检查。
- 仅 `reserved`/`reconciliation_required` 向 `settled`/`released` 转换一次；终态不能重开。
- 保留旧算法：结算使用累计 consumed（含子任务），负数/超额由既有 Go 层截断；provider
  reserved 使用旧下界零算法。root 和 child 各自带 provider 的既有计量语义不在此改写。
- 未配置 provider 时保留原“不启用该层”的空 tag；SQL/权限错误不再伪装成未配置。
- provider 暂停/额度不足使整个业务事务失败，租户钱包和 provider 占用一起回滚。
- 普通 app 不能删除 reservation；维护角色删除仅适用于经授权的维护/隔离 fixture 清理，
  不是退款接口。用户退款仍必须经过既有状态机。

SQLite 保留原计算实现用于旧逻辑回归，不作为 PostgreSQL 权限证据。

## 狭窄身份解析

`flow_system.resolve_user_tenants(text)` 只解析 active 用户/成员/租户；首次个人租户仅在
真的不存在时返回确定的 `tn_u_` 身份，不复活 disabled/revoked 记录。
`resolve_legacy_invite(text,text)` 只解析有效未消费旧产品邀请，并绑定已验证平台邮箱；
真正接受操作还须在目标租户事务再次锁定/校验。农业产品仍使用既有 v82 resolver。
两个函数仅供可信会话调用方使用，不把浏览器给定 user ID 当权限。

## 部署与回退边界

先预建 guard/system 最小角色，准备与新调用方兼容的服务二进制及私有配置；维护窗口
停止旧后台实例后备份数据库/角色清单，再运行 84/85、验证 runtime readiness、启动新实例。
不能混跑依赖空 scope 的旧后台，否则会遗漏任务或错误处理结算。
若兼容回归未完成，停止农业上线并继续修复，不用空 scope=system 或关闭 RLS 临时兜底。
回退须在停写/停后台下恢复经过验证的数据库与匹配二进制备份；不向正在使用新财务
触发器的数据库直接降版本，不删账本、清余额或重置 schema_migrations。

## 实际验证

`native-flow-scope-test.py` 使用已验 SHA 的工作区 PostgreSQL/PostGIS 包、全新私有集群、
随机 fixture 密码及 finally 停服，执行生产 init/全迁移/重放/verify。严格 reducer 要求
`TestFlowPostgresStrictScopeAndProviderAtomicity` 和五子项全部 pass，任何 skip/missing 失败。
2026-10-30 本地 run `58b093c394494cc0b2e7b63d514d6288` 已真实通过；不是远程 CI、生产
部署或整个 DT018 验收。原首次 run 因旧缓存目录缺模块失败，保留失败记录，没有改变
断言或在线拉取未锁定依赖来掩盖错误。

后续 native runner 可选 `--bootstrap agriculture|flow`，后者真实调用旧影视 Flow 的独立角色
初始化脚本，仍执行相同平台迁移/provider/callback/scanner/worker 门禁。fixture bootstrap
管理凭据只注入专门的权限漂移负例子进程，用于构造后恢复错误授权/ownership；不会传入产品。
报告分别保留开始/结束源码 SHA，运行期间源码变化必须重新验证，不能用结束时哈希冒充实际执行版本。
本文件列出的实现边界不替代最终整包验收状态，实际结果以对应归档报告为准。
