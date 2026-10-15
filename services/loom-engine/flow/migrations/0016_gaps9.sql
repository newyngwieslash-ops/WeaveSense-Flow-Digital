-- Audit-8 gaps.
-- §5.1 monitors carry their budget and notification targets; scope/budget/target
-- expansion needs a new approval (history in flow_monitor_revisions).
ALTER TABLE flow_monitors ADD COLUMN IF NOT EXISTS budget_micro_per_window BIGINT NOT NULL DEFAULT 0;
ALTER TABLE flow_monitors ADD COLUMN IF NOT EXISTS notification_target_ids TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE flow_monitors ADD COLUMN IF NOT EXISTS extraction_method TEXT NOT NULL DEFAULT 'feed';
ALTER TABLE flow_monitors ADD COLUMN IF NOT EXISTS version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE flow_monitors ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();
CREATE TABLE IF NOT EXISTS flow_monitor_revisions (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  monitor_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  spec JSONB NOT NULL,
  expanded BOOLEAN NOT NULL,
  expansion TEXT[] NOT NULL DEFAULT '{}',
  approval_id TEXT,
  actor TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (monitor_id, version)
);
ALTER TABLE flow_monitor_revisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_monitor_revisions FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON flow_monitor_revisions;
CREATE POLICY tenant_isolation ON flow_monitor_revisions USING (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true));

-- §7.3 evidence carries the snippet hash and document URL it was taken from; citation checks recompute both.
ALTER TABLE flow_evidence ADD COLUMN IF NOT EXISTS snippet_hash TEXT;
ALTER TABLE flow_evidence ADD COLUMN IF NOT EXISTS url TEXT;
UPDATE flow_evidence SET snippet_hash = encode(sha256(convert_to(quote, 'UTF8')), 'hex') WHERE snippet_hash IS NULL;
UPDATE flow_evidence e SET url = d.url FROM flow_documents d WHERE e.url IS NULL AND d.id = e.document_id;

-- §6.3 raw response: the connector item as received (capped) + its sha256 digest, per revision;
-- batch digest per run. §5 extraction method of each revision (feed | extract | reader | transcript | ocr | asr).
ALTER TABLE flow_document_revisions ADD COLUMN IF NOT EXISTS raw_item JSONB;
ALTER TABLE flow_document_revisions ADD COLUMN IF NOT EXISTS raw_digest TEXT;
ALTER TABLE flow_document_revisions ADD COLUMN IF NOT EXISTS extraction TEXT NOT NULL DEFAULT 'feed';
UPDATE flow_document_revisions SET extraction = split_part(collected_by_run, ':', 1)
  WHERE collected_by_run ~ '^(extract|reader|transcript|ocr|asr):';
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS raw_digest TEXT;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS raw_items INTEGER;

-- §6.4 embed and analyze are their own jobs, unique per (document, revision, kind, content hash):
-- an unchanged body is never re-analysed and frequent ticks do not spend twice.
CREATE TABLE IF NOT EXISTS flow_derived_jobs (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  document_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('embed','analyze')),
  content_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','succeeded','failed','skipped')),
  attempts INTEGER NOT NULL DEFAULT 0,
  provider TEXT,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, document_id, kind, content_hash)
);
CREATE INDEX IF NOT EXISTS flow_derived_jobs_due ON flow_derived_jobs (kind, status, attempts);
CREATE TABLE IF NOT EXISTS flow_document_analyses (
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  document_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  content_hash TEXT NOT NULL,
  analysis JSONB NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, document_id, revision)
);
CREATE TABLE IF NOT EXISTS flow_document_embeddings (
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  document_id TEXT NOT NULL,
  revision INTEGER NOT NULL,
  chunk_index INTEGER NOT NULL,
  provider TEXT NOT NULL,
  dims INTEGER NOT NULL,
  vector REAL[] NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, document_id, revision, chunk_index, provider)
);
ALTER TABLE flow_derived_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_derived_jobs FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON flow_derived_jobs;
CREATE POLICY tenant_isolation ON flow_derived_jobs USING (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true));
ALTER TABLE flow_document_analyses ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_document_analyses FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON flow_document_analyses;
CREATE POLICY tenant_isolation ON flow_document_analyses USING (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true));
ALTER TABLE flow_document_embeddings ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_document_embeddings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON flow_document_embeddings;
CREATE POLICY tenant_isolation ON flow_document_embeddings USING (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true));
