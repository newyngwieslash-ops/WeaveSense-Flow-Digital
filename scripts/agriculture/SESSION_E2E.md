# DT009 真实同源会话测试

执行（仓库根）：

```sh
node --test scripts/agriculture/session-e2e.test.mjs
node scripts/agriculture/session-e2e.mjs
```

前置条件：

- 编译当前 `backend/cmd/server` 到 `.local/bin/agriculture-platform`；不使用测试替身服务器。
- React `web/dist-agriculture` 和 Vue `services/agri-gis/frontend/dist-agriculture` 为当前源码的真实生产构建。
- Node 22+（内置 fetch/WebSocket）、Python 3（仅变更专属 SQLite fixture 到期时间）、可启动的 Chromium/Chrome。
- 浏览器运行环境必须支持 AF_UNIX socket；只开放 TCP 不够。默认 `CHROME_BIN=/usr/bin/chromium`，CI 可设置真实已安装 Chrome 路径。

可覆盖路径：`AGRI_E2E_BINARY`、`AGRI_E2E_REACT_DIST`、`AGRI_E2E_VUE_DIST`、`AGRI_E2E_REPORT`。默认报告 `.local/evidence/session-browser-e2e.json`，只记录脱敏断言、源码提交/dirty 状态、二进制及入口文件摘要。不保存密码、cookie、浏览器响应体、截图或完整后端日志。

测试启动新的 loopback Go 服务与唯一临时 SQLite 数据库，执行真正的注册 API、Chromium 输入登录表单及同源完整文档跳转。临时同源服务器分别挂载 React `/`、Vue `/agri/`，所有 `/api/` 转发真实 Go。浏览器 CDP 请求拦截拒绝测试 origin 以外的 HTTP 请求。

与组织harness共用 `browser-navigation.mjs`：新导航必须匹配 `Page.navigate` 返回的frame/loader及 `Page.getFrameTree` 的实际提交，然后才接受新文档complete。原仅比较URL与performance name的条件无法区分同URL旧DOM；已用确定性单元复现并修正。共享等待保留20秒总deadline，诊断只存提交布尔值、不输出URL/loader ID。该harness修订须另一次真实CI，旧会话44条证据不自动视为新版本已通过。

覆盖匿名/伪造 cookie 与 localStorage、跨 React/Vue 的真实 actor、HttpOnly 同路径 cookie、错误 token 不能注销原会话、Vue 退出后 React 刷新与 cookie 重放、真实 session 的数据库 fixture 到期后双方拒绝恢复。仅过期负例写本脚本新建数据库；其他身份操作都是实际 API/浏览器。所有进程和临时目录由脚本创建并精确清理。

匿名、伪造 cookie、正确 session ID + 错误 token、注销后重放及过期 cookie 均直接请求真实受保护 `/api/agriculture/workspaces` 并断言 HTTP 401，而非仅查看界面隐藏或 `/auth/session` 的 null。仅本脚本自建服务显式启用 `FLOW_AGRICULTURE_ENABLED=true`、`FLOW_AGRICULTURE_ALLOW_SQLITE_DEV=true`。农业 GIS 业务资源尚未开放；本脚本不把工作区入口拒绝当作未来 GIS 全部资源授权通过。

## 口令重置扩展

口令重置使用隔离数据库 fixture：仅给本脚本注册的用户绑定随机 `fixture.invalid` 邮箱；SMTP 配置通过真实管理员 API 写入同一私有新建 SQLite，host 固定 `127.0.0.1`、随机端口、不配置任何外部账号。SMTP sink 只接收该地址，拒绝 relay、非 fixture 发件人/收件人、超大内容，无对外连接功能。邮件只在内存，关闭时清空。

浏览器实际操作 forgot-password 的“发送验证码”与“重置密码”两阶段表单。验证码由真实 Go 服务生成、按实际 HMAC 规则入库、经真实 SMTP socket 发送至本地 sink；测试不插入验证码、不模拟发送/重置成功、不改 React 状态。然后验证两个真实旧会话的工作区 HTTP 401、双方页面拒绝旧 cookie、原口令 HTTP 401、新口令真实登录表单恢复同一身份、数据库内码为 hash 且已消费，以及重复使用该码失败。数据库直接修改仅邮箱 fixture 与原有 expiry 负例，不写新密码或伪造 session。

这是 **loopback 邮件链路**，不是外部 SMTP/邮件供应商或用户真实邮箱验收。第三轮原有 31 项通过报告保留。扩展已在 CI run `40818545088` 的 snapshot `f45cb899126ef543956bf2e86dfa4e0b9c2e4ee2` 实际通过 44 项断言；当时工作区端点为旧平台列表。当前 DT010 将 React/Vue 和受保护负例切换为农业端点，必须对新源码另行回归，不能沿用旧结果。后端真实数据库测试仍作为独立分层证据，PostgreSQL RLS 与 GIS 上下文不在本测试范围。

农业端点切换后的真实回归已通过：run `40819936013`（snapshot `84b279c2148c54cd72c9833e777282ec3ae3e458`）及 run `40820949488`（snapshot `bac853367d456bcb865596e05c573e5ba5589859`）均 44 条断言通过。后者 artifact `12874367498`，zip SHA256 `66e965bd29d93f8b7e681fc36dcb06fad68ca3fcf4fa3b0602e3b925507d0c78`。这是对应源码组合的真实浏览器结果，不是仅依据构建或旧端点结论推定通过。

后续组织浏览器 run `40824605197` / job `123284634505` 在 snapshot `81a41097d3b6e58748cf286c39ac469f20b4e5da` 实际通过62条组织/既有会话撤权断言，详见 `ORGANIZATION_E2E.md`。这不是本脚本44条口令重置路径在该新snapshot重新执行的证据，不替换上述会话原始报告。该run的72项overlay与本地 `7a07fdfd9f53b8b117c1da8c61190c99646b0e59` 比较仅两份说明文档不同；运行代码、配置及测试脚本一致，精确比较已归档。

`session-e2e.test.mjs` 验证静态同源服务路由/穿越拒绝，以及 loopback SMTP 协议接收、拒绝 relay、大小限制与内存清理；这些不冒充浏览器身份集成测试。

当前云执行器发现 AF_UNIX `socket()` 被沙箱拒绝，Chromium 尚不能在该环境启动；标准执行升级也在执行器初始化失败。真实 Go 注册已运行通过，浏览器闭环必须在支持 Chromium 的 CI/授权执行环境实际运行后才可记 pass。不得通过删除浏览器断言或换成 mock 将此阻塞“修复”。

已在授权 GitHub Actions 环境实际完成验证：[run 40816921854](https://github.com/newyngwieslash-ops/WeaveSense-Flow-Digital/actions/runs/40816921854)，源码组合 snapshot `b31ff9ad80987264769be2fe51235e4f1c019772`，Chrome 156.0.8078.47 / Node 24.22.1 / Python 3.12.3，31 项浏览器/API 断言全部通过、外连尝试为 0。真实 Go 编译、React/Vue 生产构建及各自测试步骤均通过。前两轮失败暴露并修复了导航同步与匿名表单焦点重验丢输入的问题，失败证据保留，不将其回写为通过。此成功仍不代表 PostgreSQL RLS、GIS 签名服务上下文、口令重置浏览器全流程或商用验收通过。

`.github/workflows/agriculture-session.yml` 仅 `contents: read`，不发布镜像/注册表/部署或写上游。`ci/agriculture-w00` 分支通过 `session-ci-prepare.py` 只读获取固定 Flow/AgriScope SHA 的源码归档（不带 `.git`），仅覆盖脚本内逐文件允许列表。报告明确这是 scaffold 提交 + 固定上游 + 每个覆盖文件 SHA256 的组合来源，不冒充完整产品提交。完整产品分支直接使用实际 checkout，不替换上游或覆盖用户代码。`--list-overlay` 可输出精确待发布清单。

浏览器断言要求 `/agri/` 路径且 Vue 独有 `main[data-session-status=ready]`，不能用 React 中相同 actor selector 冒充跳转通过；伪造身份查询参数必须拒绝；任何页面对外请求尝试均失败。历史回退测试检查恢复后的 React 重新验证，但未声称 Chromium 一定启用了 BFCache。

浏览器在进入应用前执行真实 `Page.bringToFront` 并确认 `document.hasFocus()`。填写用户名后再创建专属空白页、实际切换前台 tab、确认原页 blur，再返回原页，等待真实 `/api/auth/session` 响应完成及渲染后断言输入仍保留；不重填掩盖丢失，也不直接修改 React state。此负例防止通过测试初始前台条件掩盖匿名表单重验时被卸载的产品缺陷。
