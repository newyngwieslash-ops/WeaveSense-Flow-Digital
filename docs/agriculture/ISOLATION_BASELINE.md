# DT024 当前农业接口隔离基线

状态：实现与复验中，尚未完成真实 Redis/Docker CI 验收。不能据此宣称 W02/G0 或商用发布通过。

## 受测范围

| 边界 | 精确清单与证明 |
|---|---|
| Go 农业公开接口 | `API.Register` 的 19 个 method/path；真实平台 Login/CurrentUser、cookie、CSRF；跨租户、同租户未加入工作区、低角色、撤销、过期和注销 |
| Go 内部回调 | 2 个 method/path；真实服务签名、主体/范围/epoch、错误方向、snapshot 替换及撤权 |
| GIS 生产 app | 9 个 method/path，含公开 health；8 个内部操作分别验证签名和 scope；docs/openapi/旧路由不开放 |
| 对象下载 | Go 平台 PostgreSQL → 签名 HTTP → GIS PostgreSQL → 私有真实文件；asset/report GET/HEAD/Range、已存在 foreign 对象/version、工作区替换、撤销及并发 |
| 对象读取中撤权 | 仅测试进程的 stdin barrier 暂停真实文件读取，平台提交撤权后释放，GIS 最终真实回调必须拒绝，不能发送内容或下载元数据 |
| 缓存/SSE/连接池 | 显式 test-only PostgreSQL 领域投影、FORCE RLS、runtime 只读角色、真实会话；版本/主体/范围键、撤权和 in-flight fill、逐帧注销、cursor、并发复用、空 scope |

Go 的固定 21 路由、158 请求案例及预期状态在 `contracts/agriculture/isolation-matrix.json`。测试执行时枚举实际注册路由并比较精确集合；CI 独立核验清单、完整案例/测试集合、预期状态和无副作用观察，缺失、重复、新增未登记、跳过或伪成功摘要均失败。

组织创建、邀请接受、工作区列表按原契约验证：不能将合法创建错误地设成 viewer 禁止，也不能将被过滤列表误当成应返回 403。规范版和兼容版路径分别执行；身份从真实会话取得，伪造 user/tenant/role 参数不能覆盖它。

这些不是整个平台所有路由。原 Flow/数学/MCP 路由继续存在；原有旧业务隔离、缓存、SSE 及真实浏览器链回归仍保留。尚无生产农业 Agent 内容工具、领域 ownership 或 JobEvents provider 的路径保持关闭；不得将测试投影/固定领域事实当成生产农场、Agent 或 W06 调度已完成。

## 无副作用与不泄漏

拒绝请求前后通过断言专用 migration 连接读取完整排序业务行；真正服务只使用受限 runtime 角色。平台覆盖 10 张治理/授权/账本表；GIS 覆盖当前领域、对象、作业侧表及审计，显式 scope 和审计 canary 防止空快照伪通过。
登录、CSRF、安全重放 nonce 是独立安全状态，合法变化明确排除；不排除领域/计费/成员/对象发布变化。读取中撤权以已提交撤权之后的快照为基准，不将预期撤权本身误判为拒绝副作用。
报告只包含固定案例、状态、相等布尔和哈希；原始行、cookie、签名、DSN、日志和原始 JUnit 不上传。

## 执行与门禁

本地使用已校验的原生 PostgreSQL 包及锁定 Go/Python：

```sh
python3 scripts/agriculture/native-postgres-test.py \
  --package-root "$PWD/.local/native-postgres/package-root" \
  --python "$PWD/.local/identity-rebuild/venv/bin/python" \
  --go "$PWD/.local/go-toolchain/go/bin/go" --phase isolation --race
```

该入口明确使用 `auth_component_only=true`，表示真实 PG 认证组件而非完整 Redis 平台启动，报告检查源码开始/结束一致。最终 CI 必须使用真实 Redis、`ValidateRuntime`、`auth_component_only=false` 和 Go race detector；本地组件模式不能满足 CI gate。

`.github/workflows/agriculture-identity.yml` 复用原受限只读 CI，运行已有身份链及新矩阵；`isolation-matrix-result.py` 必须检查 79 个完整 pytest 案例（76 GIS + 3 Go bridges），而 Go bridges 严格核验 180/4/6 个 root/分组/子测试记录。它们是包含关系，不能相加当成独立测试数。

新增路由时同步更新实际路由测试、固定契约和独立 reducer 的完整用例清单，补充有效请求/错误范围/拒绝无副作用证明；不得通过降低数量、移除断言或改期望以掩盖实现错误。
