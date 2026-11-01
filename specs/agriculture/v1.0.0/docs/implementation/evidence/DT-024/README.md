# DT024 验收：done

依赖 DT017–023 均已按各自任务验收。本项真实身份/隔离 CI 已通过并独立审查；gateway首次构建超时后，仅重试原job、未改源/限额/断言，attempt2实际通过，六条CI最终全部成功，允许进入DT025。

## 最终验收（2026-11-01 06:30 UTC）

同一产品源71021e9f、同一CI aa60e1a6，gateway run41278325702 attempt2/job124772007832成功。容器16项检查通过，实际Vue构建1.9秒完成；真实浏览器19项通过、出站违规0。新artifact13073167829的398源哈希独立匹配冻结源；镜像 `sha256:606805aae945c575446059745cbb4b23751340cdbb240ccbfd33fbebefc5d9bc`。证据见 `gateway-aa60-attempt2/` 和 `final-ci-success.json`。

本地相同Node24.22.1、冻结Vue源/原lock完整构建2.27秒通过；未复现首次Docker停滞，不能宣称其根因已经确认。旧失败原件/原终态清单完整保留，不覆写成成功。本任务完成是当前W02开放接口的隔离基线，不是所有80项验收、G0-G5、未来域接口、真实农场或商用可发布。

## 冻结源与真实 CI 检查点（2026-11-01 06:24 UTC）

产品源 `71021e9f4632b1eb3a2002e354164453fdb13c1c`；CI scaffold `aa60e1a6222cbfab3426134ebc7b88d6313abf76`。完整产品历史尚未因此发布到产品分支。

- 真实 identity run `41278325667` 成功：隔离79/79、零跳过，真实 Redis runtime，组件模式关闭，race开启；Go精确集合180/4/6、21条平台路由158次请求、GIS9路由，全业务快照未变化。既有身份/迁移/对象/作业/缓存/SSE/浏览器回归无遗漏、失败或跳过。398 overlay 独立逐Git对象核验通过，见 `ci-aa60e1a6/independent-review.json`。
- 冻结源本地原生 PG/PostGIS 重验79/79、零失败/跳过，race开启，1537源文件逐Git对象匹配，见 `native-71021e9f/`；本地仍明确为auth组件模式，不冒充Redis验收。
- 本轮6条CI中5成功；gateway run `41278325702` 失败，浏览器阶段通过，独立容器构建在最后 Vue `npm run build:agriculture`（build16/16）超过1800秒，`TimeoutExpired`。前序 npm ci 11.9秒完成、React构建3.9秒完成。保存原安全失败报告 `gateway-aa60e1a6/gateway-container-smoke.json`，不把失败改称未运行或已通过。
- 全部8份artifact的398源映射已核对，见 `source-verification.json`；6条终态见 `final-ci.json`。这些是真实依赖上的fixture证据，不是实际农场/商用/生产部署验收。

历史检查点恢复动作现已完成：原源单job重试及终态独立核验通过。保留冻结源和失败证据，下一依赖就绪任务为DT025；只读契约准备在工作区 `.local/audit/dt025-*.md`。

## 实现

- `backend/internal/agriculture/isolation_matrix_postgres_test.go`：真实平台服务登录和 cookie/CSRF，19 public + 2 internal 路由，158 HTTP 案例；正控和拒绝均比较业务全行快照。
- `isolation_object_session_test.go`、`object_gateway_integration_test.go`、GIS `tests/object_http_fixture.py`：真实平台/GIS双 PG、真实文件和回调；asset/report、GET/HEAD/Range、跨租户/同租户工作区、真实外对象/version、逐请求并发期望、读取中撤权、固定无详情错误响应。
- `isolation_projection_postgres_test.go`：受限 PG test-only provider、缓存/版本/撤权、SSE cursor/逐帧注销、30并发连接池。
- GIS `test_agriculture_isolation_matrix_postgres.py`：9个生产操作的76项签名/范围/缺依赖/不开放旧路由检查；全行哈希和审计 canary。
- `contracts/agriculture/isolation-matrix.json` 与 `scripts/agriculture/isolation-matrix-result.py`：精确清单、状态、完整 Go 子项、全部请求快照和独立严格 CI gate；重复/缺少/额外/跳过/组件模式/未启用 race 均不能验收。

没有新增生产接口、数据库迁移或前端业务；这是当前 W02 隔离基线验收，不是 W03 农场或 W06 provider 实现。完整范围与后续新增接口要求见 `docs/agriculture/ISOLATION_BASELINE.md`。

## 已执行本地预验

```sh
python3 scripts/agriculture/native-postgres-test.py \
  --package-root "$PWD/.local/native-postgres/package-root" \
  --python "$PWD/.local/identity-rebuild/venv/bin/python" \
  --go "$PWD/.local/go-toolchain/go/bin/go" --phase isolation --race
```

`run_e9b0e1fb8114431382c2eda58f7feb89`：79 passed、0 failed、0 skipped、进程退出及停库退出均0、源码开始/结束一致，74.963秒。原脱敏结果保留为 `local-precommit-*.json`，其 recorded HEAD 为开发基线，最终源映射必须逐文件核对后补充，不能误称报告已记录后来的提交。
其中 3 个 Go bridge 分别包含 180 / 4 / 6 个根、分组和子测试记录，不能与79相加当独立测试数。全部开启 `-race`；本地 `auth_component_only=true`，不代表完整 Redis runtime 启动。CI 强制真实 Redis、ValidateRuntime、组件模式关闭和 race。

额外验证：`pytest -q tests/agriculture` 140项通过；严格 reducer 10项、assembly 8项、parser wiring 5项通过；既有 object gateway 17项组件回归通过。测试中捕获并修复的是 fixture 的非法幂等 key、错误 GORM view建模、owner缺显式内容角色；未放宽任何生产权限或断言。一次 race 运行的3个 Go suite 全过，但 native 报告生成曾因 CLI 变量被覆盖失败，已修并在上述完整运行重验，不把该失败尝试当成成功报告。

本项剩余阻塞：无。未来新增接口必须扩展严格隔离门槛；真实农场、影像、模型、设备、付费和生产部署不是本项验收。
