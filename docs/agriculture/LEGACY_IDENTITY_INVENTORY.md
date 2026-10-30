# 旧 GIS 身份入口清单（运行环境恢复后重建）

本实现于 2026-10-29 根据当前真实源码重新实现，版本为
`2.0-reconstructed`。不是旧补丁的逐字恢复，不继承旧报告 SHA 或旧测试通过结论。

## 静态清单的边界

`scripts/agriculture/legacy-identity-inventory.py` 仅解析 Python AST，不导入或执行
被扫描代码。扫描整个 `services/agri-gis/backend/app` 的 Python 文件并记录每个文件
SHA256，因此新增没有身份参数的列表、详情、下载入口也会导致快照检查失败。
清单包含所有声明的路由（包括未挂载路由）、注册位置、路由参数、继承/嵌套 schema
身份字段、调用及导入引用；动态路径标记 `<dynamic>`，不推断安全性。

身份候选包含 operator/reviewer/uploader/requester/user/investigator/actor/applicant
等 `*_code`，以及所有 `*_by_code`。`assignee_code` 是指派目标，不是调用人。
输入 schema 的调用人字段不可信；响应与持久化历史字段只作为追溯数据，不用作认证。
`TaskSubmitRequest.reviewer_code` 实际表示提交人，不应凭名字误判为指派审核人。
`ReviewActionRequest.reviewer_code` 表示执行审核的人；响应记录中的同名字段是历史事实。
`credential_code`、`credential_secret`、`api_key`、`api_key_hash` 单列为服务凭据，
不等价于人的平台身份，也不等价于 DT012 签名服务上下文。

语法候选数量不是漏洞数量：约束名和内部引用也会被记录；没有显式身份参数不代表没有
动态请求输入。静态扫描不能证明运行时授权，不能代替租户隔离或真实数据库测试。

## 可执行检查

```sh
python3 scripts/agriculture/legacy-identity-inventory.test.py
python3 scripts/agriculture/legacy-identity-inventory.py --check
PYTHONPATH=services/agri-gis/backend python -m pytest \
  services/agri-gis/backend/tests/test_legacy_identity_contracts.py -q
```

源码经过审查且冻结后，显式使用 `--write` 更新
`docs/agriculture/evidence/w01/legacy-identity-inventory.json`。CI 只能 `--check`，
不能先自动覆盖快照再宣称发现漂移。任何语法错误使扫描失败而不是跳过文件。

## 运行时负例

新合同测试调用真实公共 gate 和 SQLite actor binding fixture，不 mock 授权结果。
覆盖六种调用人编码、十二个独立下载能力、实际审核/提交/指派 schema 和服务凭据路径。
ASGI 测试导入真实旧 `app.main`，逐个调用源码内全部旧业务路由并要求 404；显式健康
端点不在业务关闭范围内。测试不启动 lifespan，不冒充数据库健康、PostgreSQL RLS、
真实农场或生产部署验收。当前旧业务路由关闭意味着不能绕过新身份入口，并不意味着
这些业务已经恢复可用；领域资源适配器和独立验收仍需要完成。

恢复后的测试结果及报告哈希须由新执行证据提供，不复用环境丢失前的日志。

2026-10-29 重建后的实际执行：scanner 单元测试 4 项通过；上述运行时合同测试
21 项通过、0 跳过（Python 3.12 锁定依赖环境，SQLite/ASGI）。实际日志分别位于
`.local/recovery/inventory-reconstructed.log` 和
`.local/recovery/identity-contracts-reconstructed.log`。合同文件 Ruff 检查与格式检查通过。
最终 source snapshot 须在各 runtime 作者冻结后另行刷新并核对；这些测试不代表
DT013 PostgreSQL 验收或整个工作包完成。
