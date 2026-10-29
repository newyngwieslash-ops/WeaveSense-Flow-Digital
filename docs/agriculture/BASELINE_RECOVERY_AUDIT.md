# W00 恢复后增量源码审计

审计日期：2026-10-29。本文是**重新执行的只读源码审计**，不是丢失的原 W00
文档逐字恢复，也不是新一轮全量运行验收。覆盖 DT-001、DT-002、DT-004；不修改
任务历史状态。机器证据见同目录 `BASELINE_RECOVERY_MANIFEST.json`。

## 1. 工作区、历史与可重建边界

- 当前独立目录 `WeaveSense-DigitalTwin-Recovery-20261029`，分支
  `recovery/agriculture-20261029`；审计冻结参考 HEAD
  `06b78298c52348d07fafe97751526f59dc06b151`。
- `origin` 为用户指定目标 `https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital.git`。
  本审计没有推送、fetch、远端 HEAD 查询或操作旧 Collector；不将本地 HEAD 当作已发布。
- Flow 固定基线 `206c82af7ae007f45da4c582f373fbabf027f1b8` 在当前 Git 历史中，
  `git merge-base --is-ancestor <Flow SHA> HEAD` 退出 0。保留原主干历史，不是无历史复制。
- AgriScope 固定基线 `571a2dcd9abf8b8a22b66c750ee710b61d482051` 的完整 Git
  来源副本在相邻 `digitaltwin-recovery-inputs/AgriScope`，HEAD 精确匹配。
  主仓 `services/agri-gis` 是受控文件导入，**主仓尚无此 Agri commit 对象**；
  不声称主仓已合并其历史。保留独立来源副本或从该固定提交重建来源，不能只备份主仓
  后宣称已携带两套完整 Git 历史。
- 以 `git ls-tree -rz` 列出固定提交 blob，逐文件比较实际工作树 SHA256。
  manifest 分列相同、修改、缺失文件；修改包括农业集成和依赖安全修补，非静默升级。
  此检查识别差异，不等于逐条修改的业务验收。新增农业代码不属于原上游文件相同性检查。
  实际结果：Flow 5,441 文件中 5,416 相同、25 修改、0 缺失；AgriScope 578 文件中
  556 相同、22 修改、0 缺失。全部修改路径及前后摘要列入 manifest，不宣称整个来源树未变。
- 根 `AGENTS.md`、MIT `LICENSE`、`NOTICE`、`THIRD_PARTY_UPSTREAMS.md`，以及
  AgriScope `AGENTS.md`、Apache `LICENSE`、`NOTICE` 均保留；哈希见 manifest。
  Loom 自有许可证也保留。源数据附带 attribution 不是客户商业使用授权。
- 本次未查询两个上游当前远端 HEAD，故无法声称“与当前 main 无差异”。采用固定基线，
  后续升级必须新增差异和兼容性记录，不沿用这次检查结论。
- `SPEC_ROOT=specs/agriculture/v1.0.0`。PRD、requirements、规格 AGENTS、原
  SOURCE_AUDIT 与恢复输入原件逐字相同；实施 DECISIONS 含明确恢复后追加 ADR，
  不是原文被替换。原规格的网页引用审查不冒充本次重新在线验证。

重建顺序与原 CI 156 项覆盖证据见 `RECOVERY_20261029.md`；重建产物、已恢复字节、
重新实现候选、后续新提交分别记录。不要把原旧本地提交号重新赋给恢复分支。

## 2. 实际代码复用及缺口

固定源码链接基址：
[Flow 206c82](https://github.com/newyngwieslash-ops/WeaveSense-Flow/tree/206c82af7ae007f45da4c582f373fbabf027f1b8)、
[AgriScope 571a2](https://github.com/autool/AgriScope/tree/571a2dcd9abf8b8a22b66c750ee710b61d482051)。
下表路径对应这些固定来源；新增农业路径对应本地审计 HEAD，不虚构上游文件链接。

| 范围 | 实际实现与复用路径 | 当前产品集成/仍需实现 |
|---|---|---|
| 平台身份 | `backend/internal/auth`、`backend/internal/handler`；农业 `routes.go/service.go` 复用可信平台会话 | 已有真实会话/组织实现，不另建 GIS 登录；DT-015 完整攻击面验收仍待做 |
| Agent | `backend/internal/flow/runchain.go` 的 Scope/Context/Tools/Executor/Usage 五接口；`backend/agent-runtime/pi`、`internal/app` 组合链 | 不能只改提示词当农业 Agent；农业 subject、长任务引用、工具审批和计量闭环仍需后续包 |
| MCP | `backend/internal/flow/mcp.go`、`mcp_routes.go`、`mcp_secrets.go`：批准工具集、schema hash、服务端凭据、预算与未知结果 | 可复用，不表示农业工具已注册或真实外部调用通过；无新外部服务调用 |
| 预算 | `backend/internal/flow/billing.go`：预留、子分配、结算、未知结果状态 | 农业价格版本/计算费用/取消竞态/实际收费验收仍需接入，不能直接改余额补账 |
| Loom | `services/loom-engine/src/lib/db.ts`、`src/lib/server/cron.ts`、`migrations`；平台 `flow/command_outbox.go` | 保留 Node/Pi/Loom，不宣称纯 Go。农业知识授权、范围检索、真实来源链未验收 |
| 数据库 | `backend/internal/database/flow_rls.go` 仍将空 `app.tenant_ids` 解释为 system；教育/原 Flow 表共用策略 | DT-017 必须先盘点真实系统任务再迁移；新增农业策略不是全量旧表已默认拒绝的证明 |
| GIS 旧身份 | 原 `frontend/src/store/userStore.ts` 读 localStorage、is_default/首用户；`project_user_service.py` 原编码能力链 | DT-013 已阻断生产旧身份路径；`agriculture/actors.py`、`actor_session.py` 和 `operation_authorization.py` 是可信绑定/操作与同事务审计边界，非默认角色恢复 |
| 空间读取 | `backend/app/dao/plot_dao.py::get_by_bbox` 原查询没有租户参数 | 旧生产路由当前未开放；W03 实际领域映射与 DT-017 全面空间隔离尚缺，不因关闭入口算完整领域实现 |
| 地块编辑 | `backend/app/services/workbench_service.py` 与 `dao/workbench_dao.py` 原编辑/版本/历史流程 | 已有真实 PostGIS fixture 适配与回滚证据，但 stable parcel→legacy plot 的测试映射不能当生产映射；农场/边界 revision/种植季待 W03 |
| 栅格 | `growth_monitoring_engine.py` 真实 Rasterio/窗口/对齐/差值算子；`growth_monitoring_service.py` 仍有 `asyncio.to_thread` | 不冒充持久 worker。质量掩膜、insufficient_data、租约/fencing/发布恢复需后续包 |
| 模型/三维 | `monitoring_network_service.py::create_assessment` 登记外部结果；`CesiumViewer.vue` 固定 extrudedHeight | 不是模型推理或真实仿真；禁用的演示/外部数据不作为 live 证明 |
| React/Vue | `web/vite.agriculture.config.ts` 独立农业 entry；`services/agri-gis/frontend/vite.agriculture.config.ts` 同域 session-only entry | 当前 Vue 明确 `business_api: not_implemented`；保留旧源码不把旧不可信业务重新挂到生产 |
| 服务身份 | `backend/internal/agriculture/delegation.go`、`service_identity_transport.go`；GIS `agriculture/identity` | 已有真实 Go→Python→Go fixture/签名/撤销验证；生产 ownership provider 缺失默认关闭，不是 live GIS 服务全部完成 |

## 3. DT-004 规则、profile 与变更追溯

根仓库 AGENTS 与导入 AgriScope AGENTS 保持原文；农业补充规则位于 SPEC_ROOT，
适用层级是环境/当前用户指令优先，再按文件作用域保留原规则。具体冲突处理：

1. 根目录原影视定位不被农业名直接覆盖。农业 entry/profile/同域 `/agri/` 独立，
   原 React 影视/数学、Loom 与 Vue 旧业务源码保留；构建通过不是全旧业务回归通过。
2. 原“默认不自动测试”不阻止本任务明确要求的实际验证；仍按风险范围执行，报告真实命令。
3. 上游旧 GIS 编码身份与新可信身份冲突时默认拒绝，不以兼容演示为由恢复角色选择权限。
4. tldraw 仅保留原非农业路径；农业插件检查可达模块和依赖图，不是只隐藏菜单。
   最新初始依赖证据见 `dependencies/README.md`，非最终商业许可证结论。
5. 根规则要求手工编辑用 apply_patch；状态以 `tasks.json` 为任务源并同步
   `DEVELOPMENT_CHECKLIST.md`、`docs/implementation/STATUS.md`，验收矩阵独立，不自动随 done 通过。

已存在的实际样例：`docs/DECISIONS.md` ADR-028R → DT-013 →
AG-FR-002/004/006/054、AG-NFR-001 → `docs/implementation/evidence/DT-013/final-report.json`。
该样例说明可信旧身份迁移及恢复证据边界，不删除需求或放宽验收。后续规格变更需追加
原因、范围、FR/NFR/任务/验收影响、迁移/测试/回滚，外部付费/部署/发送仍需具体授权。

本次执行 `python scripts/agriculture/spec_status.py --check` 退出 0，明确警告历史
DT-001/002/004–012 的恢复证据待补验；`python scripts/agriculture/spec_status.test.py`
9 测试通过。检查覆盖需求哈希、144 任务 DAG、状态同步及负例；这是追溯检查，不是产品验收。
本审计没有用 `--sync` 修改任何任务或规格，也不声称 `--require-evidence` 已通过。

## 4. 证据层级与停止承诺

- **本次新检查**：本地来源 blob/工作树差异、祖先关系、许可证/规则保留、关键源码阅读、
  规格状态结构测试。机器摘要只支持本次观察，不恢复丢失的旧命令输出。
- **恢复后实际运行证据，非本次重跑**：DT-013
  `docs/agriculture/evidence/w01/native-pg-20261029/final-report.json`，123 passed / 0 skipped、
  6 项必需 PG 测试；配套 provenance 固定源码。DT-003 `dependencies/recovery-validation.json`
  的 4 项审计负例、22 项来源测试与真实可达产物摘要。
- **进行中**：DT-014 的真实 Nginx/Go HTTP 50 检查和导航单测/构建有证据；真实浏览器
  深链接/跨应用回跳仍受环境限制，容器启动未验收。见 `SAME_ORIGIN_GATEWAY.md` 与
  `NATIVE_GATEWAY_TESTING.md`。本次不把 HTTP 等价成浏览器通过。
- **尚未达成**：DT-015 完整安全负例、DT-017 旧系统角色/RLS 改造、W03 真实农业领域、
  后续持久遥感/外业/报告/农业 Agent/收费/真实试点。无真实外部数据、支付或设备验收。
- 历史 DT-001–012 done 与当前恢复核验分开；W00 本次部分产物恢复不等于 G0 全通过，
  更不等于完整 PRD 或可商用。后续以父任务记录、确切源码与实际执行结果推进。
