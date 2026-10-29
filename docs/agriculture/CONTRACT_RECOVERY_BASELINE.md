# DT-007 恢复后的机器契约基线

日期：2026-10-29。范围仅 W00 最小 schema、版本、作用域负例与职责边界，
**不是实际 endpoint、签名、完整农业 API 或商用验收**。历史任务完成与本次新执行
证据分开；本文件没有改变任务状态。

## 可追溯来源

- 原规格：`specs/agriculture/v1.0.0/docs/DATA_API_CONTRACTS.md` 和
  `docs/ARCHITECTURE.md`（该 docs 指 SPEC_ROOT 下目录）。这两份是需求合同，
  不是自动可运行的服务。
- 恢复来源：本地可读 Git 提交 `c20fa403468951beb2c898a05fb1062337178c33`。
  逐 blob 比对 9 份 `contracts/agriculture/*.schema.json`、7 份 examples、
  `openapi.json`、`tests/agriculture/test_contracts.py`，18/18 精确相同。
  这是此前 CI scaffold 的来源提交，不混作当前产品代码提交。
- README 曾引用的 ADR-027 原文没有恢复。新追加 ADR-029R 明确这一缺口，
  当前 DT012 协议细节以 `docs/agriculture/SERVICE_IDENTITY.md` 为准。
  本次只修正文档引用和本地测试环境路径，schema/示例/测试没有改动。

## 实际职责与尚未实现的边界

| 权威服务/库 | 权威数据 | 交互与实际限制 |
|---|---|---|
| Go / flow_platform | 可信用户会话、租户/成员/工作区、资源授权、主 Agent、审批、预算与账本 | 会话决定 actor；浏览器/LLM 不能指定身份。对 GIS 使用受限服务调用，不写 GIS 表 |
| GIS / flow_geo | 农场、地块/版本、种植季、空间数据、影像、观测、作业、外业和领域审核 | 再次校验资源归属；不自行创建客户登录或可消费余额。当前真实领域映射仍待 W03 |
| Loom / flow_intel | 获准资料采集、文档修订、知识索引/检索及外部证据 | 不充当地块空间库；不直接修改平台余额或 GIS 数据 |

这张表规定写入所有者，不是“全路径已经证明不跨库”的审计结论。新跨服务交互
必须经过 API 与 outbox/inbox；事件至少一次投递、唯一键/业务去重，不声称天然
exactly-once。旧 Flow `backend/internal/database/flow_rls.go` 空 scope system
策略仍需 DT-018 改造和系统任务回归（依赖 DT-017 GIS 独立角色）；旧 GIS bbox/领域模型不因 schema 带 tenant_id
就自动隔离。关闭未适配路由不是领域功能已完整实现。

## 冻结的最小合同与负例

- JSON Schema 2020-12，本地 Registry 解析 `.invalid` namespace，不出网取 schema。
  未知版本、额外字段、缺 tenant、空/通配能力被拒绝。
- delegation 的只读/写请求绑定、单一 action、actor ID、policy/resource revision/farm
  snapshot；service-request 的调用方向/受众/能力独立。结构通过不代表可信签名。
- analysis-submit 不允许客户端写 tenant/user/operator/requester/role/price/object key。
  OpenAPI 仅 `/analyses`、`/jobs/{id}`，明确 `contract_only`，没有声明实现路由。
- 三种事件、作业状态/结果、NDVI-change 参数、公共产物 manifest；拒绝内部存储定位符、
  秘密字段、自嵌 manifest hash、错误 hash 格式和非法计费整数。
- insufficient_data 不得附正常统计或正常产物。示例是固定 demo 数据与 fixture hash，
  不表示影像已经测量、对象已经上传或实际事件已经发布。
- schema 不能证明时间窗口、revision 单调性、CRS 数值正确、当前成员权限、nonce、
  lease/fencing、账本竞态或内容 hash 相符；必须由后续真实服务/数据库测试验证。
- delegation/service-request 使用 `rfc8785-safe-v1`；其余 `sorted-json-v1`
  仍属有界暂定标签，不能直接当成已完成跨语言序列化/签名规范。

## 新执行记录

源码参考 HEAD `9f93e94c3ab039b974c4fd9fbb33a59f7016ad4a`，测试源码及全部合同
SHA256、运行 Python/包版本和逐测试结果保存在
`evidence/w00/contract-recovery-result.json`。

```bash
.local/agri-identity-venv/bin/python -m pytest -q tests/agriculture/test_contracts.py \
  --junitxml=.local/evidence/contract-recovery/junit.xml
```

实际退出 0：**69 passed、0 failed、0 skipped**。这是本次重新运行的精确计数，
不沿用之前其他组合测试的 72 条总数。该次只跑离线契约测试，没有外部服务、数据库、
浏览器、签名验证器或 live 数据调用；这些结果不能替代 DT012/DT014/DT017 等任务证据。

原始本地输出保留在 `.local/evidence/contract-recovery/pytest.log` 与 JUnit；提交的
JSON 只含命令、退出码、版本、公开源码摘要和固定测试名/状态，不包含凭据或临时业务值。
