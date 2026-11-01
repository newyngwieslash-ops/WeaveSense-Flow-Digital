# WeaveSense-DigitalTwin 实施状态

更新日期：2026-11-01（Asia/Shanghai）
规格版本：1.0.0  
当前状态：recovery_evidence_verified_implementation_in_progress

应用实现状态：in_progress

当前工作包：W03 准备开始；DT-001 至 DT-024 已按各自原范围完成，共 24/144 项；W02 当前开放路径隔离基线完成，不等于完整G0或产品商用验收。

下一任务：DT-025 农场档案和区域配置；依赖已齐备，按Go可信授权/持久派发、GIS事实/面积与Vue工作台互不冲突路径并行推进。

DT-024 已完成：产品冻结 `71021e9f`、远端 CI scaffold `aa60e1a6`。真实 identity run `41278325667` 隔离79项全过，Redis完整启动、组件模式关闭、race开启；独立复核398源哈希、Go精确集合180/4/6、21路由158请求快照、GIS9路由及原有回归无缺项/跳过。gateway run `41278325702` 首次Vue容器构建超时，保留失败原件；同源仅重试失败job，attempt2实际容器16检查/浏览器19断言通过，未改限额/断言。六条CI全部成功，详见 `evidence/DT-024/README.md`。首次超时根因未证实，不冒称修复了代码错误；完整产品历史并未因CI scaffold发布而同步。

DT-023 最终验收：CI `41276134632`（远端 `0abff504`、产品 `e50ec16d`）三项真实 Docker 全部通过、退出0、零跳过；独立核验24 parser输入与388 overlay一致。本地修复后77项组件通过，34源哈希对应同一冻结源。根因是缺少 `libexpat.so.1`，只补固定 Expat 运行依赖和非 root 构建 import，不调整隔离/限额/断言。证据见 `evidence/DT-023/ci-0abff504/`；这是安全组件任务完成，不是生产上传、真实外部来源或W06业务完成。

新增回归：`0040b606` identity CI `41274681557` 成功，382 overlay 哈希映射 `ad4995c6`；JSON 299 pass 含 XML 258 项，不能重复相加，零失败/跳过。`d77a5c36` W00 CI `41275361980` 已修复 Loom 源码装配，平台 schema 85/85 ready、190 表，Loom 28 表迁移/重放成功，388 overlay 精确映射 `2cfe8d8c`。证据保留于 `evidence/historical-ci/`，不是生产部署、DT024 全路径或真实农场验收。

DT-023 历史失败保留：首轮 `41274681614`（0040/ad499）以及诊断轮 `41275361944`（d77/2cfe）、`41275760183`（8de/403）均为2 pass/1 not_pass，原报告和源映射分别保留，不回写为成功。安全诊断仅白名单阶段/异常类/代码行/库basename；测试探针不替换原生产路径结果。此前冻结 `92f2146e` 组件及独立审查证据同样保留，见 `evidence/DT-023/README.md`。

DT-021 已完成：冻结 `bbc650eb` 的严格执行证据包含农业范围缓存/事件流20项、旧业务撤权事件流15项和缓存1项，全部通过且无缺失/跳过；1171源码哈希独立核对一致。前端可信权限快照缓存键18项测试及实际农业构建、旧业务完整app/handler回归和逐事件撤权竞态另有冻结证据。修复真实Logout发生于Flush/Write之后仍发送下一敏感帧的问题，保留原失败记录。证据见 `evidence/DT-021/`。这证明本地真实HTTP/会话及SQLite fixture组件范围，不冒称远程CI、此次真实浏览器、PG RLS、生产农业检索/地图provider或W06队列；完整AG-AC/W02/G0仍未通过。

DT-020 已完成：冻结 `c8106dfc` 的实际平台会话→Go授权/签名→Python验证→PG对象清单/独立权属→私有文件链，47项通过、零失败跳过、退出及集群关闭0；1464源码哈希精确映射，独立审查通过。修复了查询元数据前scope校验及宿主时区签发问题，未放宽验签。私有对象按版本、单Range/HEAD、撤回及no-store策略统一控制；不是S3、大型COG、瓦片生成、生产领域provider或资产生命周期验收。见 `evidence/DT-020/`，历史中途helper变更报告另列，不当作冻结证据。

DT-022 已完成：冻结 `bc07d1c7` 的真实Go→Python→PG侧表/lease fixture→反向当前授权链7项通过、零失败跳过、退出及关闭0；1459源码哈希精确一致，独立审查通过。不可变主体/范围/输入快照、重试fencing、撤权/撤回及PG锁等待跨有效期回滚已有实证。不是W06队列或真实结果原子发布；生产缺执行provider保持503，未来发布仍需事务内fencing/CAS。见 `evidence/DT-022/`。上述两项是本地执行，不冒称远程CI、生产或完整AG-AC/W02/G0通过。

DT-018 已完成：冻结 `b7be7c44` 的农业/旧 Flow 两套独立 fresh PostgreSQL 初始化均完成全迁移、重放、校验及 8 根 + 25 子测试，零跳过、1159 源码哈希开始/结束一致；Loom 13 项必需原生测试、67 源码映射及旧业务回归独立通过。平台普通空范围不再获得 system 权限，受限后台扫描、预算/provider 同事务、数学非平台成员角色/签名文件/邀请入班与 105 学校分页取消重试均有实际证据。完整 app 最终 228.963s 通过，先前失败通过真实 actor/metadata fixture 及恢复锁定 Pi 依赖解决，未降低断言。证据 `evidence/DT-018/`，独立审查通过；保留旧 Loom 全依赖图 tsc 失败、旧 math Sync/部分同步 REST 网络事务未全面改造的边界，不声称生产部署、远程 CI 或全部权限验收完成。

DT-019 已完成：冻结源码 `26343a0c` 的三个独立 fresh PostgreSQL 阶段分别为查询 17、旧组件回归 152、正式迁移 6 项通过，均零失败/跳过，退出及集群关闭为 0。主实现和独立审查分别核对 299 项报告源码并精确映射冻结提交；查询清单包含 215 个应用源文件、412 个数据库执行函数。原 DAO 同 bbox 列表/详情、聚合、历史/关联与导出源行受到实际数据库资源策略约束，嵌套 SQL 重绑与缺资源项目投影漏洞已修复且实际验证。证据见 `evidence/DT-019/`，保留三份真实早期失败；84 个未适配业务仍关闭，不代表 W03 领域 CRUD、真实农场、远程 CI 或新签名浏览器整链已通过，也不将完整 AG-AC/W02/G0 置为完成。

DT-016 新整链验收完成：冻结产品 `d43899d2`、scaffold `56c1f7c8` 的三个实际触发 workflow 全部成功；235 项源码映射一致。真实浏览器登录→Go 授权→GIS 验签/归属/当前权限→原 Workbench 写入及稳定主体审计，主测试和四个必需子项全部通过，无跳过。浏览器 15 项检查、42 请求零违规；两个租户三个工作区，跨范围/伪造主体/撤权后同 cookie 写入均拒绝且无领域副作用，16 个农业公开入口匿名均为 401。证据见 `evidence/DT-016/` 和 `docs/agriculture/IDENTITY_CHAIN_ACCEPTANCE.md`。首轮登录装配失败保留，不回写成功；旧批次单独运行的 Docker 结果不并入本轮三 workflow 结论。W01 任务闭环完成不等于 G0；测试专用写桥不代表生产农场 UI，旧下载/预览/审核未开放，W02 全模型 RLS 和空范围系统策略仍待实现。

DT-017 已按原任务验收完成：冻结 `2e3d43a6` 的实际本地 PostgreSQL 17.11/PostGIS 3.5.2 组件 152 项及独立新集群正式迁移 CLI 6 项均通过，零失败/跳过/遗漏，退出和集群关闭为 0；292/282 源码哈希独立核实。93 旧领域表、4 身份表和显式地块归属表共 98 表 FORCE RLS，范围、连接池复用、受限角色及正式迁移漂移/回滚已有真实证据。84 旧表默认关闭，不宣称领域功能全部可用；worker 没有默认业务 CRUD。证据见 `evidence/DT-017/` 和 `docs/agriculture/DATABASE_BOUNDARY_ACCEPTANCE.md`。新 schema 远程签名浏览器回归仍未运行，不将本地证据冒充 CI；该独立回归不改变本任务原文 PG 验收已完成的事实，也不代表 W02/G0 或完整 AG-AC-005/006 通过。

## 1 基线

- Flow：206c82af7ae007f45da4c582f373fbabf027f1b8。
- AgriScope：571a2dcd9abf8b8a22b66c750ee710b61d482051。
- 目标远程：https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital；最新已复核 CI scaffold 为 `aa60e1a6`（产品源码 `71021e9f`），六条CI最终全部通过，早期失败与本次gateway首次超时原件仍保留。完整本地产品历史与 scaffold 区分，不声称产品分支已同步。
- 当前恢复目录：`/workspace/scratch/8cdfee496491/digitaltwin-recovery-20261101/WeaveSense-DigitalTwin`；实际分支/HEAD 以恢复检查点记录为准。
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
| done | 24 |
| not_started | 120 |

历史 done、恢复证据待补验：无。
依赖状态就绪但仍须检查恢复条件：DT-025。

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
| DT-018 | done |
| DT-019 | done |
| DT-020 | done |
| DT-021 | done |
| DT-022 | done |
| DT-023 | done |
| DT-024 | done |
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
