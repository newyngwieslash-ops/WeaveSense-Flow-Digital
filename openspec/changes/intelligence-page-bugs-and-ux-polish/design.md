# Design: 情报工作台代码缺陷修复与中文 UI / 布局体验完善

## Context

参见 `proposal.md`。情报工作台（`/intelligence`）后端与前端主流程已就绪，但遗留了会话缓存未按用户隔离、工作区成员 `viewer`/`editor` 角色回显错位、非「专题」Tab 下无法打开 Agent 对话，以及英文脚手架文案和 `antd` `Badge`/`Alert`/`Modal` 未对齐项目 UI-Kit 规范的问题。

## Goals / Non-Goals

**Goals:**
- 消除同浏览器切换账号时的情报会话缓存串号风险；
- 修复 `ListMembers` / `SetMemberRole` 对工作区角色（`admin` / `editor` / `viewer`）的读写不一致；
- 让全部 9 个情报子视图都能随时使用当前工作区作用域的 Agent 对话与监控管理，并完成全中文界面与 UI-Kit（`Callout` / `StatusBadge` / `AppModal` / `AppDrawer`）收口。

**Non-Goals:**
- 不改动数据库表结构或新增迁移；
- 不改变后端 Agent 运行链、监控审批协议或采集调度逻辑。

## Decisions

1. **按用户 ID 隔离会话缓存（`web/src/services/api/intelligence.ts`）**
   - 从 `useUserStore.getState().user?.id`（并支持显式传入 `userId` 参数以便单元测试验证）构造缓存键 `flow.conversations.cache.v1:${userId}`。
   - 当 `userId` 为空时，`cachedConversations` 直接返回 `[]`，`listConversations` 不写全局共享缓存。

2. **工作区成员有效角色查询与更新（`backend/internal/flow/p7.go`）**
   - `ListMembers(db, scope)`：在查询 `flow_memberships` 后，批量或按工作区查询 `flow_workspace_memberships (workspace_id = scope.WorkspaceID)`。若租户角色为 `owner` 则保持 `owner`；否则若存在工作区角色（`admin` / `editor` / `viewer`）则優先返回工作区角色，兜底将租户 `member` 映射为 `editor`。
   - `SetMemberRole(db, scope, userID, role)`：允许 `"admin"`、`"editor"`、`"member"`（兼容旧调用，等同 `"editor"`）、`"viewer"`。租户层存 `"admin"` 或 `"member"`，工作区层存 `"admin"` / `"editor"` / `"viewer"`。
   - 前端 `WorkspaceAdminPanel` 成员角色下拉统一使用 `admin`（管理员）、`editor`（编辑）、`viewer`（只读），与邀请下拉完全一致。

3. **跨子视图的 Agent 与监控抽屉（`web/src/pages/intelligence/index.tsx`）**
   - 抽出共用的对话与监控侧栏渲染片段，在 `view === "topics"` 时保持三栏常驻；在其余 8 个子视图下，顶栏导航右侧提供「情报助手」与「监控（N）」按钮，点击通过 `AppDrawer` 打开同一作用域的对话与监控面板。

4. **中文文案与 UI-Kit 替换**
   - `plan-card.tsx` 与 `reports-panel.tsx` 的 `antd Alert` 替换为 `@/components/ui/product/callout` 的 `Callout`（`tone="warning"`）。
   - `workspace-admin-panel.tsx` 的 `antd Badge` 替换为 `@/components/ui/base/badges` 的 `StatusBadge`（展示未读数）。
   - `topics-panel.tsx` 与 `monitor-editor.tsx` 的 `antd Modal` 替换为 `@/components/ui/product/app-modal` 的 `AppModal`。
   - `sources-panel.tsx` 中 Telegram 的提示改为中文：「支持公开频道消息与受管大小内的媒体附件采集；超限附件将标记为超限跳过。」

## Risks / Trade-offs

- **[Risk] 既有后端单测 `TestP7MembersRoleEditing` 传入 `"member"`** → **Mitigation**：`SetMemberRole` 同时兼容 `"editor"` 与 `"member"` 输入，并新增断言验证设为 `"viewer"` 与 `"editor"` 后 `ListMembers` 返回值准确。
- **[Risk] 修改文件数超过 tweak 默认提示阈值（>6 个文件）且同时涉及前后端** → **Mitigation**：按 `comet-tweak` 升级评估规则，在进入构建前由用户明确选择继续 `tweak` 轻量流或升级 `full`。
