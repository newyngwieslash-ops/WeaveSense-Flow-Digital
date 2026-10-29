# 农业网关原生验证（DT014）

## 实际覆盖

`scripts/agriculture/native-gateway-test.py` 同时启动真实 Go 服务和非特权 nginx，使用实际 React/Vue 农业生产构建。不是 Node 静态服务器或 API mock。当前矩阵验证：

- React 与 `/agri` 深链接 HTML 回退、相对重定向及查询保留；HTML/API/error 均不缓存。
- 内容哈希资源 immutable 缓存；缺失文件、隐藏文件、私有路径、旧 GIS 路径不能变成成功 SPA 页面。
- 真实注册、登录、共享根路径 HttpOnly/SameSite Cookie、同一身份、组织创建、跨 Origin/错误端口拒绝、退出、旧 Cookie 重放拒绝与重新登录。
- 停止真实 Go 上游后 nginx 返回真实 502 错误页；随机查询值不写入过程日志。

`/api/auth/session` 的既有合同允许匿名状态返回 HTTP 200 且 `data.user=null`；真正受保护的工作区 API 必须返回 401。测试同时检查二者，不把匿名状态误认成登录成功。

## 运行

先按项目锁定工具链构建 Go 和两套农业前端。入口文件必须是 `web/dist-agriculture/index.html` 与 `services/agri-gis/frontend/dist-agriculture/index.html`，Go 二进制默认 `.local/bin/agriculture-platform`。

```bash
python scripts/agriculture/native-gateway-test.py \
  --nginx .local/nginx-gateway/root/usr/sbin/nginx \
  --mime-types .local/nginx-gateway/root/etc/nginx/mime.types
```

也可用环境已安装的 nginx 与其 `mime.types`。本次验证在 Debian 13 非 root 工作区，将官方 `nginx` 和 `nginx-common` **1.26.3-3+deb13u7** 两个 `.deb` 按固定 SHA256 校验后，仅用 `dpkg-deb -x` 解包到 `.local/nginx-gateway/root`。未运行包安装脚本、sudo、apt install 或系统服务。包地址/版本/哈希见 `evidence/w01/native-gateway-20261029/packages.json`；上游已签名 Debian 元数据的校验链与本地 PostgreSQL 验证相同。解包缺系统动态库时停止并报告，不自动修改系统。

测试直接读取生产 `deploy/agriculture/nginx.conf.template`，只替换三个部署占位及原生环境所需的 MIME、pid、临时目录路径。使用 loopback 高位端口和测试拥有的目录；因当前环境不允许 Unix-domain IPC，原生验证采用 `master_process off`。该模式验证真实 HTTP 路由，不代表容器多 worker 行为或生产性能验收。nginx 的 `-p/-c/-g/-t` 行为见[官方命令行文档](https://nginx.org/en/docs/switches.html)。

测试临时账号、数据库、进程日志保留在 `.local/native-gateway/run_<UUID>`，不发布 Cookie、密码或原始过程日志。测试进程在 finally 中停止。正式 JSON 仅包含结果、来源/产物哈希与明确限制。

## 证据与剩余条件

`evidence/w01/native-gateway-20261029/http-report.json` 记录 **50 项实际 HTTP 检查通过**，包括 2175 个来源文件哈希、两个构建的全部静态文件哈希及 nginx/Go 二进制哈希。恢复后的脏工作区使用实际字节追溯，不能仅用早于修改的 Git HEAD 证明构建来源。

保留了最初的正则未引号导致配置解析失败、原生默认临时目录不可用，以及 harness 最初误期望匿名 session 为 401 的记录。前两项按实际错误修复配置，后一项按既有会话合同更正测试并补受保护 API 的 401 负例，没有修改后端以迁就断言。

**这不是浏览器闭环通过。** 当前系统 Chromium 155 默认 headless 启动时因 `process_singleton_posix socket() EPERM` 退出；探测证据在 `browser-capability.json`。没有关闭浏览器 sandbox、改系统策略或冒充浏览器点击。需在支持实际 Chromium 的环境继续通过真实 nginx 执行跨 React/Vue 导航、刷新、退出重登与网络请求检查。

GIS 领域业务接口仍未就绪时应明确 401/404/503，不返回虚构业务成功。Go SQLite fixture 不替代 PostgreSQL 隔离验收，原生 HTTP 通过不表示 Docker 镜像已构建、远程 CI 成功、生产部署或商业试点通过。
