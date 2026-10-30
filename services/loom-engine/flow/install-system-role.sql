-- DT018 controlled installer: execute as the dedicated migration login AFTER 0019.
-- Bootstrap must precreate flow_intel_guard NOLOGIN and flow_intel_system LOGIN,
-- and grant migration SET ROLE only (INHERIT FALSE). Never run as runtime app.
-- Caller MUST execute the whole file in one transaction (psql --single-transaction).
GRANT USAGE ON SCHEMA public TO flow_intel_guard, flow_intel_system;
GRANT CREATE ON SCHEMA public TO flow_intel_guard;
GRANT SELECT (tenant_id) ON public.flow_brief_previews TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_brief_previews;
CREATE POLICY dispatch_tenant_scan ON public.flow_brief_previews FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_brief_schedules TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_brief_schedules;
CREATE POLICY dispatch_tenant_scan ON public.flow_brief_schedules FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_claims TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_claims;
CREATE POLICY dispatch_tenant_scan ON public.flow_claims FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_collection_runs TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_collection_runs;
CREATE POLICY dispatch_tenant_scan ON public.flow_collection_runs FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_connector_samples TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_connector_samples;
CREATE POLICY dispatch_tenant_scan ON public.flow_connector_samples FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_derived_jobs TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_derived_jobs;
CREATE POLICY dispatch_tenant_scan ON public.flow_derived_jobs FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_document_analyses TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_document_analyses;
CREATE POLICY dispatch_tenant_scan ON public.flow_document_analyses FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_document_chunks TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_document_chunks;
CREATE POLICY dispatch_tenant_scan ON public.flow_document_chunks FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_document_embeddings TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_document_embeddings;
CREATE POLICY dispatch_tenant_scan ON public.flow_document_embeddings FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_document_revisions TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_document_revisions;
CREATE POLICY dispatch_tenant_scan ON public.flow_document_revisions FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_documents TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_documents;
CREATE POLICY dispatch_tenant_scan ON public.flow_documents FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_evidence TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_evidence;
CREATE POLICY dispatch_tenant_scan ON public.flow_evidence FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_legacy_archive TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_legacy_archive;
CREATE POLICY dispatch_tenant_scan ON public.flow_legacy_archive FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_legacy_id_map TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_legacy_id_map;
CREATE POLICY dispatch_tenant_scan ON public.flow_legacy_id_map FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_media_asr TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_media_asr;
CREATE POLICY dispatch_tenant_scan ON public.flow_media_asr FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_media_ocr TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_media_ocr;
CREATE POLICY dispatch_tenant_scan ON public.flow_media_ocr FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_monitor_previews TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_monitor_previews;
CREATE POLICY dispatch_tenant_scan ON public.flow_monitor_previews FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_monitor_revisions TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_monitor_revisions;
CREATE POLICY dispatch_tenant_scan ON public.flow_monitor_revisions FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_monitors TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_monitors;
CREATE POLICY dispatch_tenant_scan ON public.flow_monitors FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_outbox TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_outbox;
CREATE POLICY dispatch_tenant_scan ON public.flow_outbox FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_reports TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_reports;
CREATE POLICY dispatch_tenant_scan ON public.flow_reports FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_research_runs TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_research_runs;
CREATE POLICY dispatch_tenant_scan ON public.flow_research_runs FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_schedules TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_schedules;
CREATE POLICY dispatch_tenant_scan ON public.flow_schedules FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_source_cursors TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_source_cursors;
CREATE POLICY dispatch_tenant_scan ON public.flow_source_cursors FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_sources TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_sources;
CREATE POLICY dispatch_tenant_scan ON public.flow_sources FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_tenant_limits TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_tenant_limits;
CREATE POLICY dispatch_tenant_scan ON public.flow_tenant_limits FOR SELECT TO flow_intel_guard USING (true);
GRANT SELECT (tenant_id) ON public.flow_term_aliases TO flow_intel_guard;
DROP POLICY IF EXISTS dispatch_tenant_scan ON public.flow_term_aliases;
CREATE POLICY dispatch_tenant_scan ON public.flow_term_aliases FOR SELECT TO flow_intel_guard USING (true);
SET LOCAL ROLE flow_intel_guard;
CREATE OR REPLACE FUNCTION public.flow_dispatch_tenants(after_tenant text, page_size integer)
RETURNS TABLE (tenant_id text)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog
AS $scan$
  SELECT t.tenant_id FROM (
    SELECT tenant_id FROM public.flow_brief_previews
    UNION
    SELECT tenant_id FROM public.flow_brief_schedules
    UNION
    SELECT tenant_id FROM public.flow_claims
    UNION
    SELECT tenant_id FROM public.flow_collection_runs
    UNION
    SELECT tenant_id FROM public.flow_connector_samples
    UNION
    SELECT tenant_id FROM public.flow_derived_jobs
    UNION
    SELECT tenant_id FROM public.flow_document_analyses
    UNION
    SELECT tenant_id FROM public.flow_document_chunks
    UNION
    SELECT tenant_id FROM public.flow_document_embeddings
    UNION
    SELECT tenant_id FROM public.flow_document_revisions
    UNION
    SELECT tenant_id FROM public.flow_documents
    UNION
    SELECT tenant_id FROM public.flow_evidence
    UNION
    SELECT tenant_id FROM public.flow_legacy_archive
    UNION
    SELECT tenant_id FROM public.flow_legacy_id_map
    UNION
    SELECT tenant_id FROM public.flow_media_asr
    UNION
    SELECT tenant_id FROM public.flow_media_ocr
    UNION
    SELECT tenant_id FROM public.flow_monitor_previews
    UNION
    SELECT tenant_id FROM public.flow_monitor_revisions
    UNION
    SELECT tenant_id FROM public.flow_monitors
    UNION
    SELECT tenant_id FROM public.flow_outbox
    UNION
    SELECT tenant_id FROM public.flow_reports
    UNION
    SELECT tenant_id FROM public.flow_research_runs
    UNION
    SELECT tenant_id FROM public.flow_schedules
    UNION
    SELECT tenant_id FROM public.flow_source_cursors
    UNION
    SELECT tenant_id FROM public.flow_sources
    UNION
    SELECT tenant_id FROM public.flow_tenant_limits
    UNION
    SELECT tenant_id FROM public.flow_term_aliases
  ) t
  WHERE t.tenant_id COLLATE "C" > after_tenant COLLATE "C" AND t.tenant_id ~ '^[A-Za-z0-9_-]{1,128}$'
    AND page_size BETWEEN 1 AND 100
  ORDER BY t.tenant_id COLLATE "C"
  LIMIT least(greatest(page_size, 0), 100)
$scan$;
REVOKE ALL ON FUNCTION public.flow_dispatch_tenants(text, integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.flow_dispatch_tenants(text, integer) TO flow_intel_system;
RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM flow_intel_guard;
