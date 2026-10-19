# Proposal: 侧边栏新增「情报」入口

## 动机

情报工作台（Intelligence，`/intelligence`）功能已完整上线（路由存在、API 链路冒烟通过、页面内置 9 项导航 Tabs），但侧边栏注册表（`web/src/constant/navigation-tools.ts`）和 `buildNav`（`web/src/components/layout/workspace-sidebar-nav.tsx`）均未注册入口，用户只能手动输入直链访问。

## 目标

在侧边栏「创作空间」分组新增「情报」入口，点击进入 `/intelligence`；行为与现有项一致（hover 预加载、命令面板可达、折叠态图标语义）。

## 范围与边界

- `web/src/constant/navigation-tools.ts`：注册表加 `intelligence` slug（图标 + 分组「创作空间」）
- `web/src/components/layout/workspace-sidebar-nav.tsx`：`buildNav` 第一组加 `{ ...toolItem("intelligence", "/intelligence"), title: "情报" }`
- `web/src/lib/workspace-route-modules.ts`：`preloadWorkspaceRoute` 加 intelligence 映射（hover 预加载与其他项对齐）

不做：设置页「自定义区域」显隐/排序面板（features.mdx 描述的该能力当前代码本就不存在，属另一个独立变更）。

## 验收

1. 登录后侧边栏出现「情报」项，点击进入 `/intelligence`，页面 9 项 Tabs 正常
2. `bun run build` 通过；现有 `bun test` 不回归
