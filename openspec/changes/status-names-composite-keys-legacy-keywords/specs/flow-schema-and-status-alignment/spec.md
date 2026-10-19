# Spec: flow-schema-and-status-alignment

## ADDED Requirements

### Requirement: PRD §5.2 7-State Operation Status Alias
The system SHALL expose `prd_status` in the PRD §5.2 7-state set (`queued`, `running`, `retrying`, `completed`, `partial`, `failed`, `cancelled`) on operations and jobs while preserving existing internal status fields.

#### Scenario: Completed and retrying operation status mapping
- **WHEN** `getOperation` or `listJobs` is called for a succeeded or retry_wait job
- **THEN** `prd_status` is returned as `"completed"` or `"retrying"` respectively, and `listJobs` accepts `"completed"` / `"retrying"` filter values

### Requirement: PRD §6.3 & §7.2 Composite Uniqueness and Scoped Foreign Keys
The intelligence database SHALL enforce `UNIQUE (tenant_id, workspace_id, source_id, external_id)` on `flow_documents` and composite tenant/workspace foreign keys across child tables.

#### Scenario: Cross-workspace or cross-tenant foreign key violation is rejected
- **WHEN** a child row in `flow_documents`, `flow_collection_runs`, or `flow_evidence` references a parent ID with a mismatched `tenant_id` or `workspace_id`
- **THEN** PostgreSQL rejects the write with a foreign key constraint violation

### Requirement: PRD §13.3 Legacy Keyword Import as Monitor Previews
The legacy importer SHALL support an opt-in `importKeywords` mode that converts legacy `keywords` rows into tenant/workspace-scoped monitor previews (`flow_monitor_previews`) recorded in `flow_legacy_id_map`.

#### Scenario: Opt-in legacy keyword import creates monitor preview
- **WHEN** `importLegacy` is invoked with `{ apply: true, importKeywords: true }`
- **THEN** legacy keywords are imported as `flow_monitor_previews` rows and mapped in `flow_legacy_id_map`
