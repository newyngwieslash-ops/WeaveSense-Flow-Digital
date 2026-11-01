# DT026 / DT041 inventory只读预审（开发中，非验收通过）

基线为已提交25任务完成状态之后的审查快照，当前工作树仍由多个分包更新。未运行--write，未修改scanner或snapshot。

## 精确比对

- legacy declared routes 179 → 180，旧179路由键(path/function/route/methods)无删除。
- identity sites 1526 → 1526；忽略位置行列，按path/scope/field/kind/classification/evidence多重集比对无旧条目删除/新增。
- registrations 4 → 8，新asset_catalog、asset_object、parcel固定动态注册列入；未把动态路径当成已授权证明。
- query entries 425 → 435，按path/function多重集旧条目无删除，新增10入口。
- 本次扫描完整源码hash与具体JSON分别为legacy-current.json及query-current.json。最终冻结需重新scan核对新增delta。

## 新边界核对

parcel routes以真实path资源提取器传入原非空business delegation；list farm/ids模式精确校验，historical selector绑定当前resource version，receipt使用独立受限typ。ParcelStore复用FarmStore短事务/真实scope/到期回滚，父farm→parcel锁顺序已明确，缩界guard检查active/current revision而不删除历史。

asset catalog使用独立workspace资产typ、签名bodyhash和current callback，不把no-farm资产塞入空businessscope。asset ownership只返回当前scope元数据，数量/ID集合/tenant/workspace/状态及票据有效性再次验证。typed byte route区分asset_revision当前授权与asset_version历史，读取前后都调用current_reference，并通过CatalogObjectStore检查当前与历史权利及真实entity；原普通objectgateway不被放宽。

资产目录存储SQL写入和ownership查询都有明确tenant/workspace事务scope和参数，不跨平台表写。元数据登记不产生ready对象。

## 未关闭问题

**高优先级：asset_catalog_store.execute锁等待后可过期提交。** 当前transaction只set_config后yield，无固定statement/lock timeout，无yield后DBclock expiry复核；execute仅在入事务前检查expires。等待advisory/row锁期间票据过期仍可写入并提交，随后route.current返回401/403不能回滚已提交写。已通知实现负责人，须补真实锁等待过期与全领域无副作用负例、固定限额及commit前到期回滚，未修前不能批准该写边界。

## 范围限制

该静态query scanner只识别execute/scalar/stream等，**不枚举asyncpg全部fetch/fetchval/fetchrow读取**，因此数量不等于所有SQL已审计。上面针对新增stores和typed routes做边界预审，最终仍必须完整路由/真实PG/对象读取中撤权/浏览器验收；没有把此文记作任务done或最终许可/商用结论。

## 到期写入漏洞复核（11:28 UTC追加）

asset_catalog_store.transaction已加入固定statement_timeout=3s、lock_timeout=2s及expires_at可选写入边界；execute真实写事务传入verified.expires_at，advisory/row锁后及yield退出commit前用数据库clock_timestamp复核。提前return也经过asynccontextmanager退出检查；异常回滚，不以响应后复核代替事务保护。

实际报告.local/audit/dt041-catalog-pg-2sneorva/report.json为16pass/0fail/0skip/exit0/source_stable=true；含advisory、row、commit三参数真实阻塞/延迟负例。各自断言401到期与四表完整行快照无变化。owner对应SQL明确只授flow_geo_migrate owner policy，快照不是空scope被RLS掩盖的只读假空。当前关键源码与报告hash核对一致。此特定发现已关闭；冻结候选仍需整体CI和最终delta审查。
