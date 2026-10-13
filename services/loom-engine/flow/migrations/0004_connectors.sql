-- P4: Telegram / WeChat sources, connector cursors, media status, acceptance records.
ALTER TABLE flow_sources DROP CONSTRAINT IF EXISTS flow_sources_kind_check;
ALTER TABLE flow_sources ADD CONSTRAINT flow_sources_kind_check CHECK (kind IN ('rss','telegram','wechat'));
ALTER TABLE flow_sources ADD COLUMN IF NOT EXISTS config JSONB NOT NULL DEFAULT '{}';
ALTER TABLE flow_sources ADD COLUMN IF NOT EXISTS revoked_reason TEXT;
ALTER TABLE flow_source_cursors ADD COLUMN IF NOT EXISTS last_external_id TEXT;
ALTER TABLE flow_documents ADD COLUMN IF NOT EXISTS media JSONB NOT NULL DEFAULT '[]';
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS collector TEXT;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS provider TEXT;
CREATE TABLE IF NOT EXISTS flow_connector_samples (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  source_id TEXT NOT NULL,
  run_id TEXT NOT NULL,
  collector TEXT NOT NULL,
  provider TEXT,
  mode TEXT NOT NULL CHECK (mode IN ('live','fixture')),
  items INTEGER NOT NULL,
  media_unsupported INTEGER NOT NULL DEFAULT 0,
  sample JSONB NOT NULL,
  recorded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (run_id)
)
