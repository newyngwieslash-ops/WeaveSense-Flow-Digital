-- §13.3 legacy import archive: legacy analyses, knowledge notes and media objects are kept
-- read-only, linked to the imported documents. They are NOT evidence-bound reports (no
-- verified citations), so they never pose as Flow claims.
CREATE TABLE IF NOT EXISTS flow_legacy_archive (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('analysis','note','object')),
  old_id TEXT NOT NULL,
  document_ids TEXT[] NOT NULL DEFAULT '{}',
  title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  payload JSONB NOT NULL DEFAULT '{}',
  import_id TEXT NOT NULL,
  legacy_created_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, workspace_id, kind, old_id)
);
CREATE INDEX IF NOT EXISTS flow_legacy_archive_docs ON flow_legacy_archive USING gin (document_ids);
ALTER TABLE flow_legacy_archive ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_legacy_archive FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON flow_legacy_archive;
CREATE POLICY tenant_isolation ON flow_legacy_archive USING (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true));
