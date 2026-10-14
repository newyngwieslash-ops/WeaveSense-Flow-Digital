-- §7.3 workspace term aliases; §11 REST research runs can be cancelled.
CREATE TABLE IF NOT EXISTS flow_term_aliases (
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  term TEXT NOT NULL,
  alias TEXT NOT NULL,
  created_by TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, workspace_id, term, alias)
);
ALTER TABLE flow_term_aliases ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_term_aliases FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON flow_term_aliases;
CREATE POLICY tenant_isolation ON flow_term_aliases USING (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true));
ALTER TABLE flow_research_runs DROP CONSTRAINT IF EXISTS flow_research_runs_status_check;
ALTER TABLE flow_research_runs ADD CONSTRAINT flow_research_runs_status_check CHECK (status IN ('queued','running','succeeded','failed','cancelled'));
ALTER TABLE flow_research_runs ADD COLUMN IF NOT EXISTS cancel_reason TEXT;
ALTER TABLE flow_sources ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
