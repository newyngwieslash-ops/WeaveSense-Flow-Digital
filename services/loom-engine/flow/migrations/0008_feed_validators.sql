-- loom collection-reliability: conditional feed requests and per-entry rejection evidence.
-- Validators live on the tenant-scoped cursor row (RLS policy unchanged) and are written in
-- the same commit as the cursor, so a failed run can never skip unseen content.
ALTER TABLE flow_source_cursors ADD COLUMN IF NOT EXISTS feed_etag TEXT;
ALTER TABLE flow_source_cursors ADD COLUMN IF NOT EXISTS feed_last_modified TEXT;
ALTER TABLE flow_source_cursors ADD COLUMN IF NOT EXISTS feed_validator_identity TEXT;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS items_rejected INTEGER NOT NULL DEFAULT 0;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS not_modified BOOLEAN NOT NULL DEFAULT false
