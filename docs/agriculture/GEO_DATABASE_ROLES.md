# GIS 独立数据库角色（DT017）

`flow_geo_migrate` 仅用于受控迁移；`flow_geo_app` 是 GIS 请求业务身份；新增
`flow_geo_worker` 是独立后台身份。三者均无超级用户、BYPASSRLS、建库或建角色权限。
worker 为 NOINHERIT、无任何角色成员关系，仅有 flow_geo 的 CONNECT 与 public 的
USAGE；不会继承 migrate 的默认表授权，也不能 SET ROLE 到 app/migrate/actor_guard。
这里指显式服务数据库授权；未改变集群维护数据库 postgres/template1 的默认 PUBLIC
CONNECT。跨服务数据库 flow_platform/flow_intel 的连接拒绝另有真实权限测试。

## 配置与开放范围

在私有环境文件配置独立随机 `GEO_WORKER_PASSWORD`，不要复用其他数据库密码。
Compose 仅交给 PostgreSQL 首次初始化，**不交给 Web/GIS 请求进程**，不启动虚构后台任务。
当前 worker 即便设置正确 tenant scope 也不能读写任何业务表；它不是全局系统旁路。
未来有真实持久任务模型后，另行审核固定 claim/租约方法的 EXECUTE 和逐任务最小权限。
不得为方便扫描授权全部表，也不得使用空 scope 或迁移凭证充当后台授权。

## 已有卷与回滚

`init.sh` 仅适用于全新卷，不会自动修改已有数据库。已有环境应先备份角色/权限清单，
由授权数据库管理员在维护窗口执行新增角色、CONNECT/USAGE 三条等价语句；密码经
私有 psql 变量提供，不进入 SQL 文件、命令参数、日志或版本控制。不得重跑整份 init，
不得删卷来规避迁移。若无此管理员权限，记录该环境 blocked_external。

角色变更是增量且未改旧 Flow/Loom 权限。回退时先停用所有持有 worker 凭证的进程，
撤销其数据库 CONNECT/schema USAGE，并设 NOLOGIN；确认没有新任务授权和依赖后才由
管理员 DROP ROLE。不得使用 DROP OWNED/CASCADE 删除业务对象，也不要修改旧账本或审计。
不把恢复旧的空 scope 系统权限作为回滚方案。

## 证据边界

`test_geo_worker_role_policy.py` 是配置/脚本静态检查，不是数据库验收。
此增量提交仅完成本地静态测试；第七角色的真实 PostgreSQL 检查尚待 CI 执行。
`scripts/agriculture/geo-worker-role-test.sh` 由既有隔离 ci-smoke 调用，在真实 PostgreSQL
上以 TCP 密码验证第七角色：跨库、DDL、角色切换、无 scope/有 scope 表访问、写/删/
TRUNCATE 均拒绝，原记录不变。原六角色测试保持原样。旧 GIS 表的租户 RLS、连接池
scope 清理及后台 claim 均须各自实际验证，不能用角色创建成功宣称全部 DT017 完成。
