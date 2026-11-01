# DT025 农场档案与区域配置验收

冻结产品源 `f281a8d436055343c1bdd145490d72ae646287bb`，CI scaffold `79f25d19827dfa7b10e96ae5649b263bebf044a5`。真实 identity run `41297700980`、job `124821944791`、artifact `13080466396` 成功；456 个 overlay 源哈希逐 Git 对象复核一致，浏览器报告绑定同一 provenance、runner 和 nginx 模板。

## 实现与迁移

- 平台 `backend/internal/agriculture/farm_*.go`：统一会话/CSRF、治理创建与内容权限分离、版本化预览和幂等操作、现有 Flow CommandAdapter 派发与回执、状态查询和当前授权复核。没有另造登录、授权或调度器。
- 平台数据库 migration v86 `agriculture_farm_operations_v86`：操作/事件持久化、唯一约束及不可变 intent；GIS 独立农场/修订/命令/outbox/字典迁移，所有事实仍由 GIS 所有。
- GIS `app/agriculture/farms_{config,geometry,identity,routes,store}.py`：长期身份、WGS84 轴序/来源记录、真实 PostGIS spheroid 面积、不可变修订、按版本配置的区域/时区/单位、强 scope 与 expected_revision。空字典 fail closed，无省份或项目号默认值。
- Vue `frontend/agriculture/farms/`：真实列表/创建/编辑/预览/归档/未知结果恢复、冲突草稿保留、OpenLayers 无外部底图、原统一会话生命周期。跨区域数据及 operational/demo 明确区分。
- 合同 `docs/agriculture/FARMS_CONTRACT.md`、ADR-035；部署配置模板和 `deploy/agriculture/FARM_DICTIONARY.md` 明确可信农业入口及配置方式，不把旧 GIS 入口或 readiness 当成业务服务。

## 实际执行与结果

`.github/workflows/agriculture-identity.yml` 在 Ubuntu 24.04 执行 `scripts/agriculture/identity-postgres-ci.sh`，最终退出 0。真实 PostgreSQL 17/PostGIS 3.5、独立角色/RLS、Redis 7.4.2 authenticated runtime、非 root Chromium/nginx；组件模式关闭，Go race 开启。

| 门禁 | 结果 |
| --- | --- |
| 农场 Go HTTP/浏览器根及子项 | 10/10 pass |
| 真实 React 登录→Vue 农场浏览器检查 | 23/23 pass |
| Python 配置/边界/身份/真实 PG store | 81/81 pass，零跳过 |
| 真实 Flow 适配器与旧 worker | 10/10 pass |
| 扩展当前接口隔离 | 156/156 pass，30 平台/20 GIS 路由，229 请求，219 拒绝快照 |

浏览器实际创建两个区域农场、提交编辑、同版本竞态保留草稿、归档、重新加载、撤权与历史导航清空、相同 HttpOnly 会话及完整数据库 checkpoint；161 次网络观察、违规 0。测试 fixtures 不是实际客户农场。全部 JSON 原报告在 `ci-79f25/`，其余 identity/迁移/对象/作业/缓存/SSE 旧回归同轮通过。

本地 receiver 红绿回归：旧源码＋新普通函数测试 4 pass/2 fail；修复后完整 Vue 75 pass/382 断言，vue-tsc/Vite 构建成功。普通函数 probe 不是浏览器验收，最终真实浏览器成功才闭合该问题。

## 失败保留与 CI 来源边界

保留 `ci-f767-preflight-failure`、`ci-e8fa-browser-failure`、`ci-56fb-login-failure`、`ci-11a380-receiver-failure`，不覆盖失败结果。分别修复审查清单漂移、私有 fixture 目录权限、原生 fetch 接收者及 runner checkbox 微任务问题；未放宽权限、隔离、断言或浏览器沙箱。

session/membership/gateway 三条成功运行对应上一 CI `8ff749f5`/产品 `f5ea0758`；后续仅 identity 触发过滤/接线测试及 farm runner 改动。identity/parser/W00 三条对应 `79f25d19`/`f281a8d4`。`final-ci-success.json` 逐条保存实际 SHA，不声称六条均在同一提交重跑。

## 未验证条件

本项代码/fixture 验收不代表实际农场试点、商业发布、全部需求验收、支付或设备控制。完整产品历史没有因 CI scaffold 发布而同步到产品分支。后续地块、影像、报告、预算等仍按任务依赖实施；区域字典需要部署者合法配置，空模板不能作生产可用证据。
