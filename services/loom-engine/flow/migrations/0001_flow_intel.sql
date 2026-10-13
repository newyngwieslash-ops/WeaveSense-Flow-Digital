-- WeaveSense-Flow intel DB (flow_intel). Every business row is tenant scoped;
-- unique constraints include tenant_id so equal feeds/keywords never collide.
CREATE TABLE IF NOT EXISTS flow_sources (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('rss')),
  url TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  revision INTEGER NOT NULL DEFAULT 1,
  status TEXT NOT NULL DEFAULT 'active',
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, workspace_id, url)
);
CREATE TABLE IF NOT EXISTS flow_source_cursors (
  source_id TEXT PRIMARY KEY REFERENCES flow_sources(id),
  tenant_id TEXT NOT NULL,
  last_published_at TIMESTAMPTZ,
  last_run_id TEXT,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS flow_monitors (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  name TEXT NOT NULL,
  keywords TEXT[] NOT NULL DEFAULT '{}',
  source_ids TEXT[] NOT NULL DEFAULT '{}',
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS flow_schedules (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  source_ids TEXT[] NOT NULL,
  interval_seconds INTEGER NOT NULL CHECK (interval_seconds >= 60),
  next_run_at TIMESTAMPTZ NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT true,
  grant_actor TEXT NOT NULL,
  policy_version TEXT NOT NULL,
  revision INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS flow_schedules_due ON flow_schedules (next_run_at) WHERE enabled;
CREATE TABLE IF NOT EXISTS flow_collection_runs (
  id TEXT PRIMARY KEY,                       -- job_id
  operation_id TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  source_id TEXT NOT NULL REFERENCES flow_sources(id),
  idempotency_key TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  schedule_id TEXT,
  scheduled_at TIMESTAMPTZ,
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','succeeded','failed','cancelled')),
  attempts INTEGER NOT NULL DEFAULT 0,
  lease_owner TEXT,
  lease_expires_at TIMESTAMPTZ,
  fencing_token BIGINT NOT NULL DEFAULT 0,
  items_new INTEGER NOT NULL DEFAULT 0,
  items_revised INTEGER NOT NULL DEFAULT 0,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ,
  UNIQUE (tenant_id, idempotency_key, source_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS flow_runs_schedule_window
  ON flow_collection_runs (tenant_id, schedule_id, scheduled_at, source_id) WHERE schedule_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS flow_runs_claim ON flow_collection_runs (status, lease_expires_at);
CREATE TABLE IF NOT EXISTS flow_documents (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  source_id TEXT NOT NULL REFERENCES flow_sources(id),
  external_id TEXT NOT NULL,
  url TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  published_at TIMESTAMPTZ,
  current_revision INTEGER NOT NULL DEFAULT 1,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, source_id, external_id)
);
CREATE TABLE IF NOT EXISTS flow_document_revisions (
  document_id TEXT NOT NULL REFERENCES flow_documents(id),
  revision INTEGER NOT NULL,
  tenant_id TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  body TEXT NOT NULL DEFAULT '',
  collected_by_run TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (document_id, revision)
);
CREATE TABLE IF NOT EXISTS flow_outbox (
  event_id TEXT PRIMARY KEY,
  sequence BIGSERIAL UNIQUE,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  operation_id TEXT NOT NULL,
  job_id TEXT NOT NULL,
  type TEXT NOT NULL,
  payload JSONB NOT NULL,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  delivered_at TIMESTAMPTZ,
  attempts INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS flow_outbox_pending ON flow_outbox (sequence) WHERE delivered_at IS NULL;
