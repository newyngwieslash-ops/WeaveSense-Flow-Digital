-- PRD §6.2 collection job state machine:
--   queued (= PRD "pending") → leased → running → succeeded | partial
--   retryable failure → retry_wait → (retry_at) back to leased; over the limit → failed
--   cancel → cancel_requested (while leased/running) → cancelled once the worker stops
--   unknown external outcome → reconciliation_required → reconciled to succeeded or re-queued
ALTER TABLE flow_collection_runs DROP CONSTRAINT IF EXISTS flow_collection_runs_status_check;
ALTER TABLE flow_collection_runs ADD CONSTRAINT flow_collection_runs_status_check CHECK (status IN
  ('queued','leased','running','retry_wait','partial','reconciliation_required','cancel_requested','cancelled','succeeded','failed'));
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS max_attempts INTEGER NOT NULL DEFAULT 3;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS retry_at TIMESTAMPTZ;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS partial_reason TEXT;
CREATE INDEX IF NOT EXISTS flow_runs_retry ON flow_collection_runs (retry_at) WHERE status = 'retry_wait';
-- PRD §6.1 schedules: kind (interval | cron), IANA timezone, misfire_policy.
ALTER TABLE flow_schedules ALTER COLUMN interval_seconds DROP NOT NULL;
ALTER TABLE flow_schedules ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'interval';
ALTER TABLE flow_schedules ADD COLUMN IF NOT EXISTS cron TEXT;
ALTER TABLE flow_schedules ADD COLUMN IF NOT EXISTS timezone TEXT NOT NULL DEFAULT 'Asia/Shanghai';
ALTER TABLE flow_schedules ADD COLUMN IF NOT EXISTS misfire_policy TEXT NOT NULL DEFAULT 'coalesce';
ALTER TABLE flow_schedules DROP CONSTRAINT IF EXISTS flow_schedules_kind_check;
ALTER TABLE flow_schedules ADD CONSTRAINT flow_schedules_kind_check CHECK (
  (kind = 'interval' AND interval_seconds IS NOT NULL AND cron IS NULL) OR (kind = 'cron' AND cron IS NOT NULL));
ALTER TABLE flow_schedules DROP CONSTRAINT IF EXISTS flow_schedules_misfire_check;
ALTER TABLE flow_schedules ADD CONSTRAINT flow_schedules_misfire_check CHECK (misfire_policy IN ('coalesce','skip','catch_up'))
