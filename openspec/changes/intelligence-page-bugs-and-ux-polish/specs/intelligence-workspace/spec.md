# Spec Delta: Intelligence Workspace

## Purpose

规范情报工作台（`/intelligence`）的会话缓存账号隔离、工作区成员角色投影与变更一致性、跨子视图 Agent 协作可达性，以及中文文案与 UI-Kit 组件规范。

## ADDED Requirements

### Requirement: 情报会话缓存按登录账号 Scope 隔离
前端情报会话目录的 `localStorage` 热启动缓存 MUST 按当前登录用户 ID（或匿名空态）隔离存储键；切换账号或未登录时 MUST NOT 读取或展示其他账号缓存的会话列表。

#### Scenario: 同浏览器切换账号不串会话缓存
- **WHEN** 用户 A 登录并拉取会话列表写入缓存后退出，用户 B 在同一浏览器登录进入 `/intelligence`
- **THEN** `cachedConversations` 只读取用户 B 专属缓存键（初始为空数组），不会展示用户 A 的会话列表

#### Scenario: 未登录状态不读写共享会话缓存
- **WHEN** 当前用户状态为空（未登录）
- **THEN** `cachedConversations` 返回空数组，且不会污染任意已登录用户的缓存键

### Requirement: 工作区成员角色准确投影与编辑一致
工作区成员查询接口（`GET /api/intelligence/members`）MUST 返回成员在当前工作区的有效角色（租户 `owner` 保持 `owner`，其余成员优先取当前工作区 `WorkspaceMembership.Role`：`admin`、`editor`、`viewer`）；成员角色修改接口（`PATCH /api/intelligence/members/:userId`）MUST 接受 `admin`、`editor`（兼容 `member`）、`viewer` 并同步更新工作区角色，使前端刷新后角色回显一致。

#### Scenario: 将成员设为 viewer 后刷新仍显示 viewer
- **WHEN** 工作区管理员将某成员角色修改为 `viewer` 并重新调用 `ListMembers`
- **THEN** 该成员返回的 `role` 为 `"viewer"`，且其丧失写权限（如 `intel.reports.write`）

#### Scenario: 支持 editor 与 member 角色设置并回显为 editor
- **WHEN** 工作区管理员将某成员角色修改为 `editor`（或兼容传入 `member`）并重新调用 `ListMembers`
- **THEN** 该成员返回的 `role` 为 `"editor"`，与邀请成员的角色枚举（`admin` / `editor` / `viewer`）保持一致

### Requirement: 情报页跨视图 Agent 可达性与中文 UI-Kit 规范
情报工作台（`/intelligence`）MUST 在全部 9 个导航子视图下保持「情报助手 & 监控」能力可达（专题视图内嵌展示，其余视图可通过顶栏入口打开抽屉或侧栏）；界面文案 MUST 统一为中文且移除过期的 Telegram 媒体不支持提示；提示横幅、未读/状态徽章、弹窗与抽屉 MUST 使用 UI-Kit 的 `Callout`、`StatusBadge`、`AppModal`、`AppDrawer`，不再直接引入 `antd` 的 `Badge`、`Alert` 或原生 `Modal`。

#### Scenario: 非专题视图下唤醒情报助手
- **WHEN** 用户处于「总览」「信源」或「关键词」等非专题 Tab
- **THEN** 顶栏显示「情报助手 / 监控」入口按钮，点击可在不离开当前视图的情况下打开当前工作区作用域的对话与监控面板

#### Scenario: 情报页面零 antd Alert/Badge/原生 Modal 残留
- **WHEN** 扫描 `web/src/pages/intelligence/` 下的组件导入
- **THEN** 不存在从 `antd` 直接导入 `Alert`、`Badge` 或 `Modal` 的代码，统一消费 `@/components/ui/*`
