# DT017 首轮 CI 原始证据

测试 scaffold `3f6f97b9e9ce69aab5bcd360d8b839e61890852d` 对应冻结产品源码
`332c4ba1486d91d6815a2f1b9e8196796ea57e0d`。5 个实际触发 workflow 已到终态，
**整个批次未通过**；未重跑、未改远程或任务状态。

- identity 失败：`legacy-identity-inventory.py --check` 在初始化阶段报告
  `inventory drift: review source and explicitly refresh snapshot`，退出 1。
  这是官方 job 日志中的固定安全诊断，不是根据文件缺失猜测。
  pre_domain_component、post_domain_current 七项 PG、最后 Go 签名链与签名浏览器链
  **均未执行**。不能将其写作数据库测试失败或通过。
- session 44 项通过；organization 71 项通过；membership PostgreSQL 与 legacy 回归通过。
- gateway 真实浏览器 19 项通过；独立 Docker 实际构建及 16 项静态/拒绝路径检查通过。
  Docker 原件保留构建进度、镜像 ID、日志摘要；不是仅依据 workflow step 绿色。
  其中平台故意未启动返回 502，不代表完整生产应用部署。
- W00 contracts 101 项通过；core 与 migrations 成功，平台 186 表、Loom 28 表迁移重放。
  W00 没有 artifact ZIP，reconciliation 仅保留官方日志固定安全成功行。

5 个 artifact ZIP 均核对官方 SHA256、CRC、解包路径与大小；14 份脱敏 JSON 原字节
保留。5 份 provenance 各 246 项 SHA256 与 `git show 332c4ba:<path>` 全部匹配，
未与正在变化的工作树混淆。原 source_commit、计数与失败信息没有改写。
详细 run/job/步骤及 ZIP 摘要见 `reconciliation.json`。
