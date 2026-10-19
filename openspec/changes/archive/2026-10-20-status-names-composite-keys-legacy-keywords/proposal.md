# Why

根据 `prd-audit-9.md` 第 9 轮审计，还有 4 项实现细节与 PRD 命名或约束粒度存在偏差（`D`）：
1. **PRD §5.2 状态枚举**：PRD 定义外部任务状态为 `queued/running/retrying/completed/partial/failed/cancelled`（7 态），当前内部使用 `succeeded`/`retry_wait`/`leased`/`cancel_requested` 等 10 态而未暴露标准 7 态 `prd_status` 映射及过滤别名。
2. **PRD §6.3 同源去重唯一键**：PRD 要求 `flow_documents` 同源去重键包含 `workspace_id`（`UNIQUE (tenant_id, workspace_id, source_id, external_id)`），当前仅定义了 `UNIQUE (tenant_id, source_id, external_id)`。
3. **PRD §7.2 情报库内部复合作用域外键**：PRD 要求 `flow_intel` 内部子表使用带 `(tenant_id, workspace_id, ...)` 的复合作用域外键，当前仅定义了单列 `REFERENCES flow_sources(id)` / `REFERENCES flow_documents(id)`。
4. **PRD §13.3 旧库关键词导入**：`legacy-import.ts` 默认跳过 `keywords` 表，缺少按需将旧 `keywords` 转换为待审批监控预览（`flow_monitor_previews`）并记录 `flow_legacy_id_map` 的导入能力。

# What Changes

- **PRD §5.2 状态映射与过滤别名**：在 `services/loom-engine/flow/engine.ts` 与 `backend/internal/flow/events.go` 中新增 `PRD_OPERATION_STATUSES`、`prdOperationStatus` / `PRDOperationStatus`，在 `getOperation` 与 `listJobs` 中同步返回 `prd_status`（7 态），并支持按 `completed` / `retrying` / `pending` 过滤任务列表。
- **PRD §6.3 & §7.2 复合唯一键与复合作用域外键**：新增迁移 `services/loom-engine/flow/migrations/0018_composite_keys_and_fk.sql`，为 `flow_documents` 补齐 `UNIQUE (tenant_id, workspace_id, source_id, external_id)`，为 `flow_sources` / `flow_documents` / `flow_monitors` / `flow_reports` 补齐复合作用域唯一键，并为 `flow_source_cursors`、`flow_collection_runs`、`flow_documents`、`flow_document_revisions`、`flow_document_chunks`、`flow_evidence`、`flow_monitor_revisions`、`flow_claims`、`flow_derived_jobs`、`flow_document_analyses`、`flow_document_embeddings` 补齐复合作用域外键；同步更新 `engine.ts` 与 `legacy-import.ts` 的 `ON CONFLICT (tenant_id, workspace_id, source_id, external_id)`。
- **PRD §13.3 旧关键词导入为监控预览**：在 `services/loom-engine/flow/tools/legacy-import.ts` 的 `importLegacy` 中支持 `importKeywords?: boolean`（及 CLI `--import-keywords`），启用时将旧 `keywords` 表记录转换为当前租户/工作空间的 `flow_monitor_previews`（关联已导入的 `source_ids`）并写入 `flow_legacy_id_map`，默认保持跳过语义以兼容既有调用。

# Impact

- `services/loom-engine/flow/migrations/0018_composite_keys_and_fk.sql`
- `services/loom-engine/flow/engine.ts`
- `services/loom-engine/flow/tools/legacy-import.ts`
- `services/loom-engine/flow/gaps9.test.ts`
- `backend/internal/flow/events.go`
- `backend/internal/flow/p7_test.go`
- `docs/implementation-status.md`
