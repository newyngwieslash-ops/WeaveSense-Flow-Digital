# Proposal: 情报工作台代码缺陷修复与中文 UI / 布局体验完善

## Why

当前情报工作台（`/intelligence`）在近期合入后存在多处实际代码缺陷与界面体验割裂：
1. 会话缓存使用全局未隔离的 `localStorage` 键 `flow.conversations.cache.v1`，导致同浏览器切换账号时串会话列表（违反 `AGENTS.md` §4 账号隔离合同）；
2. 成员列表 `ListMembers` 仅返回租户级角色 `m.Role`（将 `viewer` 存为租户 `member` + 工作区 `viewer`），导致将成员设为 `viewer` 后刷新仍显示 `member`，且邀请与成员编辑角色枚举不一致；
3. 右侧 Agent 对话与监控面板仅在 `view === "topics"` 时渲染，其余 8 个 Tab 下无法直接唤醒或使用 Agent 对话，与「关键词」等页面的空态引导矛盾；
4. 情报页各面板存在大量英文脚手架文案、过期的 Telegram 媒体不支持提示，以及重新引入了已归零的 `antd` `Badge` / `Alert` 和原生 `Modal`。

## What Changes

- **会话缓存按用户 Scope 隔离**：在 `web/src/services/api/intelligence.ts` 中按当前登录用户 ID 隔离会话列表 `localStorage` 缓存键，未登录或切换账号时不读取其他账号缓存。
- **工作区成员角色准确回显与枚举对齐**：
  - `backend/internal/flow/p7.go` 的 `ListMembers` 结合当前工作区 `WorkspaceMembership.Role` 返回成员在当前工作区的真实有效角色（`owner` / `admin` / `editor` / `viewer`）；`SetMemberRole` 同时接受 `"editor"` 与 `"member"`（兼容已有调用）并映射至工作区 `"editor"`。
  - `web/src/pages/intelligence/workspace-admin-panel.tsx` 成员角色下拉与邀请下拉统一对齐为 `admin` / `editor` / `viewer`（带中文标签）。
- **情报页全局可访问 Agent 与监控侧栏 + 中文 UI 与 UI-Kit 规范对齐**：
  - `web/src/pages/intelligence/index.tsx` 在所有情报子视图下提供可展开/收起的右侧「情报助手 & 监控」抽屉/侧栏（在「专题」Tab 默认展开三栏，在其他 Tab 可一键展开或常驻，保持当前工作区作用域）；
  - 清理 `sources-panel.tsx` 中过期的 Telegram 媒体不支持英文提示，更新为准确的媒体附件采集说明；
  - 将 `index.tsx`、`sources-panel.tsx`、`reports-panel.tsx`、`workspace-admin-panel.tsx`、`mcp.tsx` 中的英文脚手架文案统一替换为符合产品语境的中文文案；
  - 将情报页中的 `antd` `Badge` / `Alert` / `Modal` 迁移为项目 UI-Kit 的 `StatusBadge`、`Callout` 与 `AppModal`。

## Capabilities

### New Capabilities
- `intelligence-workspace`: 情报工作台的会话缓存账号隔离、成员工作区角色一致性、跨子视图 Agent 协作侧栏及中文 UI-Kit 规范。

### Modified Capabilities

## Impact

- 前端：`web/src/services/api/intelligence.ts`、`web/src/pages/intelligence/{index,views,sources-panel,reports-panel,topics-panel,monitor-editor,workspace-admin-panel,mcp,plan-card}.tsx`、配套测试 `web/test/intelligence-gaps9.test.ts`
- 后端：`backend/internal/flow/p7.go`、`backend/internal/flow/p7_test.go`
