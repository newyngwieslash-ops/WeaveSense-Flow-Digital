# WeaveSense-DigitalTwin：开发交付规格入口

版本：1.0.0  
制定日期：2026-10-29，Asia/Shanghai  
适用对象：产品负责人、技术负责人、开发 Agent、GIS/遥感工程师、测试与试点团队  
当前状态：**规格已编制；应用尚未实施，全部开发任务为 not_started，应用验收为 not_run。**

## 1 这套文件解决什么问题

本规格把 WeaveSense-Flow 平台能力与 AgriScope 农业 GIS/遥感能力组合成一个可实施的商业产品方案。目标产品暂名 **WeaveSense-DigitalTwin**；这不是已经创建的远程仓库或已经上线的产品。

首个商用版本覆盖 S0—S3：统一身份与租户隔离、农场/地块/种植季、农业地图工作台、真实影像分析、移动巡查、审核报告、农业 Agent、计量收费、运维和真实客户试点。S4 持续监测和 S5 预测决策保留完整开发任务，须在相应数据、设备、模型验证条件满足后交付。

两项上游不是直接拼装即可商用。必须完成安全边界、农业数据模型、持久任务、数据质量、计费与真实场景验收，见[来源审计](docs/SOURCE_AUDIT.md)。本包只说明应做什么与如何验证，不宣称应用已通过验证。

## 2 四份主文件

| 文件 | 用途 | 建议阅读者 |
| --- | --- | --- |
| [PRD.md](PRD.md) | 产品目标、用户角色、范围、80 项功能需求、20 项非功能要求、发布门槛 | 产品负责人、全体实施者 |
| [DEVELOPMENT_PLAN.md](DEVELOPMENT_PLAN.md) | 源码集成、环境、架构实施、阶段、团队与交付方案 | 技术负责人、开发 Agent |
| [DEVELOPMENT_CHECKLIST.md](DEVELOPMENT_CHECKLIST.md) | 18 个工作包、144 项有依赖/产物/验收/验证说明的任务 | 开发与测试 |
| [AGENT_MASTER_PROMPT.md](AGENT_MASTER_PROMPT.md) | 可整段复制的首次开发提示词 | 向开发 Agent 下达任务的人 |

任务的机器可读版本为 [tasks.json](tasks.json)。需求的机器可读版本为 [requirements.json](requirements.json)。这些文件应一起交给开发 Agent，不能只复制主提示词而省略规格。

## 3 第一次交给开发 Agent：三步即可

1. **放入完整规格。** 已有代码项目时，将整个目录放到项目的 `specs/agriculture/v1.0.0/`；这一目录称为 `SPEC_ROOT`。尚无项目时，把整个规格目录放到可读工作区，让 Agent 先检查环境，再建立独立本地项目。无需先创建新的 GitHub 远程。
2. **提交主提示词。** 打开 [AGENT_MASTER_PROMPT.md](AGENT_MASTER_PROMPT.md)，复制分隔线以下的完整内容。可在前面加一句实际规格路径，例如：“规格在 /实际路径/specs/agriculture/v1.0.0，请按以下要求实施。”路径必须替换成当前工作区真实位置。
3. **按工作包持续执行。** Agent 从 W00 审计开始，按依赖推进，并更新状态、测试与证据。代码开发可持续进行；真实账户、影像、模型凭据、收费渠道或设备缺失时，只将对应项标记为 blocked_external，其余继续。

**不要把规格目录的 AGENTS.md 直接复制覆盖代码仓库现有 AGENTS.md。** 先保留已有规则，由 Agent 将本规格中的工程约束作为补充读取或合并。也不要将本包 PRD 覆盖上游自己的产品文档。

若用户现有项目带有未提交改动，应采用独立工作树、分支或新目录，保留原改动。此规格授权的是实施方向，不自动授权公开部署、付费采购、向外发消息、真实设备控制或推送原上游。

## 4 全部配套文档

| 路径 | 内容 |
| --- | --- |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | 服务边界、数据库所有权、身份、任务、产物发布、计费、部署拓扑 |
| [docs/DATA_API_CONTRACTS.md](docs/DATA_API_CONTRACTS.md) | 核心实体、API、错误、幂等、事件、Agent 工具、地图联动 |
| [docs/SECURITY_TENANCY.md](docs/SECURITY_TENANCY.md) | 角色能力矩阵、租户全链路隔离、上传与数据访问、密钥和 Agent 边界 |
| [docs/ACCEPTANCE_MATRIX.md](docs/ACCEPTANCE_MATRIX.md) | 80 项功能验收、20 项 NFR 交叉映射、G0—G5 发布门槛及真实证据规则 |
| [docs/SOURCE_AUDIT.md](docs/SOURCE_AUDIT.md) | 固定提交、上游事实/局限、官方规范及许可来源 |
| [docs/DECISIONS.md](docs/DECISIONS.md) | 已采纳架构决定、默认选择及重新评估条件 |
| [AGENTS.md](AGENTS.md) | 开发 Agent 的补充工程约束模板 |
| [docs/AGENT_CONTINUATION_PROMPTS.md](docs/AGENT_CONTINUATION_PROMPTS.md) | 续跑、解除阻塞、发布审查、上游升级和后续阶段的完整提示词 |
| [docs/implementation/STATUS.md](docs/implementation/STATUS.md) | 当前实施状态、外部条件、下一项任务与恢复点 |
| [docs/implementation/WORK_PACKAGE_TEMPLATE.md](docs/implementation/WORK_PACKAGE_TEMPLATE.md) | 工作包实施记录与证据模板 |
| [VALIDATION_REPORT.md](VALIDATION_REPORT.md) | 本规格自身的结构、一致性和打包检查；不是应用测试报告 |
| [MANIFEST.json](MANIFEST.json) | 包内文件清单、字节数及 SHA-256，便于检查传输完整性 |

## 5 核心工程选择

- WeaveSense-Flow 是平台主干：Go 平台统一管理身份、租户、Agent、审批、预算、套餐和支付。既有 Node/Pi/Loom 依赖仍需纳入运行和部署。
- AgriScope 按固定提交受控导入为农业 GIS 服务；Python/FastAPI、PostGIS、Rasterio/GDAL 保留专业分工。
- 首轮采用 React 平台壳 + 同域 Vue 农业子应用；地图优先 OpenLayers，Cesium 按需要启用。前后端都要统一授权，不能以页面嵌入代替集成。
- 平台、知识与农业数据库各自有明确写入所有权，跨服务用 API 与事件，不跨服务直接写表。
- 使用持久任务、租约、fencing、outbox/inbox、幂等产物和费用结算；不能用浏览器长连接或进程内线程冒充可靠作业系统。
- 首版提供有证据的监测、人工复核和建议；预测能力另经验证，真实设备控制保持关闭。
- 农业生产 profile 默认移除不需要的 tldraw 使用路径及依赖；若确实使用，应先满足其独立许可。来源与数据授权逐项核实。

## 6 固定上游基线

| 项目 | 仓库 | 本规格审查提交 |
| --- | --- | --- |
| WeaveSense-Flow | https://github.com/newyngwieslash-ops/WeaveSense-Flow | `206c82af7ae007f45da4c582f373fbabf027f1b8` |
| AgriScope | https://github.com/autool/AgriScope | `571a2dcd9abf8b8a22b66c750ee710b61d482051` |

后续使用不同提交时，先记录差异和重新验证范围。静态源码审查不能证明部署环境、安全、真实数据质量或运行性能已经合格。

## 7 如何判断真正完成

需要区分五种状态：代码实现、确定性 fixture 验证、真实依赖验证、客户试点可用、商用可发布。不得把界面可点开或 mock 返回成功等同于后四种状态。

工作包完成应提供代码/迁移路径、实际命令、退出码、结果与证据；功能验收必须映射对应需求。G3 首次商用门槛包括真实农业闭环、租户隔离负例、选定收费模式验证、数据权利、恢复演练和运维交接。B2B 人工对公收款必须由用户明确选择，有真实到账和独立复核；未验证的在线支付不能面向客户启用。

本文档中的性能、恢复与容量数值是待验证目标，不是测量结论或已承诺 SLA。实际工期取决于 W00 基线、团队、数据与外部条件，开发方案中的估算不可当作自动完成承诺。

## 8 续跑与故障恢复

每次结束开发会话前，由 Agent 更新 `SPEC_ROOT/docs/implementation/STATUS.md`。下次提交[续跑提示词](docs/AGENT_CONTINUATION_PROMPTS.md)，从记录的 Git 提交、任务状态及下一依赖就绪任务恢复。

任务状态由 `tasks.json` 保存结构化信息，Markdown 清单保持同步。发现规格歧义或确有必要调整时，记录 ADR、受影响需求/任务/验收与迁移影响，不能悄悄缩减范围或将阻塞项标成完成。

## 9 下载与复制建议

完整 ZIP 是正式交付单元，包含四份主文件、机器可读清单和全部配套文档。单独下载的主文件便于阅读和复制，但不是完整上下文。若 ZIP 下载异常，可先使用合订本阅读全部 Markdown，再用单独主提示词；正式开发仍应保留本目录的文件结构及 JSON 清单。

本包不包含第三方源码副本、账号、密钥、客户数据、可执行安装包或已经上线的服务。

