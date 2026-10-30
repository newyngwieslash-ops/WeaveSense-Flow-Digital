# WeaveSense-DigitalTwin 实施状态

更新日期：2026-10-30（Asia/Shanghai）
规格版本：1.0.0  
当前状态：recovery_evidence_verified_implementation_in_progress

应用实现状态：in_progress

当前工作包：W02；DT-017 已以真实本地 PostgreSQL 完成数据库缺省拒绝验收，推进平台空范围迁移与 GIS 查询范围

下一任务：依赖就绪的 DT-018 Flow/Loom 空范围策略兼容迁移及 DT-019 GIS 查询范围；远程新 schema 签名浏览器回归仍单独等待审批/执行。

DT-016 新整链验收完成：冻结产品 `d43899d2`、scaffold `56c1f7c8` 的三个实际触发 workflow 全部成功；235 项源码映射一致。真实浏览器登录→Go 授权→GIS 验签/归属/当前权限→原 Workbench 写入及稳定主体审计，主测试和四个必需子项全部通过，无跳过。浏览器 15 项检查、42 请求零违规；两个租户三个工作区，跨范围/伪造主体/撤权后同 cookie 写入均拒绝且无领域副作用，16 个农业公开入口匿名均为 401。证据见 `evidence/DT-016/` 和 `docs/agriculture/IDENTITY_CHAIN_ACCEPTANCE.md`。首轮登录装配失败保留，不回写成功；旧批次单独运行的 Docker 结果不并入本轮三 workflow 结论。W01 任务闭环完成不等于 G0；测试专用写桥不代表生产农场 UI，旧下载/预览/审核未开放，W02 全模型 RLS 和空范围系统策略仍待实现。

DT-017 已按原任务验收完成：冻结 `2e3d43a6` 的实际本地 PostgreSQL 17.11/PostGIS 3.5.2 组件 152 项及独立新集群正式迁移 CLI 6 项均通过，零失败/跳过/遗漏，退出和集群关闭为 0；292/282 源码哈希独立核实。93 旧领域表、4 身份表和显式地块归属表共 98 表 FORCE RLS，范围、连接池复用、受限角色及正式迁移漂移/回滚已有真实证据。84 旧表默认关闭，不宣称领域功能全部可用；worker 没有默认业务 CRUD。证据见 `evidence/DT-017/` 和 `docs/agriculture/DATABASE_BOUNDARY_ACCEPTANCE.md`。新 schema 远程签名浏览器回归仍未运行，不将本地证据冒充 CI；该独立回归不改变本任务原文 PG 验收已完成的事实，也不代表 W02/G0 或完整 AG-AC-005/006 通过。

## 1 基线

- Flow：206c82af7ae007f45da4c582f373fbabf027f1b8。
- AgriScope：571a2dcd9abf8b8a22b66c750ee710b61d482051。
- 目标远程：https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital；最新已验 CI scaffold 为 56c1f7c8，映射本地冻结产品源码 d43899d2。完整本地产品历史与 scaffold 区分，不声称产品分支已同步。
- 当前恢复目录：`/workspace/scratch/8cdfee496491/WeaveSense-DigitalTwin-Recovery-20261030`；实际分支/HEAD 以恢复检查点记录为准。
- 规格位置：推荐PROJECT_ROOT/specs/agriculture/v1.0.0。

## 2 任务与验收记录边界

| 类别 | 总数 | 当前状态 |
| --- | ---: | --- |
| 功能需求 | 80 | 待实现和验证 |
| 非功能需求 | 20 | 待验证 |
| 开发任务 | 144 | 当前分布及历史恢复待补验清单见下方自动生成区 |
| 逐项验收 | 80 | 当前恢复的原始矩阵仍为 not_run；历史 AG-AC-002 running 待证据恢复，不自动改写 |
| 发布门禁 | 6 | G0-G5全部未通过 |

上游已存在代码不计作新产品已通过验收。DT-001–013 的恢复证据现已按各任务原范围核验：历史 CI 原件与冻结来源精确映射，重建的审计/许可/样本/契约分别有实际检查，新执行结果单独存档。没有声称恢复了所有原本地未发布提交的字节，也没有把旧 CI 当作后续 DT-014 工作树的回归。

DT-008 的恢复首包审查已完成：001–007 的依赖证据齐备，9 项状态测试及同步检查通过，构建、迁移、真实失败和剩余风险见 `docs/agriculture/W00_RECOVERY_ACCEPTANCE.md`。W00 完成不等于 W01/G0 通过。

DT-013 已按任务原范围完成：冻结源码提交 `91e3fd5d8bf07e02410ef5375d25fee5fdc5976a`，恢复后实际本地 PostgreSQL 17.11 / PostGIS 3.5.2 新集群验证 123 passed、0 failed、0 skipped，6 项必需真实数据库测试全部通过，退出及清理均为 0。280 项执行源码哈希已逐一匹配冻结提交；原始报告保留执行时的工作树来源，不冒充当时已经提交。证据见 `evidence/DT-013/final-report.json`。该结果不是远程 CI，不表示 W01/G0、全量旧 GIS RLS、W03 真实领域映射或商业验收完成。

DT-003 的恢复核验已通过：重新生成初始 SBOM，984 份通知原文、实际农业构建可达图与产物摘要、固定依赖及来源禁用策略均已核验；4 项 SBOM 负例及 22 项来源权利测试通过。保留最终镜像、缺通知、图标出处及原生编解码器等发布门禁，不声明所有商业许可已审结。

DT-005、DT-009–012 的历史 CI 证据映射现已核实：原 c20 provenance 的 156 项
overlay 与冻结产品祖先 `d13a09bb5ebdcaeb38b8136987ee577360fa7add` 全部匹配，
W00 的 11 项部署/工作流源码也全部匹配；原 JSON 字节与日志成功标记保留。
RECOVERY_STATUS 的 verified 在此仅表示历史执行证据恢复与冻结来源映射完成，
不表示后续 DT013/DT014 工作树重新跑过这些浏览器/Docker 测试。
原 W00 仅六角色/核心 readiness 及 Flow206/Loom 迁移重放，不含后续农业82/83或完整
应用服务器启动；当前重建 env.example 另属后续源码。详见
`docs/agriculture/RECOVERED_CI_EVIDENCE.md` 与规格证据 `historical-ci/d13-mapping.json`。
上述历史证据补齐本身不代表新网关回归；DT014 使用下述独立新运行结果。

DT-014 已按原任务范围完成：2026-10-30 实际 CI 41007114373 中真实 Nginx/Chromium 15 项检查通过、71 请求零违规，独立 Docker 构建/静态 smoke 16 项通过；211 运行源码逐项映射到冻结本地 53ddad05/856af8f3。W00 首次因旧端口断言失败，修正为精确 Web 回环白名单并补负例后，41008181499 的 contracts/core/migrations 三 job 全部通过；原失败保留。证据见 `evidence/DT-014/` 和项目 `docs/agriculture/GATEWAY_ACCEPTANCE.md`。浏览器使用 SQLite fixture，不替代真实 PostgreSQL 隔离、农场验收、DT015 或 G0；CI 组装目录不是完整产品历史已发布的证明。

恢复验证独立记录于 `RECOVERY_STATUS.json`；历史例外严格限定 DT-001–012，新增 done 必须提供当前真实证据文件、命令/退出码/结果、源码哈希和已完成依赖。`spec_status.py --require-evidence` 要求所有当前 done 的证据完整；检查通过仍只表示状态与证据一致，不冒充全部产品验收。

DT-015 已按原任务范围完成：冻结 `574d48f02e96b43c01f0cd3ee2b90a956713b5cb` 的 228 项源码与五份 CI provenance 完全匹配。最终 fd5fa31d 的五个 workflow 全部成功：真实组织浏览器 71 项、会话 44 项、同域网关 19 项及独立 Docker 16 项；PostgreSQL 六项主测试及 reciprocal 子项全部通过、无跳过。包含 CSRF/Origin 负例、降权后旧 cookie 的旧写请求 403、真实多标签失效、服务重建后的撤权检查。生产 30 秒例行重验实际观察 35038ms/347 次采样，权限未变时保留邀请码/草稿，重验后旧确认弹窗不复活，随后同邀请码确实接受成功。历史失败及修正分别保留，不回写为成功。

证据见 `evidence/DT-015/` 与项目 `docs/agriculture/BROWSER_SECURITY_ACCEPTANCE.md`。平台权限输入指纹仅供显示连续性比较，不是授权凭证，也不替代 GIS 资源归属；所有实际操作继续校验。已签服务票据最长 30 秒且检查当前权限 epoch，退出不声称追溯撤回已签票据或中断已开始的任务。浏览器使用隔离 SQLite fixture，真实 PostgreSQL 授权另有证据；不代表 W01、G0、全部 AG-AC-004 或真实农场商用验收。

新增完成记录的 `source_commit` 必须是本地权威历史中已存在且为当前 HEAD 祖先的完整 40 位提交号；`source_hashes` 针对此冻结提交读取，而不是未来工作树。后续任务合法修改源码不改写旧证据。远程连接器提交号可能不同，必须另记映射，不混作本地源码提交或 CI 通过证据。未提交源码的测试只能作为进行中记录，不能用于新增 done。

## 3 外部依赖登记

| 依赖 | 状态 | 影响 | 可继续的工作 |
| --- | --- | --- | --- |
| 真实试点农场/地块授权及种植季 | 待提供 | 现场验收、农学有效性、商业试点 | fixture、导入/几何/权限/计算实现 |
| 选定真实影像来源与网络访问 | 待开发环境验证 | live数据接入 | STAC适配器/质量门禁/小样本测试 |
| 模型服务凭据与测试预算 | 待提供或从已授权环境配置 | 真实Agent工具链 | runtime/工具Schema/fixture和契约 |
| 生产对象存储与域名 | 待指定 | live下载/恢复/部署 | 开发profile与兼容对象存储测试 |
| 收费模式/商户凭据 | 待用户选择 | 真实收费/支付渠道发布 | 幂等账本/适配器/沙箱/对账 |
| 农技审核与真实现场采集 | 待安排 | G2/G3试点结论 | 工单/报告/审核链路开发 |
| 设备与模型训练验证数据 | 后续条件 | S4/S5 | 契约/模拟负例与不启用的适配接口 |

这里登记的是外部条件，不代表当前所有任务都应标blocked。只有执行到具体依赖时，才在对应任务上记录blocked_external，其他就绪任务继续。

## 4 实施时持续维护

- 当前分支、HEAD、工作区未提交变更和已有用户修改。
- 当前任务ID、相关FR/NFR、代码/迁移/契约变更、完成状态。
- 实际测试命令、退出码、样本与环境、证据路径。
- 未完成项、风险、外部阻塞、责任人/下一动作。
- 当前阶段门禁结果与下一项依赖就绪任务。

## 5 首次执行检查点

开发Agent读取完整规格与原AGENTS，保留原工作区，验证上游SHA，建立实际源码与可运行命令清单，形成W00审计记录。然后按依赖持续实施，不在完成审计后自动停等“继续”。

<!-- generated-task-status:start -->
## 当前任务状态（从 tasks.json 生成）

历史完成状态与本次恢复验证分开记录；done 不表示恢复后重新验收通过。

| 状态 | 数量 |
| --- | ---: |
| done | 17 |
| in_progress | 2 |
| not_started | 125 |

历史 done、恢复证据待补验：无。
依赖状态就绪但仍须检查恢复条件：无。

| 任务 | 状态 |
| --- | --- |
| DT-001 | done |
| DT-002 | done |
| DT-003 | done |
| DT-004 | done |
| DT-005 | done |
| DT-006 | done |
| DT-007 | done |
| DT-008 | done |
| DT-009 | done |
| DT-010 | done |
| DT-011 | done |
| DT-012 | done |
| DT-013 | done |
| DT-014 | done |
| DT-015 | done |
| DT-016 | done |
| DT-017 | done |
| DT-018 | in_progress |
| DT-019 | in_progress |
| DT-020 | not_started |
| DT-021 | not_started |
| DT-022 | not_started |
| DT-023 | not_started |
| DT-024 | not_started |
| DT-025 | not_started |
| DT-026 | not_started |
| DT-027 | not_started |
| DT-028 | not_started |
| DT-029 | not_started |
| DT-030 | not_started |
| DT-031 | not_started |
| DT-032 | not_started |
| DT-033 | not_started |
| DT-034 | not_started |
| DT-035 | not_started |
| DT-036 | not_started |
| DT-037 | not_started |
| DT-038 | not_started |
| DT-039 | not_started |
| DT-040 | not_started |
| DT-041 | not_started |
| DT-042 | not_started |
| DT-043 | not_started |
| DT-044 | not_started |
| DT-045 | not_started |
| DT-046 | not_started |
| DT-047 | not_started |
| DT-048 | not_started |
| DT-049 | not_started |
| DT-050 | not_started |
| DT-051 | not_started |
| DT-052 | not_started |
| DT-053 | not_started |
| DT-054 | not_started |
| DT-055 | not_started |
| DT-056 | not_started |
| DT-057 | not_started |
| DT-058 | not_started |
| DT-059 | not_started |
| DT-060 | not_started |
| DT-061 | not_started |
| DT-062 | not_started |
| DT-063 | not_started |
| DT-064 | not_started |
| DT-065 | not_started |
| DT-066 | not_started |
| DT-067 | not_started |
| DT-068 | not_started |
| DT-069 | not_started |
| DT-070 | not_started |
| DT-071 | not_started |
| DT-072 | not_started |
| DT-073 | not_started |
| DT-074 | not_started |
| DT-075 | not_started |
| DT-076 | not_started |
| DT-077 | not_started |
| DT-078 | not_started |
| DT-079 | not_started |
| DT-080 | not_started |
| DT-081 | not_started |
| DT-082 | not_started |
| DT-083 | not_started |
| DT-084 | not_started |
| DT-085 | not_started |
| DT-086 | not_started |
| DT-087 | not_started |
| DT-088 | not_started |
| DT-089 | not_started |
| DT-090 | not_started |
| DT-091 | not_started |
| DT-092 | not_started |
| DT-093 | not_started |
| DT-094 | not_started |
| DT-095 | not_started |
| DT-096 | not_started |
| DT-097 | not_started |
| DT-098 | not_started |
| DT-099 | not_started |
| DT-100 | not_started |
| DT-101 | not_started |
| DT-102 | not_started |
| DT-103 | not_started |
| DT-104 | not_started |
| DT-105 | not_started |
| DT-106 | not_started |
| DT-107 | not_started |
| DT-108 | not_started |
| DT-109 | not_started |
| DT-110 | not_started |
| DT-111 | not_started |
| DT-112 | not_started |
| DT-113 | not_started |
| DT-114 | not_started |
| DT-115 | not_started |
| DT-116 | not_started |
| DT-117 | not_started |
| DT-118 | not_started |
| DT-119 | not_started |
| DT-120 | not_started |
| DT-121 | not_started |
| DT-122 | not_started |
| DT-123 | not_started |
| DT-124 | not_started |
| DT-125 | not_started |
| DT-126 | not_started |
| DT-127 | not_started |
| DT-128 | not_started |
| DT-129 | not_started |
| DT-130 | not_started |
| DT-131 | not_started |
| DT-132 | not_started |
| DT-133 | not_started |
| DT-134 | not_started |
| DT-135 | not_started |
| DT-136 | not_started |
| DT-137 | not_started |
| DT-138 | not_started |
| DT-139 | not_started |
| DT-140 | not_started |
| DT-141 | not_started |
| DT-142 | not_started |
| DT-143 | not_started |
| DT-144 | not_started |
<!-- generated-task-status:end -->
