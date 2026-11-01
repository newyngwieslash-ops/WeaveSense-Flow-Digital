# DT024 检查点：in_progress

依赖 DT017–023 均已按各自任务验收；本项仍等待最终冻结源的真实 Redis/Docker CI 与独立审查。

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

剩余条件：冻结本候选、普通推送到授权 CI 分支、真实 PG/Redis/Docker/浏览器组合回归，核验源码与证据，再独立审查并更新状态。真实农场、影像、模型、设备、付费和生产部署不是本项验收。
