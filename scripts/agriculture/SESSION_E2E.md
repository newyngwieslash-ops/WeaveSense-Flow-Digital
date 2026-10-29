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

覆盖匿名/伪造 cookie 与 localStorage、跨 React/Vue 的真实 actor、HttpOnly 同路径 cookie、错误 token 不能注销原会话、Vue 退出后 React 刷新与 cookie 重放、真实 session 的数据库 fixture 到期后双方拒绝恢复。仅过期负例写本脚本新建数据库；其他身份操作都是实际 API/浏览器。所有进程和临时目录由脚本创建并精确清理。

匿名、伪造 cookie、正确 session ID + 错误 token、注销后重放及过期 cookie 均直接请求真实受保护 `/api/intelligence/workspaces` 并断言 HTTP 401，而非仅查看界面隐藏或 `/auth/session` 的 null。农业 GIS 业务资源尚未开放；本脚本不把工作区入口拒绝当作未来 GIS 全部资源授权通过。口令重置浏览器流程不在本脚本范围，后端真实数据库撤销测试是独立分层证据，不能冒充浏览器覆盖。

两个 `session-e2e.test.mjs` 测试仅验证静态同源服务路由/穿越拒绝，不冒充身份集成测试。

当前云执行器发现 AF_UNIX `socket()` 被沙箱拒绝，Chromium 尚不能在该环境启动；标准执行升级也在执行器初始化失败。真实 Go 注册已运行通过，浏览器闭环必须在支持 Chromium 的 CI/授权执行环境实际运行后才可记 pass。不得通过删除浏览器断言或换成 mock 将此阻塞“修复”。

`.github/workflows/agriculture-session.yml` 仅 `contents: read`，不发布镜像/注册表/部署或写上游。`ci/agriculture-w00` 分支通过 `session-ci-prepare.py` 只读获取固定 Flow/AgriScope SHA 的源码归档（不带 `.git`），仅覆盖脚本内逐文件允许列表。报告明确这是 scaffold 提交 + 固定上游 + 每个覆盖文件 SHA256 的组合来源，不冒充完整产品提交。完整产品分支直接使用实际 checkout，不替换上游或覆盖用户代码。`--list-overlay` 可输出精确待发布清单。

浏览器断言要求 `/agri/` 路径且 Vue 独有 `main[data-session-status=ready]`，不能用 React 中相同 actor selector 冒充跳转通过；伪造身份查询参数必须拒绝；任何页面对外请求尝试均失败。历史回退测试检查恢复后的 React 重新验证，但未声称 Chromium 一定启用了 BFCache。
