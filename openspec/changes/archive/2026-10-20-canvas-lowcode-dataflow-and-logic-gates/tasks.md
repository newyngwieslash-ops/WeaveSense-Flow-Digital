# Tasks: 画布低代码数据源节点与逻辑门节点（Data Source + Logic Gates）

- [x] 1. 在 `web/src/lib/canvas/dataflow-evaluator.ts` 与 `web/src/types/canvas.ts` 中实现数据源解析、集合/条件逻辑运算、防环求值与多端口几何计算
- [x] 2. 实现 `DataSourceNode`（`data-source-node.tsx`）与 `LogicNode`（`logic-node.tsx`）组件，并在画布节点注册表、添加菜单、连线控制器与下游生成门控中完成集成
- [x] 3. 在后端 `backend/internal/canvas/capability/` 与 `backend/internal/app/cloud_agent_*.go` 中注册 `data-source` 和 `logic` 节点能力及自动排版支持
- [x] 4. 编写并通过 `web/test/canvas-dataflow.test.ts`、`backend/internal/canvas/capability/registry_test.go`、`bun run lint` 与 `bun run build` 验证
