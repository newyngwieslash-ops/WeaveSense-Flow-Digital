# Tasks

- [x] 1. 在 `services/loom-engine/flow/engine.ts` 与 `backend/internal/flow/events.go` 中实现 PRD §5.2 7 态操作状态映射（`prdOperationStatus` / `PRDOperationStatus`）及 `listJobs` 状态过滤别名
- [x] 2. 新增 `services/loom-engine/flow/migrations/0018_composite_keys_and_fk.sql` 补齐 PRD §6.3 四元组唯一键与 PRD §7.2 复合作用域外键，并更新 `engine.ts` / `legacy-import.ts`
- [x] 3. 在 `services/loom-engine/flow/tools/legacy-import.ts` 中支持 `importKeywords` 选项将旧关键词导入为监控预览，并补充单元与集成测试及文档更新
