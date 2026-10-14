-- §13.3 Loom legacy import: stable old_id → new_id mapping per tenant/workspace.
-- Re-running an import reuses existing mappings, so it never duplicates rows.
CREATE TABLE IF NOT EXISTS flow_legacy_id_map (
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  legacy_table TEXT NOT NULL,
  old_id TEXT NOT NULL,
  new_id TEXT,
  status TEXT NOT NULL CHECK (status IN ('imported','skipped')),
  reason TEXT,
  import_id TEXT NOT NULL,
  imported_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, workspace_id, legacy_table, old_id)
);
ALTER TABLE flow_legacy_id_map ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_legacy_id_map FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON flow_legacy_id_map;
CREATE POLICY tenant_isolation ON flow_legacy_id_map USING (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true));
