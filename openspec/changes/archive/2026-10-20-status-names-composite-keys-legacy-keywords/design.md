# Design: 状态命名映射、复合作用域外键与旧关键词导入（PRD §5.2 / §6.3 / §7.2 / §13.3）

## 1. PRD §5.2 7 态操作状态映射
- 保持内部存储与 `status` 字段向后兼容（`succeeded` / `retry_wait` / `leased` 等），新增 `PRD_OPERATION_STATUSES = ["queued", "running", "retrying", "completed", "partial", "failed", "cancelled"]` 与 `prdOperationStatus(status)`（以及 Go 侧 `PRDOperationStatus(status)`）。
- `Engine.getOperation` 与 `Engine.listJobs` 在返回对象上附加 `prd_status`，同时 `listJobs` 支持传入 `status: "completed" | "retrying" | "pending"` 自动映射到存储态查询。

## 2. PRD §6.3 & §7.2 复合唯一键与复合作用域外键
- 新增 `0018_composite_keys_and_fk.sql`：
  - `flow_documents` 增加 `UNIQUE (tenant_id, workspace_id, source_id, external_id)`，同时保留原 `UNIQUE (tenant_id, source_id, external_id)`。
  - 父表 `flow_sources`、`flow_documents`、`flow_monitors`、`flow_reports` 增加包含 `tenant_id`（及 `workspace_id`）的复合唯一约束。
  - 子表 `flow_source_cursors`、`flow_collection_runs`、`flow_documents`、`flow_document_revisions`、`flow_document_chunks`、`flow_evidence`、`flow_monitor_revisions`、`flow_claims`、`flow_derived_jobs`、`flow_document_analyses`、`flow_document_embeddings` 增加复合作用域 `FOREIGN KEY`，在数据库层阻止跨租户/跨工作空间的孤儿或越权关联。

## 3. PRD §13.3 旧库关键词按需导入为监控预览
- `importLegacy(legacy, target, scope, opts)` 新增可选参数 `importKeywords?: boolean`（CLI 支持 `--import-keywords`）。
- 未开启时保持原 `skipped` 行为；开启且存在已导入来源时，为每条旧 `keywords` 调用 `engine.previewMonitor` 生成待审批监控预览（`mpv_*`），并将 `old_id -> preview_id` 写入 `flow_legacy_id_map`。
