# workspace-navigation Delta

## ADDED Requirements

### Requirement: 侧边栏情报入口

工作台侧边栏 MUST 提供「情报」入口，导航至 `/intelligence` 情报工作台。

#### Scenario: 登录用户从侧边栏进入情报工作台

- **WHEN** 已登录用户查看工作台侧边栏
- **THEN** 「创作空间」分组显示「情报」项（Radar 图标）
- **AND** 点击后进入 `/intelligence`，页面展示 9 项导航 Tabs
- **AND** 悬停或预加载逻辑与其他侧边栏项一致（preloadWorkspaceRoute 命中 intelligence 映射）
