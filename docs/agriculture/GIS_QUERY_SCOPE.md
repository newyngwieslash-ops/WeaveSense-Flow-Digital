# GIS 查询资源范围（DT019）

## 边界与接口

本任务在 DT013/017 的可信身份和数据库隔离之上，补充**同一工作区内**的资源范围。它不是 W03 的农场/稳定地块生命周期，也不新增第二套账户、Farm 实体或默认 fixture provider。

`agriculture_legacy_resource_references` 是迁移 owner 配置的引用适配表：旧 `plot_code` 对应已有的 canonical `parcel_id`、`farm_id`，通过复合外键绑定现有 tenant/workspace/project/plot ownership。没有几何、种植季或业务生命周期；没有公开创建端点，没有从票据猜测归属、默认农场或旧数据自动回填。缺少或隔离中的真实归属一律拒绝。测试由 owner 显式创建合成引用，不代表已接入真实农场。

`GISRuntimeDatabase.session(verified)` 的 `QueryScope` 只从实际 DT012 验证输出取得 tenant/workspace、稳定 actor、typed resource refs、parent farm ids 和过期时间，并拷贝成不可变值。建立会话前，独立查询 GIS 引用表，要求**每个**请求资源都匹配且实际父 farm 集合精确一致；混合合法与越权/未映射资源不会部分成功。明确 farm 授权可覆盖该 farm 的已配置图斑，parcel 授权只覆盖该 parcel，不能因为票据附带 parent farm id 就扩大成全 farm。

每次事务（包括 legacy 内部 commit 后的新事务）重新 `SET LOCAL` 完整范围，连接归还回滚清理。业务查询不能用 `select(func.set_config(...))`、原生 SQL 字面量或修改原 claims 重绑定范围。查询到时再次检查短票据过期；撤权遵循既有独立 authority callback、映射状态和 PostgreSQL 事务快照边界，不宣称可以取消已经开始的跨数据库计算。

## 数据库策略与计数保护

- plot、PlotVersion、TaskPlot 等实际查询由新的 `AS RESTRICTIVE` 资源策略约束，覆盖 ORM、别名、Core、聚合、子查询与原生普通角色 SQL；不是只在列表返回后过滤。
- `flow_geo_query_guard` 是专用 NOLOGIN/NOINHERIT/NOBYPASS 角色，只读取所需关联列，普通 app/worker 不可 SET ROLE。固定 `search_path=pg_catalog` 的 helper 只返回布尔、不改 GUC、不返回隐藏计数或业务行。
- helper 用真实、未被资源过滤的 TaskPlot 关联全集证明 complete scope；空任务、未映射关联、错误父 project 都不能变成“全部授权”。
- 完整 MonitoringTask 含 `total_plots/completed_plots/quality_score` 等全任务信息，因此只在**全部**关联 plot 均授权时可读/改。部分 farm 授权不能先过滤子集，再重算并写坏全任务进度；这时旧 Workbench 完整任务写闭环明确拒绝。
- 对部分任务合法的空间查询使用受控 `security_barrier` locator view，只投影 `id/task_code/project_id` 并要求至少一个真实授权关联。FarmlandPlotDAO 的内部 join 使用该 view；不开放任务计数或更多字段。由此避免 Task→TaskPlot→Task 的 RLS 递归。
- 项目级 `progress` 同样不能泄漏：app 对 MonitoringProject 仅有 id/code/year/status 四列 SELECT；ProjectUser 仅当前 Verified actor 的活动绑定行可读，项目治理仍由 Go 平台负责。
- task 级 ReviewRecord 没有 parcel 外键，不能伪造关联；它保守要求 complete task scope。明确 parcel 历史使用真实 PlotVersion/ReviewDAO，间接越权返回空/不存在。
- 84 个尚未完成领域授权的旧表仍保持关闭；没有通过开放全部权限来维持未适配服务。

## 查询入口审计

机器清单：`services/agri-gis/backend/app/agriculture/query_inventory.json`。它列出实际数据库执行函数、位置、调用接收者、源文件摘要及边界分类，并记录全部 app Python 文件摘要，连动态调用/新增路由的源码变化也必须重新审查。生成器的 `--write` 只是显式更新审查快照，不授权任何新表或路由；日常验证运行 `--check`。

```sh
cd services/agri-gis/backend
python scripts/query_scope_inventory.py --check
python -m pytest tests/test_query_scope_inventory.py -q
```

| 入口类别 | 实际实现与授权处理 |
|---|---|
| point/bbox/list/detail/catalog | FarmlandPlotDAO；完整 SQL 资源交集，task join 仅 locator 安全列 |
| 计数/空间统计/分组 | StatisticsDAO `get_totals`、各 group；数据库聚合前过滤，不返回外域计数 |
| 导出源行/选项/计数 | VectorExportDAO `get_export_rows/count_features/get_*_options`；同一资源策略 |
| parcel 版本详情/历史 | ReviewDAO `get_versions/get_version`；PlotVersion 的 plot 关联需当前资源授权 |
| 完整 task/review/操作历史 | full-task helper；混合 farm 部分授权时不可读全局信息 |
| 服务直接查询 | 与 DAO 使用相同农业工厂及数据库策略；清单并不只扫描 DAO |
| 未适配领域、旧不可信路由 | 保持拒绝；新增表需 inventory/迁移审查，新增路由需既有 entrypoint contract 审查 |

StatisticsService 的完整年度历史、正式导出包生成/下载、资产/影像等未来业务，不因上述源行查询通过而宣布可用。原上游隔离应用代码保留，但它的普通 AsyncSession 与旧路由不注册在农业入口；农业工厂不会回退到旧无范围连接。

## 实际验证与限制

`test_agriculture_query_scope_postgres.py` 使用真实 PostGIS、生产迁移/工厂与真实 DAO：两个租户相同坐标、同一 workspace 两个 farm 共 task、另一个 workspace、未映射 plot、历史关联、统计/导出源行、ORM alias/Core/raw SQL、部分 task 完整计数和写入拒绝、完整授权读真实计数、混合请求/空范围/父 farm 错误、回滚后范围不变。原 native PG 日志/结果保留首轮失败，成功只按对应 source hash 计入证据。

短期凭据、数据库 DSN、实际业务内容不进入报告。当前只开发隔离测试；没有在用户生产库执行引用配置或升级。真实浏览器→Go→GIS 的旧签名写链需要在本次源码上单独回归，不能用旧 DT016/017 成功替代。
