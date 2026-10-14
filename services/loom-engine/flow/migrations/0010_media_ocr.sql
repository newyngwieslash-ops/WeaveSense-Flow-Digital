-- §6.4 OCR targets: unique per (document, media object); only failed targets are retried.
CREATE TABLE IF NOT EXISTS flow_media_ocr (
  tenant_id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  document_id TEXT NOT NULL,
  object_key TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','succeeded','failed','unsupported')),
  attempts INTEGER NOT NULL DEFAULT 0,
  pages INTEGER NOT NULL DEFAULT 0,
  text_digest TEXT,
  revision INTEGER,
  error TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, document_id, object_key)
);
CREATE INDEX IF NOT EXISTS flow_media_ocr_due ON flow_media_ocr (status, attempts);
ALTER TABLE flow_media_ocr ENABLE ROW LEVEL SECURITY;
ALTER TABLE flow_media_ocr FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS tenant_isolation ON flow_media_ocr;
CREATE POLICY tenant_isolation ON flow_media_ocr USING (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true)) WITH CHECK (coalesce(current_setting('app.tenant_id', true), '') = '' OR tenant_id = current_setting('app.tenant_id', true));
