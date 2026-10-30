# DT013：旧 GIS 操作人入口的默认拒绝与受控适配

> 执行器重置恢复候选：据原文及真实操作记录重建，非原文逐字节恢复。
> 本文保留历史验证事实；恢复后的新执行证据见文末，不能混作原提交逐字恢复。

本次信任链改造不等于旧 GIS 功能已完整集成，不等于 DT017/W03 数据隔离、
稳定地块及真实业务验收已完成。原算法、状态机、schema、审计模型继续保留。

## 主体与权限

旧 app.main 仅挂 health；原业务 router 保留在 LEGACY_ROUTERS_NOT_MOUNTED。
农业入口仍为 app.agriculture.main，不直接挂旧业务路由。没有生产/demo开关
可恢复客户端用户编码作为授权来源。

ProjectUserService.require_capability 要求专用 AgricultureSession 与
服务器解析的 TrustedActor、project一致、兼容用户编码与绑定成员一致、
精确签名 action 与旧领域能力交集。独立资源适配器使用真实方法参数查找
业务对象及显式稳定资源绑定；不能拿票据里的资源自证归属。
异步资源解析后再次检查票据有效期和当前成员绑定，不能延长有效期。

operator/reviewer/uploader/investigator/requester/user 编码均不能选择主体。
旧 list_project_users 直接拒绝，不先查项目存在性，不回退默认用户。

edit_plots 共享能力必须区分 create/update/delete。split/merge/history
等复合操作未适配默认拒绝。manage_rules 不能冒充 farm.update，旧任务审核
不能冒充 report.review。下载需要独立 asset_download.read，且不覆盖旧领域能力。

## 操作适配器

operation_authorization.py 定义 OperationAdapter(operation, capability,
action, resolve) 与 OperationFacts(tenant_id, workspace_id, project_id,
resources)。服务端 db.info['agriculture_operation_adapters'] 是 tuple，默认空。
resolver得到只读targets，最长两秒；缺适配器或基础设施异常503，作用域不符403。
Workbench.update_plot_attributes 声明固定操作名和实际 task_code/plot_code，
不把旧plot_code冒充稳定地块ID。此接入点不是生产资源映射完成。

ServiceSharing.record_usage 的旧 API key 分支默认403，包括正确 key；旧密钥
算法仍保留待独立服务主体适配，不能借人类入口恢复旁路。

## 历史验证与恢复边界

原环境 Python3.12 .local/agri-identity-venv：新增真实SQLite gate测试15pass，
0.88s；原tracked GIS测试先385pass/7fail、10.35s，明确迁移新契约后392pass、8.91s。
无删除、无skip、无宽松mock补洞。五个无可信主体用例改为更早403并断言用户DAO
不调用；一个旧key用例断言错误/正确key都拒绝且不写事件不提交；一个属性更新
保留所有业务和版本审计断言，精确调用断言增加固定action/operation/targets。

原大量测试注入user-service stub，只证明算法回归；新真实SQLite gate与独立
PostgreSQL业务集成须分开报告。不能将恢复候选称为已执行这些测试。

原失败log SHA256:
0dd0b35c28a843d15aa2a0427f3073b01a88d1dcdebc6dcdd4c80e4d2570329e

原迁移后log SHA256:
c42447ca264d8708bf10ffa8f66bbc8d7e6e3605a6683e731aeba3fbcde460ae

日志原始bytes未在本次恢复中取得，不能重新生成文件冒充原证据。

## 恢复后的独立新验证

冻结源码 `91e3fd5d8bf07e02410ef5375d25fee5fdc5976a` 对应
`evidence/w01/native-pg-20261029/final-report.json` 的全部 280 项源码哈希。
实际新建本地 PostgreSQL 17.11 / PostGIS 3.5.2 集群运行 123 passed、0 failed、
0 skipped，包括真实签名与 nonce、绑定撤销并发、Workbench 内部提交及审计回滚共
6 项必需数据库测试；进程与清理退出码均为 0。该结果支持 DT013 原任务范围完成，
不是远程 CI，不开放未适配旧路由，不替代 DT017/W03 或 live 农场验收。
