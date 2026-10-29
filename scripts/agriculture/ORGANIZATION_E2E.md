# DT010 真实组织治理浏览器检查

此脚本运行实际 Go API、独立 SQLite fixture、生产 React/Vue 与两个独立 Chromium profile，不模拟接口、登录或授权结果。它不是 PostgreSQL RLS、GIS 资源、同租户多工作区或商用验收。

```sh
node --check scripts/agriculture/organization-e2e.mjs
node --test scripts/agriculture/organization-e2e.test.mjs scripts/agriculture/session-e2e.test.mjs
# 先构建包含 DT010 的 Go binary、React/Vue agriculture profile。
CHROME_BIN=/usr/bin/chromium node scripts/agriculture/organization-e2e.mjs
```

默认构建位置及可覆盖变量与 `SESSION_E2E.md` 相同。独立结果为 `.local/evidence/organization-browser-e2e.json`，不覆盖已通过的统一会话报告。脚本记录源代码组合清单、构建 hash、环境、固定阶段及脱敏断言结果。

## 边界与实际路径

- 仅对自建临时数据库开启农业 SQLite 开发模式及注册。首账号真实注册为管理员，第二账号用真实注册验证码 API、仅绑定 127.0.0.1 的 SMTP sink 和实际注册服务取得已验证邮箱；验证码不由脚本伪造。
- 邮箱固定在保留域 `fixture.invalid`；SMTP sink 不转发，账号、口令、邮件、一次性邀请令、Cookie 只在内存。组织邀请不会发送邮件。
- 两个账号均使用真实登录表单。组织创建、邀请创建/手动接受、角色升降、成员停用/恢复/移除、组织停用/恢复经实际页面与确认弹窗操作。HTTP 请求用于验证真实服务端权限拒绝，不替代这些业务 UI 写入。
- 过期邀请唯一时间 fixture：仅将自建数据库中按实际 token hash 精确找到的邀请 expiry 改为过去。正常邀请、接受、角色与状态均由服务实现。
- 验证匿名 401、未获授权对象 404、viewer 治理 403、owner 保护、邀请过期/重放 400、移除后普通邀请不能重新授权。成员被停用或移除不会撤销整个账号登录：原 Cookie 对原组织失权、对自身另一组织仍有效。
- 拒绝后重新打开原 React/Vue 工作区链接并检查显示结果；任何 Vue 集成缺口导致失败，不把隐藏按钮当成授权证据。
- 仅使用自建目录、进程和 socket，finally 清理。无截图、浏览器 trace、API body 或后台日志输出。浏览器所有跨 origin 请求被拒绝，并要求尝试次数为零。

## 当前验收状态

脚本语法、诊断脱敏及复用 SMTP/proxy transport 可在本地验证。当前容器 Chromium 因受限 AF_UNIX socket 无法启动；完整浏览器路径必须在获准 CI 执行后才可标记通过。之前会话 44 项通过不代表本组织流程已通过。
