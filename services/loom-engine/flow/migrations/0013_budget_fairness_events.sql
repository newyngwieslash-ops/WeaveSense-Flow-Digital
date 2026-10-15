-- PRD §6.1/§9.2: schedule budget per window, reserved with the platform before the window's jobs exist.
ALTER TABLE flow_schedules ADD COLUMN IF NOT EXISTS budget_micro_per_window BIGINT NOT NULL DEFAULT 0;
ALTER TABLE flow_schedules ADD COLUMN IF NOT EXISTS service_identity TEXT NOT NULL DEFAULT 'loom-engine';
-- PRD §12.2: active | blocked (admission failed: grant revoked / budget denied; reason shown) | disabled.
ALTER TABLE flow_schedules ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT 'active';
ALTER TABLE flow_schedules ADD COLUMN IF NOT EXISTS blocked_reason TEXT;
ALTER TABLE flow_schedules ADD COLUMN IF NOT EXISTS blocked_at TIMESTAMPTZ;
ALTER TABLE flow_schedules DROP CONSTRAINT IF EXISTS flow_schedules_state_check;
ALTER TABLE flow_schedules ADD CONSTRAINT flow_schedules_state_check CHECK (state IN ('active','blocked','disabled'));
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS budget_reservation_id TEXT;
ALTER TABLE flow_collection_runs ADD COLUMN IF NOT EXISTS budget_closed_at TIMESTAMPTZ;
-- PRD §7.2: event-occurrence time, separate from published_at / collected (created_at) / analysis time.
ALTER TABLE flow_documents ADD COLUMN IF NOT EXISTS event_occurred_at TIMESTAMPTZ;
ALTER TABLE flow_documents ADD COLUMN IF NOT EXISTS event_occurred_at_source TEXT;
-- PRD §9.3: per-tenant concurrency caps (fair queue uses LOOM_TENANT_MAX_CONCURRENCY when no row).
CREATE TABLE IF NOT EXISTS flow_tenant_limits (
  tenant_id TEXT PRIMARY KEY,
  max_concurrent_jobs INTEGER NOT NULL CHECK (max_concurrent_jobs > 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
ALTER TABLE flow_tenant_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_tenant_limits FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON flow_tenant_limits;
CREATE POLICY tenant_isolation ON flow_tenant_limits USING (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true));
CREATE INDEX IF NOT EXISTS flow_runs_active_tenant ON flow_collection_runs (tenant_id) WHERE status IN ('leased','running');
-- PRD §8.1: research runs record the parent run's capability snapshot (execution-context claim).
ALTER TABLE flow_research_runs ADD COLUMN IF NOT EXISTS capability_snapshot_id TEXT;
