# DT020 私有对象当前 Python / PostgreSQL 验证

冻结源码：`0422eee1cf2a93bf2908f71e7bc1633abbec34f5`。

| 独立 fresh cluster 阶段 | 实际结果 | 范围 |
| --- | --- | --- |
| objects | 46 passed / 0 failed / 0 skipped | 44 个组件用例及 2 个真实 PG 必需用例；其中内容链使用真正 uvicorn loopback TCP、httpx、普通 app 角色、独立 fixture 权属表和实际本地字节 |
| components | 152 passed / 0 failed / 0 skipped | 既有身份、pre-domain 组件和当前 post-domain 池隔离 / 权限 / 实际 Workbench 写入与审计回滚回归 |
| migration | 6 passed / 0 failed / 0 skipped | 正式 CLI 新库 / 重复升级 / 漂移 / 失败回滚与权限校验 |

每阶段退出码及独占 PostgreSQL 关闭退出码均为 0。报告保留运行时版本、官方软件包 SHA、实际测试名和阶段，而不是只保留汇总计数。报告中的工作树标志原样保留；实际记录的 297 / 304 / 294 个源码文件全部逐一对上述不可变提交匹配，没有借用“当前 HEAD”代替来源。

`reconciliation.json` 另外记录 objects 纯配置测试导入的 `object_http_fixture.py` 完整 SHA；此传递依赖尚未进入当次 runner 默认列表，不能隐瞒为默认已覆盖。其字节另行验证与同一冻结提交完全一致。

执行命令（每个 phase 单独创建、初始化、关闭一个私有实例）：

```sh
python scripts/agriculture/native-postgres-test.py \
  --package-root .local/native-postgres-current/root \
  --python .local/identity-rebuild/venv/bin/python --phase objects
# 另外分别执行 --phase components 和 --phase migration
```

本批已覆盖审查发现的路径 scope 顺序缺口：已验证票据的 tenant/workspace 与路径错配时，在任何对象元数据查询之前统一 403，foreign 对象存在与缺失均不调用 store；修复提交为 `05891f0b`。也覆盖对象/版本换用、无 scope 和跨 tenant/workspace、不可变字段、私有文件穿越及祖先符号链接替换、实际 SHA 校验、HEAD/单 Range、权属撤回及对象撤回、错误 no-store 与不返回 304。

这不是 remote CI、生产 S3、真实客户数据或正式资产/报告发布验收。独立跨语言 Go → Python 对象整链正在另一个测试切片实现，本批明确没有执行该整链；后续不得将其缺失隐含为已通过。数据库 job 侧表的当前迁移和精确权限兼容在本批回归中执行，但 DT022 专用作业授权用例由它自己的独立证据负责。

仅归档脱敏结构化原报告，未归档 DSN、密钥、私有日志、数据库目录或临时对象。SHA 是本地完整性及源码对应证明，不声称防最高权限篡改的外部信任根。
