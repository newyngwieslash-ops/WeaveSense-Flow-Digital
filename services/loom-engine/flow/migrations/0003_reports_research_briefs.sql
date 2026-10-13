-- P3: research runs, reports with citation maps, claims, periodic brief schedules.
CREATE TABLE IF NOT EXISTS flow_research_runs (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  parent_run_id TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('research','brief')),
  question TEXT NOT NULL DEFAULT '',
  skill JSONB NOT NULL DEFAULT '{}',
  evidence_scope JSONB NOT NULL DEFAULT '{}',
  budget JSONB NOT NULL DEFAULT '{}',
  status TEXT NOT NULL DEFAULT 'running' CHECK (status IN ('running','succeeded','failed')),
  output JSONB,
  error TEXT,
  idempotency_key TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ,
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS flow_research_parent ON flow_research_runs (tenant_id, parent_run_id);
CREATE TABLE IF NOT EXISTS flow_reports (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'report' CHECK (kind IN ('report','brief')),
  title TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '',
  skill JSONB NOT NULL DEFAULT '{}',
  material_scope JSONB NOT NULL DEFAULT '{}',
  coverage JSONB NOT NULL DEFAULT '{}',
  research_run_id TEXT,
  brief_schedule_id TEXT,
  scheduled_at TIMESTAMPTZ,
  created_by TEXT NOT NULL,
  idempotency_key TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE UNIQUE INDEX IF NOT EXISTS flow_reports_brief_window ON flow_reports (tenant_id, brief_schedule_id, scheduled_at) WHERE brief_schedule_id IS NOT NULL;
CREATE TABLE IF NOT EXISTS flow_claims (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  report_id TEXT NOT NULL REFERENCES flow_reports(id),
  idx INTEGER NOT NULL,
  text TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('source_statement','cross_source_verified','model_inference')),
  citations JSONB NOT NULL DEFAULT '[]',
  UNIQUE (report_id, idx)
);
CREATE TABLE IF NOT EXISTS flow_brief_previews (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  spec JSONB NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  applied_schedule_id TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS flow_brief_schedules (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  name TEXT NOT NULL,
  query TEXT NOT NULL,
  source_ids TEXT[] NOT NULL,
  cron TEXT NOT NULL,
  timezone TEXT NOT NULL,
  window_hours INTEGER NOT NULL DEFAULT 24,
  skill JSONB NOT NULL DEFAULT '{}',
  next_run_at TIMESTAMPTZ NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT true,
  grant_actor TEXT NOT NULL,
  policy_version TEXT NOT NULL,
  approval_id TEXT,
  idempotency_key TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, idempotency_key)
);
CREATE INDEX IF NOT EXISTS flow_brief_due ON flow_brief_schedules (next_run_at) WHERE enabled
