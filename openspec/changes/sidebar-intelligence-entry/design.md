# Design: 侧边栏「情报」入口

## 实现（单一方案）

1. `navigation-tools.ts` 注册表追加：
```ts
{
    slug: "intelligence",
    label: "情报",
    icon: Radar,           // lucide-react，语义贴切且未被占用
    section: "创作空间",
},
```
2. `workspace-sidebar-nav.tsx` `buildNav` 第一组数组追加：
```tsx
{ ...toolItem("intelligence", "/intelligence"), title: "情报" },
```
3. `workspace-route-modules.ts` 的 `workspaceRouteLoaders` 加 `intelligence: () => import("@/pages/intelligence")`，使 `preloadWorkspaceRoute("/intelligence")` 生效。

## 决策

- 图标用 lucide `Radar`（情报/监测语义，navigationTools 现有图标无冲突）；路由 `/intelligence` 已存在（router.tsx `/intelligence`、`/intelligence/mcp`、`/intelligence/:view`），不改路由。
- 命令面板（workspace-command-palette）按 slug 查 `navigationTools`，注册后自动可达，无需另改。
- 不加功能开关：情报端与创作端同属登录后能力，无权限差异；若未来需要开关，走 feature_availability（不在本变更）。
