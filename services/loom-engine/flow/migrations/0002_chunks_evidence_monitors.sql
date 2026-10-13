-- P2: chunked documents for full-text search + evidence, monitor previews/apply.
CREATE TABLE IF NOT EXISTS flow_document_chunks (
  document_id TEXT NOT NULL REFERENCES flow_documents(id),
  revision INTEGER NOT NULL,
  chunk_index INTEGER NOT NULL,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  source_id TEXT NOT NULL,
  char_start INTEGER NOT NULL,
  char_end INTEGER NOT NULL,
  text TEXT NOT NULL,
  published_at TIMESTAMPTZ,
  tsv tsvector NOT NULL,
  PRIMARY KEY (document_id, revision, chunk_index)
);
CREATE INDEX IF NOT EXISTS flow_chunks_scope ON flow_document_chunks (tenant_id, workspace_id, published_at DESC);
CREATE INDEX IF NOT EXISTS flow_chunks_tsv ON flow_document_chunks USING GIN (tsv);
CREATE TABLE IF NOT EXISTS flow_evidence (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  document_id TEXT NOT NULL REFERENCES flow_documents(id),
  revision INTEGER NOT NULL,
  char_start INTEGER NOT NULL,
  char_end INTEGER NOT NULL,
  quote TEXT NOT NULL,
  created_by TEXT NOT NULL,
  idempotency_key TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE TABLE IF NOT EXISTS flow_monitor_previews (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  spec JSONB NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  applied_monitor_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE flow_monitors ADD COLUMN IF NOT EXISTS interval_seconds INTEGER;
ALTER TABLE flow_monitors ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'UTC';
ALTER TABLE flow_monitors ADD COLUMN IF NOT EXISTS schedule_id TEXT;
ALTER TABLE flow_monitors ADD COLUMN IF NOT EXISTS approval_id TEXT;
ALTER TABLE flow_monitors ADD COLUMN IF NOT EXISTS idempotency_key TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS flow_monitors_idem ON flow_monitors (tenant_id, idempotency_key);
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS cancel_requested BOOLEAN NOT NULL DEFAULT false;
