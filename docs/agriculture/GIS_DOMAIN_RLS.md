# GIS 数据库领域缺省拒绝边界（DT017）

本页记录数据库边界，不表示旧 GIS 93 张表的业务接口全部可用。生产农业入口仍不注册旧路由；W03 领域 CRUD、DT019 查询级授权及后续影像/报告/设备能力分别验收。

## 归属事实与不回填原则

机器清单为 `services/agri-gis/backend/app/agriculture/domain_table_inventory.json`：
93 张旧 ORM 表分为项目根 1、直接 project_id 40、仅 task_id 17、仅其他父引用 33、全局 plot 2。
测试逐表比对实际模型名称及全部外键，遗漏新模型或更改父链即失败。

`monitoring_projects` 只通过已存在、服务器控制的 `agriculture_project_bindings`
确定租户与工作区。浏览器、票据中的任意 ID、默认操作员、名字或旧 TaskPlot 交集均不能创建归属。

旧 `farmland_plots` 是全局唯一 plot_code；同一可变行可被多项目任务引用，
`plot_versions` 又只引用此全局 code。因此新增唯一
`agriculture_legacy_plot_ownership(plot_code,tenant_id,workspace_id,project_id,state)`。
只有独立迁移角色能显式装载或隔离映射；普通角色仅可读本范围 active 映射。
不自动回填、不猜租户、不改变历史记录。尚未绑定的旧图斑保留在库内但对业务角色不可见。
历史跨项目共享行必须经人工核准的领域迁移处理复制/隔离与血缘，不能任取一个关联项目。

## 角色、策略与未来表

- 显式迁移 `20261030_agriculture_domain_rls.sql` 只允许非超级用户、无 BYPASSRLS 的
  `flow_geo_migrate` 在 `flow_geo` 执行。该凭据不得作为 Web/Worker 默认连接。
- 93 表全部 ENABLE/FORCE RLS；84 未适配表撤回普通角色权限并设置 RESTRICTIVE false。
- 每个已开放表同时有最小 permissive allow 与精确 RESTRICTIVE scope，
  旧的 permissive USING(true) 不能通过 OR 绕过范围。
- 范围必须是事务局部单值 `app.tenant_ids` 与 `app.workspace_id`，
  缺任一值、空值、逗号租户列表、跨租户或同租户跨工作区均不能命中。
- 撤回未来 TABLE/SEQUENCE 的默认 app 授权；新应用表即使漏装策略也没有默认 CRUD。
  再次执行迁移会把未知迁移角色所属 public 表设置 FORCE RLS 并关闭普通访问。
  PostGIS 扩展表通过 pg_depend 的实际扩展归属识别，不靠名字冒充全局表。
- `flow_geo_worker` 是独立角色，本阶段不具备领域 CRUD；没有通用跨租户后台旁路。
- 原四个身份/防重表的历史迁移文件不改写；本次领域迁移为普通 app 角色额外叠加
  RESTRICTIVE 单租户与工作区交集，旧 ANY 租户列表不能绕过。原受限 guard 的角色
  权限保持独立，不转交业务连接。真实后域测试覆盖四表同租户异工作区和逗号范围拒绝。
- `domain_boundary.require_domain_boundary` 在业务 ORM 连接建立前检查 98 表、
  FORCE RLS、迁移所有权、精确普通角色权限和 restrictive 策略。
  只服务身份/防重的独立连接不因此被描述为领域业务已准备完毕。

## 本阶段唯一开放的 9 表

| 表 | 普通角色权限 | 归属约束 |
|---|---|---|
| monitoring_projects | SELECT | 唯一可信项目绑定及双范围 |
| project_users | SELECT | 当前项目，不能直接升级旧角色 |
| project_plot_attribute_fields | SELECT | 当前项目 |
| task_plots | SELECT | 任务项目与显式图斑所属项目必须一致 |
| monitoring_tasks | SELECT、UPDATE | 当前项目 |
| farmland_plots | SELECT、UPDATE | 唯一显式图斑归属 |
| plot_edit_operations | SELECT、UPDATE | 任务及 source/result 全部图斑必须同项目 |
| plot_versions | SELECT、INSERT | 显式图斑归属 |
| review_records | SELECT、INSERT | 当前任务；尚未适配的 quality_run_code 必须为空 |

这里只授数据库能力，不能替代稳定主体、服务能力、对象资源范围、审批、审计或 expected_revision。
DT016 测试中的真实 Workbench 内部 commit 仍经过生产身份验证、权属回调、防重、
TrustedActor 和精确操作/审计适配器。测试专用目录不成为生产域模型或对外 API。

## 暂不开放的 84 表

- `acceptance_reports`
- `administrative_boundaries`
- `area_statistics_import_batches`
- `area_statistics_snapshots`
- `change_candidates`
- `change_detection_events`
- `change_detection_runs`
- `dataset_asset_import_batch_items`
- `dataset_asset_import_batches`
- `dataset_asset_verifications`
- `dataset_assets`
- `dataset_lineages`
- `delivery_packages`
- `device_faults`
- `device_telemetry`
- `disaster_patches`
- `disaster_reports`
- `expert_consultations`
- `field_verification_artifact_events`
- `field_verification_artifacts`
- `field_verifications`
- `growth_monitoring_events`
- `growth_monitoring_runs`
- `growth_monitoring_zones`
- `imagery_assets`
- `imagery_fusion_events`
- `imagery_fusion_jobs`
- `imagery_import_batch_items`
- `imagery_import_batches`
- `imagery_mosaic_events`
- `imagery_mosaic_inputs`
- `imagery_mosaic_jobs`
- `imagery_processing_steps`
- `imagery_registration_events`
- `imagery_registration_jobs`
- `monitoring_devices`
- `monitoring_events`
- `monitoring_stations`
- `offline_archive_events`
- `offline_archive_sources`
- `offline_archive_volumes`
- `offline_archives`
- `pest_alerts`
- `pest_assessments`
- `pest_model_versions`
- `pest_report_items`
- `pest_reports`
- `plot_attribute_import_batches`
- `plot_edit_operation_events`
- `plot_quality_checks`
- `production_audit_events`
- `production_batches`
- `project_plot_attribute_field_audits`
- `project_rule_config_audits`
- `project_rule_configs`
- `quality_issues`
- `service_access_requests`
- `service_credentials`
- `service_health_checks`
- `service_usage_events`
- `shared_services`
- `statistics_reports`
- `supervision_county_evaluations`
- `supervision_events`
- `supervision_findings`
- `supervision_inspections`
- `supervision_plans`
- `supervision_reinspections`
- `supervision_reports`
- `supervision_samples`
- `task_quality_runs`
- `thematic_map_atlas_items`
- `thematic_map_atlases`
- `thematic_map_events`
- `thematic_map_products`
- `thematic_map_templates`
- `uav_aircraft`
- `uav_artifacts`
- `uav_events`
- `uav_findings`
- `uav_missions`
- `vector_export_packages`
- `work_package_plots`
- `work_packages`

这些表不是被删除或判定实现完成。后续工作包须补齐所有父链一致性、对象授权、
不可变归属和真实负例后，逐项改变显式清单及授权；不能一次性恢复旧默认 CRUD。

## 验证与阶段边界

新领域真实 PG 用例：
`test_postgres_domain_scope_inventory_and_unmapped_denial`、
`test_postgres_domain_cross_workspace_parent_and_policy_denial`。
覆盖 93 表策略、无 scope、同一物理连接跨租户/工作区复用、未绑定图斑、
ProjectUser 禁写、未来表默认无权、跨工作区父引用、唯一归属、旧 permissive 真策略不能绕过。
第三项 `test_postgres_domain_workbench_commit_and_audit_rollback` 实测最终权限下的
真实服务提交、稳定身份历史保持、伪造操作员拒绝及数据库审计失败时原子回滚。

迁移前 DT013 合成 ProjectUser 写入审计是历史组件证据，不声称它在 DT017 迁移后仍允许。
该旧测试显式要求 fresh pre-domain 阶段，发现领域迁移已经存在即失败，不跳过也不撤回 RLS。
迁移后以禁止 ProjectUser DML、允许真实 Workbench 事务及实际浏览器签名整链为验收。
所有阶段都必须记录实际数据库/来源/退出码，不能靠测试顺序把未经执行的阶段说成通过。

本地未设置隔离 PostgreSQL DSN 时真实 PG 用例明确 not_run；机器清单单元测试不替代 RLS。
失败回滚应关闭农业入口、恢复经过验证的数据备份与迁移检查点，不能通过关闭 RLS、
恢复默认 app CRUD 或给运行角色表所有权来恢复服务。无生产数据操作或外部授权变更。
