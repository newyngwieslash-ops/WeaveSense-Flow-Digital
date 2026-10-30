# DT-020 私有对象与瓦片鉴权基础

最终源码：`c8106dfcaef2949e5271aa94cae5213224faa0c5`。
实际本地 fresh PostgreSQL 17.11/PostGIS 3.5.2：47 pass、0 fail、0 skip，
进程及集群关闭退出码均0，1464源码哈希与该冻结提交逐项一致。

命令：
`python scripts/agriculture/native-postgres-test.py --package-root .local/native-postgres-current/root --python .local/identity-rebuild/venv/bin/python --phase objects --go .local/toolchains/go/bin/go`

真实平台会话与Go授权/签发，经真实HTTP进入Python、普通PG角色清单与独立权属事实，
读取实际私有文件；HEAD/Range、路径/版本/作用域替换、撤回、成员撤权、恶意上游响应
和非UTC服务签发均有真实测试。平台测试使用SQLite，GIS使用真实PG，不能混称平台PG。
修复了元数据读取前scope检查和宿主时区签发缺陷，未放松签名或访问断言。

独立审查通过。历史首轮helper执行中更新的报告保留，不作为最终冻结来源证明。
这是DT020原任务鉴权基础，不代表S3、大型COG、瓦片生成、资产生命周期、W03真实
领域provider、生产数据或完整AG-AC/W02/G0验收；当前生产缺领域provider仍拒绝。
报告为本地执行，不是远程CI；已有发送字节与客户端保存副本不可追回。
