-- §6.3 raw HTTP response archive per collection run: scoped object key, sha256 digest, byte size and response metadata.
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS raw_response_key TEXT;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS raw_response_sha256 TEXT;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS raw_response_bytes INTEGER;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS raw_response_meta JSONB;
