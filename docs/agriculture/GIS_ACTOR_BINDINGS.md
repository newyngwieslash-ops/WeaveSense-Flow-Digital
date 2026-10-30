# GIS 可信操作人绑定与原子审计（DT013）

## 边界

平台用户 ID 与旧 `ProjectUser.user_code` 不是同一标识。GIS 使用显式、服务端维护的两级绑定：

- `agriculture_project_bindings`：租户/工作区绑定唯一旧项目；同一项目不得归属多个租户或工作区。
- `agriculture_actor_bindings`：租户/工作区/稳定平台用户 ID 绑定该项目内的旧成员主键。平台 ID 保留最多 128 字符，不截断为旧系统 50 字符编码；不按姓名、编码、默认成员自动认领。
- `agriculture_actor_audit`：只追加的稳定身份审计，保留签名请求、操作、资源、授权版本、旧成员和角色快照。旧历史表不回填、不改写历史操作人。

普通 `flow_geo_app` 只能读取绑定、读取和追加审计；没有绑定写入或审计修改/删除/清空权限。三张新表均 FORCE RLS，空租户范围不返回记录。此项不代表已为所有旧 GIS 表完成 W02 RLS 改造。

## 迁移与配置

开发 PostgreSQL 初始化增加无登录、无 BYPASSRLS 的 `flow_geo_actor_guard`，迁移角色获得 SET 权限但不继承其权限，普通运行角色无成员关系。先以 `flow_geo_migrate` 在 `flow_geo` 建立实际旧项目/成员表，再执行：

```text
services/agri-gis/backend/scripts/migrations/20261029_agriculture_actor_bindings.sql
```

已有部署必须由受控数据库管理员补齐初始化角色；本迁移不创建超级用户或自动提升运行账号。没有角色、绑定、旧表或安全配置时应拒绝服务，而非猜测默认映射。显式映射仅允许所有者离线预置；当前没有公开创建映射接口，也没有本次生产数据操作。

窄函数 `public.agriculture_lock_actor(text,text,text,integer)` 仅锁定与当前非空事务租户完全匹配的启用绑定、旧成员和项目，返回布尔值。固定 `pg_catalog` 搜索路径、全限定表名、无动态业务 SQL、无范围修改。PUBLIC 无 EXECUTE，运行账号仅 EXECUTE；迁移中临时授予函数所有者 schema CREATE 用于所有权转移，随后撤销。锁保持至业务事务结束，防止撤销绑定或角色变更与提交竞态。

## 会话与操作适配

`resolve_trusted_actor` 只接收 DT012 验证结果并再次查数据库绑定、旧成员和项目状态。查询使用列快照，避免身份重查覆盖尚未 flush 的业务 ORM 字段。锁等待后再次检查有效期；不绑定过期上下文。提交/回滚清除操作人；同一验证对象不能在同一会话再次绑定。长任务不能保留此短时 HTTP 上下文，应另走后续持久任务授权设计。

`AgricultureAsyncSession` 是显式农业会话边界。默认没有写操作适配器：未知模型、原始 SQL、bulk DML 和服务端身份表修改一律拒绝。每个 `AuditAdapter` 必须固定模型、动作、操作类型及独立资源解析器；无 `project_id` 的旧模型必须额外提供关系解析器并验证原持久化关联，不能默认属于当前项目。原始和新项目均应匹配，资源集合必须完整匹配签名集合。

审计在业务 flush 前使用同一个数据库连接/事务写入，旧服务内部 `commit()` 也不能绕过。审计失败、业务约束失败或权限失败都会使该事务回滚；不是业务提交后的补记。审计不复制业务正文、姓名、授权头或密钥。外部文件/网络副作用不因这个会话而自动获得数据库事务语义，未适配路径仍关闭。

旧 `supersede_reverted_plot_operations` 保留原任务范围语义，改成行锁查询后 ORM 逐项状态变更，以便每条变更进入资源/审计验证。不能为了通过测试静默缩小原任务的重做失效范围。

## 验证及未交付部分

`test_agriculture_actors.py` 是真实 SQLite SQL 的组件测试，不是 PostgreSQL 隔离证据。两个 `test_agriculture_actors_postgres.py` 测试要求独立的 geo owner/runtime DSN 及 `AGRI_TEST_PG_ISOLATED=1`，检查 RLS、运行角色/函数 ACL、映射唯一性、128 字符身份、审计原子性、历史不可改、并发撤销锁及等待过期。

`test_agriculture_legacy_write_integration.py` 使用真实 PostGIS、旧 Workbench 写方法及内部提交，但稳定地块映射和操作适配器仅在测试进程构造。它不是 W03 真实地块模型或生产映射验收。生产注册表仍空，旧不可信路由仍不开放。CI 未实际运行前，真实 PostgreSQL 项必须记为 not_run；运行结果以对应精确提交的证据为准。
