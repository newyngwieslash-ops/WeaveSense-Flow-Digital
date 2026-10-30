# DT019 原生 PostgreSQL 查询范围证据

冻结源码：`26343a0cc89c82ffef92558b5a3a9ed18d62618a`。

三个独立新数据库阶段均实际执行并关闭成功：

| 原始安全报告 | 通过 | 失败/跳过 | 范围 |
|---|---:|---:|---|
| query-final.json | 17 | 0/0 | 14 组件测试 + 3 个真实查询 PG 主测试 |
| components-final.json | 152 | 0/0 | 原身份/actor pre-domain 组件，以及当前 post-domain RLS、worker、池复用、真实 Workbench 提交/回滚 |
| migration-final.json | 6 | 0/0 | 5 组件测试 + 正式 CLI fresh→repeat/漂移/权限/回滚 PG 主测试 |

环境为 PostgreSQL 17.11、PostGIS 3.5.2、Python 3.12.14；包版本、摘要、实际时间及每项测试见原始 JSON。三个阶段各自的 285/295/285 个源文件摘要均逐个对冻结提交核对，无差异。其他作者的并行改动不属于这些被验证文件。

另有 query inventory 2 个组件测试通过，215 个 app Python 源文件、412 个数据库执行函数纳入机器清单检查。源码改变必须重新审查快照，快照本身不是业务验收。

三个首轮失败安全报告保留，不覆盖为成功：ReviewRecord 非 parcel 表的 SQL 42703、重放旧缺省拒绝策略时新引用表被隔离、nested TextClause 严格拒绝与原固定角色探针的兼容问题。修复均保持权限负例，不删除或放宽断言。详细对应关系见 `reconciliation.json`。

这不是新的 GitHub/Docker、真实浏览器或真实农场验收。新查询源码的浏览器签名整链仍需单独执行；不能把旧 DT016/017 的链路成功冒充当前源码结果。不包含数据库秘密、原始驱动日志或业务数据。
