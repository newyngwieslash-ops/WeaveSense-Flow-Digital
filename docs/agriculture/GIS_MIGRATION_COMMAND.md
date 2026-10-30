# 农业 GIS 显式迁移命令

此命令只供 `flow_geo_migrate` 在独立 `flow_geo` 数据库执行；不是应用启动钩子，普通 API、worker 不执行 DDL。数据库角色、PostGIS 扩展及受限 `flow_geo_actor_guard` 由已有受审初始化流程建立。不得使用超级用户、runtime 用户或 `SET ROLE` 冒充迁移登录。

`existing` 的前提也包括新的第 7 个登录角色 `flow_geo_worker` 已由 DBA 配置。已有六角色集群**不要重跑**非幂等的 `deploy/agriculture/postgres/init.sh`，它是新库首启脚本。DBA 先检查该角色不存在，再在受控 `psql` 会话中执行下面的增量步骤；已有同名角色则先审查权限，不盲目覆盖。使用交互式 `\password` 输入独立秘密，不把密码拼进 SQL、shell 参数或提示词：

```sql
CREATE ROLE flow_geo_worker LOGIN NOINHERIT NOSUPERUSER NOCREATEDB
 NOCREATEROLE NOREPLICATION NOBYPASSRLS;
\password flow_geo_worker
GRANT CONNECT ON DATABASE flow_geo TO flow_geo_worker;
\connect flow_geo
GRANT USAGE ON SCHEMA public TO flow_geo_worker;
```

不得授予角色成员资格、表 CRUD、默认表权限或 schema CREATE。迁移命令只验证/使用预建角色，不自动提权或生成长期凭据。既有 actor guard 的 NOLOGIN、NOBYPASS 与迁移角色 `INHERIT FALSE` 成员关系也必须保留。此处为受控升级说明，不代表已经在用户生产数据库执行或通过 live 升级验收。

## 执行

先按 `PYTHON_ENVIRONMENT.md` 安装固定依赖，在 `services/agri-gis/backend` 中运行。通过受控秘密注入设置 `FLOW_GEO_MIGRATION_DATABASE_URL`；不得把 DSN 写在命令行、日志或版本库，不会回退读取 `DATABASE_URL`。

```sh
python -m app.agriculture.migrate --mode new
```

`new` 仅接受没有任何非扩展业务表的新库。创建固定 AgriScope 审查基线的 93 张 ORM 表，随后在同一个数据库事务中安装三个农业 SQL 边界。不创建租户、项目、用户或图斑归属；现有图斑不自动认领。

已有兼容基线或重试已完成安装：

```sh
python -m app.agriculture.migrate --mode existing
```

`existing` 要求全部 93 张表已存在，所有业务表归专用迁移角色。逐表检查列集合、类型（含空间类型/SRID）、空值性、服务器默认值、主键、外键、必须的唯一键及 CHECK 约束。CHECK 由 PostgreSQL 对私有临时空表进行语法规范化后比较，不改旧业务表。未知结构差异失败，**不是**任意旧版本自动升级工具；缺列、半成品 schema 不能靠 `create_all(checkfirst)` 自动修复。额外旧表保留，由领域迁移设为缺省拒绝；新增功能需另行审核迁移。

兼容来源限定为已审查 AgriScope `571a2dcd9abf8b8a22b66c750ee710b61d482051` 的实际 ORM schema；独立旧系统有额外触发器、外部依赖或其他自定义业务语义时仍需人工审查，结构检查不能证明这些业务语义兼容。

## 原子性与重试

- 单连接、单事务、固定事务级 advisory lock；锁等待 10 秒、单 SQL 120 秒上限。连接异常不报告成功。
- 账本 `agriculture_schema_migrations` 记录三个原始 SQL 文件的 SHA-256 与首次安装时间；未知版本或校验和改变直接失败。不修改旧版校验和。
- 账本 ENABLE/FORCE RLS，只迁移 owner 可维护；PUBLIC、app、worker 无读写授权。它不包含业务资料或秘密。
- SQL 边界可重复安装；既有 actor guard 函数保留原 NOLOGIN 所有权，不增加迁移角色继承权，不给 runtime 角色成员资格。
- 事务中任何错误回滚本次操作；网络中断导致提交结果未知时，保留错误，恢复连接后显式用 `existing` 核验。不要删除账本或强改余额、归属、角色以绕过失败。
- 成功输出只含模式、表数、迁移文件名/摘要。失败输出固定安全错误码，不输出 DSN、底层 SQL 参数或驱动错误正文。

## 验证边界

`tests/test_agriculture_migration.py` 为组件测试；`tests/test_agriculture_migration_postgres.py::test_postgres_migration_command_new_repeat_and_drift` 必须在独立新 PostgreSQL 数据库执行，实际启动命令行 `new → existing`，检查 runtime 账本权限、错误角色、列漂移和 SQL 中途失败事务回滚。缺少隔离数据库时本地 skip 不算通过；CI 将该测试作为独立 fresh-volume 阶段，不能与旧 synthetic actor pre-domain 测试混用同一已迁移库。

该命令不表示 93 张表的业务 API 全部可用。当前仅 9 表受审闭环获得最小权限，84 表仍关闭；真实租户归属、农场/地块生命周期和更多查询授权由后续任务交付。
