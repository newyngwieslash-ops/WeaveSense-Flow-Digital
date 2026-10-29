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
- 所有者治理身份与业务角色分离：真实读取行级 `canChangeRoles`/`assignableRoles`，用 UI 显式授予自身 editor，再撤回为 `[]`，确认治理权限未丢失。自身租户管理员/财务角色变更及停用/移除仍用实际 HTTP 403 验证，不将清空业务角色误当作移除组织所有者。
- 过期邀请唯一时间 fixture：仅将自建数据库中按实际 token hash 精确找到的邀请 expiry 改为过去。正常邀请、接受、角色与状态均由服务实现。
- 验证匿名 401、未获授权对象 404、viewer 治理 403、owner 保护、邀请过期/重放 400、移除后普通邀请不能重新授权。成员被停用或移除不会撤销整个账号登录：原 Cookie 对原组织失权、对自身另一组织仍有效。
- 拒绝后重新打开原 React/Vue 工作区链接并检查显示结果；任何 Vue 集成缺口导致失败，不把隐藏按钮当成授权证据。
- 仅使用自建目录、进程和 socket，finally 清理。无截图、浏览器 trace、API body 或后台日志输出。浏览器所有跨 origin 请求被拒绝，并要求尝试次数为零。

## 当前验收状态

最新实际回归：run `40824605197` / job `123284634505`、snapshot `81a41097d3b6e58748cf286c39ac469f20b4e5da`，组织浏览器再次62条全部通过；断言名称及结果与下述前次通过完全一致，无新增失败。artifact `12876162498`，zip SHA256 `e3697511449e65e4a0a6bbfee452d5c6507394b4de52c198787e758c5883cdf3`。原报告、源manifest和本地比较记录位于 `docs/agriculture/evidence/w01/organization-*-40824605197.json`。

对比本地HEAD `7a07fdfd9f53b8b117c1da8c61190c99646b0e59`，72项overlay中70项SHA256相同，唯一差异为本文件及 `SESSION_E2E.md` 两份说明文档；实际运行代码、配置和测试脚本均匹配。比较记录保存的是更新本文前的工作区hash，不伪称所有72项匹配或完整产品历史已发布。浏览器范围仍为SQLite fixture；同run的独立PG job另有自己的报告。

本地语法、诊断脱敏及复用 SMTP/proxy transport 共 10 项测试通过；真实注册/SMTP 前置 7 项通过，但本地 Chromium 受执行环境限制在启动阶段 SIGABRT，因此不将本地前置当成浏览器通过。

CI run `40820949466`、job `123273028488`、snapshot `bac853367d456bcb865596e05c573e5ba5589859` 已实际通过完整组织流程的 62 条断言（含重复授权列表检查，不代表 62 项独立需求）。Chrome `156.0.8078.47` / Node `24.22.1`，两账号实际 UI 交互、负例 HTTP 与跨 React/Vue 旧链接拒绝均通过，无浏览器异常或外连尝试。报告 artifact `12874453518`，zip SHA256 `a56f0aec1107d1a236b0ceb97abf8397e43a3c965c2f89aaa60f595ef4353f30`。

首轮 run `40819936067` 的 11 条前置通过后，在首次邀请交互前失败，报告保留。测试后来改为真实 combobox 键盘展开、可见 option 点击及 pointer 命中校验，并增加固定阶段/布尔诊断；没有绕过 UI 或放宽服务端授权断言。PostgreSQL/RLS、真实农业资源及商用条件仍需各自证据。
