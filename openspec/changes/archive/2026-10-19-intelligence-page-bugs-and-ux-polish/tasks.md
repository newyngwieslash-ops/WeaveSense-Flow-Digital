# Tasks

## 1. 会话缓存按用户隔离与工作区成员角色一致性

- [x] 1.1 在 `web/src/services/api/intelligence.ts` 中将 `listConversations` 与 `cachedConversations` 的 `localStorage` 缓存键改为按当前登录用户 ID 隔离（未登录时不读写共享缓存），并在 `web/test/intelligence-gaps9.test.ts` 补充多账号切换缓存隔离与空用户测试，运行 `cd web && bun test test/intelligence-gaps9.test.ts` 验证通过
- [x] 1.2 在 `backend/internal/flow/p7.go` 中修复 `ListMembers`（结合当前工作区 `WorkspaceMembership.Role` 返回 `owner`/`admin`/`editor`/`viewer`）与 `SetMemberRole`（支持 `admin`/`editor`/`member`/`viewer`），在 `backend/internal/flow/p7_test.go` 补充 `viewer` 与 `editor` 回显断言并运行 `cd backend && go test ./internal/flow -run TestP7MembersRoleEditing` 验证通过

## 2. 情报工作台跨视图 Agent 侧栏、中文 UI 与 UI-Kit 组件对齐

- [x] 2.1 在 `web/src/pages/intelligence/index.tsx` 中提供跨 9 个子视图可达的「情报助手 & 监控」侧栏/抽屉（`AppDrawer`），并将对话、监控、审批卡片文案统一为中文；在 `web/src/pages/intelligence/{sources-panel,reports-panel,topics-panel,monitor-editor,workspace-admin-panel,mcp,plan-card}.tsx` 中完成中文化、修正 Telegram 媒体提示、统一成员角色选项（`admin`/`editor`/`viewer`），并将 `antd` `Alert`/`Badge`/`Modal` 替换为 `Callout`/`StatusBadge`/`AppModal`；在 `web/test/intelligence-gaps9.test.ts` 补充 UI-Kit 导入契约断言，运行 `cd web && bun test test/intelligence-gaps9.test.ts && bun run build` 验证通过
