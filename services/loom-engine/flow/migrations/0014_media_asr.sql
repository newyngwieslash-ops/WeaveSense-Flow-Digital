-- §6.4 ASR targets (audio/video attachments): unique per (document, media object); only failed targets are retried.
CREATE TABLE IF NOT EXISTS flow_media_asr (
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  document_id TEXT NOT NULL,
  object_key TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','succeeded','failed','unsupported')),
  attempts INTEGER NOT NULL DEFAULT 0,
  seconds INTEGER NOT NULL DEFAULT 0,
  language TEXT,
  text_digest TEXT,
  revision INTEGER,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, document_id, object_key)
);
CREATE INDEX IF NOT EXISTS flow_media_asr_due ON flow_media_asr (status, attempts);
ALTER TABLE flow_media_asr ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_media_asr FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON flow_media_asr;
CREATE POLICY tenant_isolation ON flow_media_asr USING (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true));
