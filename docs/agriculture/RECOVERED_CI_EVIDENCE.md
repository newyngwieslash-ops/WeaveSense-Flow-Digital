# 历史 CI 证据恢复与边界

本次仅通过 GitHub 只读接口重新取得既有工作流状态、日志中的固定安全成功标记和原 sanitized artifact，不重新运行工作流，不修改远程，不把历史结果冒充当前源码验收。

机器记录：`evidence/recovery-ci-20261029.json`。记录精确 run/job/commit、原 artifact ZIP 的 GitHub SHA256、原 JSON 文件摘要，以及逐文件历史/当前源码比较。

## W00 / DT-005

| Run | 源码 SHA | 实际终态 |
| --- | --- | --- |
| 40812303987 | `7ff2cc4fc16a349e72e4e486a427f44fa487be18` | contracts、compose(core)、compose(migrations) 全部 success |
| 40839895019 | `c20fa403468951beb2c898a05fb1062337178c33` | 同上全部 success |

两次 artifact API 都返回空数组，**不存在可恢复的 core/migrations ZIP**。本次从原 job 日志仅提取精确匹配的带时间戳 `PASS:` 行，未保存原始日志、数据库 URL 或密码。两次均有真实六角色密码连接、跨数据库拒绝、运行角色 DDL 拒绝、PostGIS 与有限 readiness 成功标记；平台迁移应用并重放后 186 张表，Loom 迁移应用并重放后 28 张表。

必须注意原 workflow 在 scaffold 模式读取固定 Flow `206c82a...`，只覆盖部署目录、Compose 与 `ci-smoke.sh`。因此这些迁移不是后来农业 Go migration 82/83 的验收（后者由 membership 专项验证）。启动的是 postgres、redis 与 geo-readiness；平台和 Loom 执行迁移命令，**没有启动它们的业务服务**。同样不代表完整 GIS 迁移、租户 RLS、真实数据或产品验收。机器记录保留实际部署源码历史摘要和当前差异。

## DT-009 至 DT-011 专项原件

全部来源于 `c20fa403...`，ZIP 完整性、文件路径安全及 GitHub archive digest 已校验；解包后保留原 JSON 字节，不重写其中的历史提交信息。

| 目录（位于 evidence/w01） | 原 artifact ID | 实际结果 |
| --- | --- | --- |
| `ci-40839895134-session` | 12881639530 | 实际浏览器会话 44 条检查 pass |
| `ci-40839895150-organization` | 12882457008 | 实际组织浏览器 62 条检查 pass |
| `ci-40839895150-membership` | 12881799616 | PostgreSQL 5 个主测试及 reciprocal_grants 子测试通过，legacy Flow 21 项通过，无跳过/失败 |

三个 provenance 各含 156 个 overlay 文件；其 SHA256 均与本地保存的原 `c20fa403...` Git 对象完全一致。恢复检查时与当前工作树有 16 个路径不同、0 缺失，逐路径列于机器记录。已有后续 DT-013/DT-014 变更，不能用上述历史绿色替代当前代码的回归。

原浏览器工作流使用当时的测试代理，不是新 DT-014 nginx 浏览器闭环证明。当前原生 nginx HTTP 证据及 Chromium 环境阻塞记录应独立阅读。

## DT-012 与后续本地 PostgreSQL

`evidence/w01/ci-40839895250-identity/` 原件已在此前恢复，本次未重复下载。`native-pg-20261029/final-report.json` 是恢复后另一轮实际 PostgreSQL/PostGIS 123 项测试，不是上述 Docker/平台/Loom 运行的替代或重写。

此文和机器记录只补齐证据与可追溯性，不修改任务状态、验收项或任何未完成能力。
