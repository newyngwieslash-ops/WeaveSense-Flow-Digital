-- A13: rate limits back off exponentially; credential failures pause the source (no infinite retry).
ALTER TABLE flow_sources ADD COLUMN IF NOT EXISTS backoff_until TIMESTAMPTZ;
ALTER TABLE flow_sources ADD COLUMN IF NOT EXISTS paused_reason TEXT
