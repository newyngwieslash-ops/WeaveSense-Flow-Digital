-- 农业领域数据库缺省拒绝边界；不推断或回填任何旧数据租户。
-- 仅 flow_geo_migrate 在独立 flow_geo 数据库显式执行。
BEGIN;
DO $preflight$
BEGIN
 IF current_database() <> 'flow_geo' OR current_user <> 'flow_geo_migrate' THEN
  RAISE EXCEPTION 'domain boundary requires geo migration ownership';
 END IF;
 IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=current_user
            AND (rolsuper OR rolbypassrls)) THEN
  RAISE EXCEPTION 'migration role must not bypass row security';
 END IF;
END $preflight$;
ALTER DEFAULT PRIVILEGES FOR ROLE flow_geo_migrate IN SCHEMA public
 REVOKE ALL ON TABLES FROM flow_geo_app;
ALTER DEFAULT PRIVILEGES FOR ROLE flow_geo_migrate IN SCHEMA public
 REVOKE ALL ON SEQUENCES FROM flow_geo_app;

-- Preserve the old identity migrations as historical inputs, but intersect their
-- permissive tenant-list policies with the current ordinary business boundary.
DO $identity_scope$
DECLARE name text;
BEGIN
 FOREACH name IN ARRAY ARRAY['agriculture_project_bindings','agriculture_actor_bindings',
   'agriculture_actor_audit','agriculture_identity_replay'] LOOP
  EXECUTE format('DROP POLICY IF EXISTS agriculture_domain_scope ON public.%I',name);
  EXECUTE format('CREATE POLICY agriculture_domain_scope ON public.%I AS RESTRICTIVE TO flow_geo_app USING(tenant_id=NULLIF(current_setting(''app.tenant_ids'',true),'''') AND workspace_id=NULLIF(current_setting(''app.workspace_id'',true),'''')) WITH CHECK(tenant_id=NULLIF(current_setting(''app.tenant_ids'',true),'''') AND workspace_id=NULLIF(current_setting(''app.workspace_id'',true),''''))',name);
 END LOOP;
END $identity_scope$;

CREATE TABLE IF NOT EXISTS public.agriculture_legacy_plot_ownership (
 plot_code varchar(50) PRIMARY KEY REFERENCES public.farmland_plots(plot_code),
 tenant_id varchar(128) NOT NULL,
 workspace_id varchar(128) NOT NULL,
 project_id integer NOT NULL,
 state varchar(16) NOT NULL DEFAULT 'active' CHECK (state IN ('active','quarantined')),
 FOREIGN KEY (tenant_id,workspace_id,project_id)
 REFERENCES public.agriculture_project_bindings(tenant_id,workspace_id,project_id)
);
ALTER TABLE public.agriculture_legacy_plot_ownership ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agriculture_legacy_plot_ownership FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.agriculture_legacy_plot_ownership FROM PUBLIC,flow_geo_app;
GRANT SELECT ON public.agriculture_legacy_plot_ownership TO flow_geo_app;

CREATE OR REPLACE FUNCTION public.agriculture_domain_project_scope(project integer)
 RETURNS boolean LANGUAGE sql STABLE SECURITY INVOKER
 SET search_path=pg_catalog AS $scope$
 SELECT EXISTS (
  SELECT 1 FROM public.agriculture_project_bindings b
  WHERE b.project_id=project AND b.state='active'
   AND b.tenant_id=NULLIF(current_setting('app.tenant_ids',true),'')
   AND b.workspace_id=NULLIF(current_setting('app.workspace_id',true),'')
 );
$scope$;
REVOKE ALL ON FUNCTION public.agriculture_domain_project_scope(integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.agriculture_domain_project_scope(integer)
 TO flow_geo_app,flow_geo_actor_guard;
DROP POLICY IF EXISTS agriculture_domain_owner ON public.agriculture_legacy_plot_ownership;
CREATE POLICY agriculture_domain_owner ON public.agriculture_legacy_plot_ownership
 TO flow_geo_migrate USING(true) WITH CHECK(true);
DROP POLICY IF EXISTS agriculture_domain_scope ON public.agriculture_legacy_plot_ownership;
CREATE POLICY agriculture_domain_scope ON public.agriculture_legacy_plot_ownership AS RESTRICTIVE
 FOR SELECT TO flow_geo_app USING (
 tenant_id=NULLIF(current_setting('app.tenant_ids',true),'')
 AND workspace_id=NULLIF(current_setting('app.workspace_id',true),'')
 AND state='active' AND public.agriculture_domain_project_scope(project_id));
DROP POLICY IF EXISTS agriculture_domain_allow ON public.agriculture_legacy_plot_ownership;
CREATE POLICY agriculture_domain_allow ON public.agriculture_legacy_plot_ownership
 FOR SELECT TO flow_geo_app USING(true);

CREATE OR REPLACE FUNCTION public.agriculture_domain_plot_scope(code text,project integer)
 RETURNS boolean LANGUAGE sql STABLE SECURITY INVOKER
 SET search_path=pg_catalog AS $scope$
 SELECT EXISTS (
  SELECT 1 FROM public.agriculture_legacy_plot_ownership o
  WHERE o.plot_code=code AND o.state='active'
   AND (project IS NULL OR o.project_id=project)
   AND o.tenant_id=NULLIF(current_setting('app.tenant_ids',true),'')
   AND o.workspace_id=NULLIF(current_setting('app.workspace_id',true),'')
   AND public.agriculture_domain_project_scope(o.project_id)
 );
$scope$;
REVOKE ALL ON FUNCTION public.agriculture_domain_plot_scope(text,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.agriculture_domain_plot_scope(text,integer) TO flow_geo_app;

DO $tables$
DECLARE
 expected text[] := ARRAY['acceptance_reports','administrative_boundaries','area_statistics_import_batches','area_statistics_snapshots','change_candidates','change_detection_events','change_detection_runs','dataset_asset_import_batch_items','dataset_asset_import_batches','dataset_asset_verifications','dataset_assets','dataset_lineages','delivery_packages','device_faults','device_telemetry','disaster_patches','disaster_reports','expert_consultations','farmland_plots','field_verification_artifact_events','field_verification_artifacts','field_verifications','growth_monitoring_events','growth_monitoring_runs','growth_monitoring_zones','imagery_assets','imagery_fusion_events','imagery_fusion_jobs','imagery_import_batch_items','imagery_import_batches','imagery_mosaic_events','imagery_mosaic_inputs','imagery_mosaic_jobs','imagery_processing_steps','imagery_registration_events','imagery_registration_jobs','monitoring_devices','monitoring_events','monitoring_projects','monitoring_stations','monitoring_tasks','offline_archive_events','offline_archive_sources','offline_archive_volumes','offline_archives','pest_alerts','pest_assessments','pest_model_versions','pest_report_items','pest_reports','plot_attribute_import_batches','plot_edit_operation_events','plot_edit_operations','plot_quality_checks','plot_versions','production_audit_events','production_batches','project_plot_attribute_field_audits','project_plot_attribute_fields','project_rule_config_audits','project_rule_configs','project_users','quality_issues','review_records','service_access_requests','service_credentials','service_health_checks','service_usage_events','shared_services','statistics_reports','supervision_county_evaluations','supervision_events','supervision_findings','supervision_inspections','supervision_plans','supervision_reinspections','supervision_reports','supervision_samples','task_plots','task_quality_runs','thematic_map_atlas_items','thematic_map_atlases','thematic_map_events','thematic_map_products','thematic_map_templates','uav_aircraft','uav_artifacts','uav_events','uav_findings','uav_missions','vector_export_packages','work_package_plots','work_packages'];
 name text;
 row record;
BEGIN
 FOREACH name IN ARRAY expected LOOP
  IF to_regclass(format('public.%I',name)) IS NULL THEN
   RAISE EXCEPTION 'missing inventoried domain table: %',name;
  END IF;
 END LOOP;
 -- Future migration-owned application tables are denied too. Extension-owned
 -- PostGIS reference tables are not application tenant data.
 FOR row IN SELECT c.relname,c.relowner FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='public' AND c.relkind IN ('r','p')
    AND c.relname NOT IN ('agriculture_project_bindings','agriculture_actor_bindings',
      'agriculture_actor_audit','agriculture_identity_replay',
      'agriculture_legacy_plot_ownership')
    AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_depend d
      JOIN pg_catalog.pg_extension e ON e.oid=d.refobjid
      WHERE d.classid='pg_catalog.pg_class'::regclass AND d.objid=c.oid
       AND d.refclassid='pg_catalog.pg_extension'::regclass AND d.deptype='e')
 LOOP
  IF row.relowner <> (SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user) THEN
   RAISE EXCEPTION 'unexpected application table owner: %',row.relname;
  END IF;
  EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY',row.relname);
  EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY',row.relname);
  EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC,flow_geo_app',row.relname);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_domain_owner ON public.%I',row.relname);
  EXECUTE format('CREATE POLICY agriculture_domain_owner ON public.%I TO flow_geo_migrate USING(true) WITH CHECK(true)',row.relname);
  -- Restrictive deny remains effective even if an older permissive policy exists.
  EXECUTE format('DROP POLICY IF EXISTS agriculture_domain_closed ON public.%I',row.relname);
  EXECUTE format('CREATE POLICY agriculture_domain_closed ON public.%I AS RESTRICTIVE TO flow_geo_app USING(false) WITH CHECK(false)',row.relname);
 END LOOP;
 FOREACH name IN ARRAY ARRAY['monitoring_projects','project_users','project_plot_attribute_fields',
   'task_plots','monitoring_tasks','farmland_plots','plot_edit_operations',
   'plot_versions','review_records'] LOOP
  EXECUTE format('DROP POLICY agriculture_domain_closed ON public.%I',name);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_domain_scope ON public.%I',name);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_domain_allow ON public.%I',name);
  EXECUTE format('CREATE POLICY agriculture_domain_allow ON public.%I TO flow_geo_app USING(true) WITH CHECK(true)',name);
 END LOOP;
END $tables$;
GRANT SELECT ON public.monitoring_projects,public.project_users,
 public.project_plot_attribute_fields,public.task_plots TO flow_geo_app;
GRANT SELECT,UPDATE ON public.monitoring_tasks,public.farmland_plots,
 public.plot_edit_operations TO flow_geo_app;
GRANT SELECT,INSERT ON public.plot_versions,public.review_records TO flow_geo_app;

CREATE POLICY agriculture_domain_scope ON public.monitoring_projects AS RESTRICTIVE TO flow_geo_app,flow_geo_actor_guard
 USING(public.agriculture_domain_project_scope(id))
 WITH CHECK(public.agriculture_domain_project_scope(id));
CREATE POLICY agriculture_domain_scope ON public.project_users AS RESTRICTIVE TO flow_geo_app,flow_geo_actor_guard
 USING(public.agriculture_domain_project_scope(project_id))
 WITH CHECK(public.agriculture_domain_project_scope(project_id));
CREATE POLICY agriculture_domain_scope ON public.project_plot_attribute_fields AS RESTRICTIVE TO flow_geo_app
 USING(public.agriculture_domain_project_scope(project_id))
 WITH CHECK(public.agriculture_domain_project_scope(project_id));
CREATE POLICY agriculture_domain_scope ON public.monitoring_tasks AS RESTRICTIVE TO flow_geo_app
 USING(public.agriculture_domain_project_scope(project_id))
 WITH CHECK(public.agriculture_domain_project_scope(project_id));
CREATE POLICY agriculture_domain_scope ON public.farmland_plots AS RESTRICTIVE TO flow_geo_app
 USING(public.agriculture_domain_plot_scope(plot_code,NULL))
 WITH CHECK(public.agriculture_domain_plot_scope(plot_code,NULL));
CREATE POLICY agriculture_domain_scope ON public.plot_versions AS RESTRICTIVE TO flow_geo_app
 USING(public.agriculture_domain_plot_scope(plot_code,NULL))
 WITH CHECK(public.agriculture_domain_plot_scope(plot_code,NULL));
CREATE POLICY agriculture_domain_scope ON public.task_plots AS RESTRICTIVE TO flow_geo_app
 USING(EXISTS(SELECT 1 FROM public.monitoring_tasks t WHERE t.id=task_id
  AND public.agriculture_domain_plot_scope(plot_code,t.project_id)));
CREATE POLICY agriculture_domain_scope ON public.review_records AS RESTRICTIVE TO flow_geo_app
 USING(quality_run_code IS NULL AND EXISTS(
  SELECT 1 FROM public.monitoring_tasks t WHERE t.id=task_id))
 WITH CHECK(quality_run_code IS NULL AND EXISTS(
  SELECT 1 FROM public.monitoring_tasks t WHERE t.id=task_id));
CREATE POLICY agriculture_domain_scope ON public.plot_edit_operations AS RESTRICTIVE TO flow_geo_app
 USING(EXISTS(SELECT 1 FROM public.monitoring_tasks t WHERE t.id=task_id AND
  CASE WHEN jsonb_typeof(source_plot_codes::jsonb)='array'
    AND jsonb_typeof(result_plot_codes::jsonb)='array'
   THEN jsonb_array_length(source_plot_codes::jsonb)>0
    AND jsonb_array_length(result_plot_codes::jsonb)>0
    AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(
      source_plot_codes::jsonb || result_plot_codes::jsonb) codes(plot_code)
      WHERE NOT public.agriculture_domain_plot_scope(codes.plot_code,t.project_id))
   ELSE false END))
 WITH CHECK(EXISTS(SELECT 1 FROM public.monitoring_tasks t WHERE t.id=task_id AND
  CASE WHEN jsonb_typeof(source_plot_codes::jsonb)='array'
    AND jsonb_typeof(result_plot_codes::jsonb)='array'
   THEN jsonb_array_length(source_plot_codes::jsonb)>0
    AND jsonb_array_length(result_plot_codes::jsonb)>0
    AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(
      source_plot_codes::jsonb || result_plot_codes::jsonb) codes(plot_code)
      WHERE NOT public.agriculture_domain_plot_scope(codes.plot_code,t.project_id))
   ELSE false END));
-- The existing narrow actor guard needs row locks, not broad web ownership.
DROP POLICY IF EXISTS agriculture_domain_guard ON public.monitoring_projects;
CREATE POLICY agriculture_domain_guard ON public.monitoring_projects TO flow_geo_actor_guard
 USING(public.agriculture_domain_project_scope(id))
 WITH CHECK(public.agriculture_domain_project_scope(id));
DROP POLICY IF EXISTS agriculture_domain_guard ON public.project_users;
CREATE POLICY agriculture_domain_guard ON public.project_users TO flow_geo_actor_guard
 USING(public.agriculture_domain_project_scope(project_id))
 WITH CHECK(public.agriculture_domain_project_scope(project_id));
DO $sequences$
DECLARE name text; sequence_name text; row record;
BEGIN
 FOR row IN SELECT c.relname FROM pg_catalog.pg_class c
  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='public' AND c.relkind='S'
   AND c.relowner=(SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user)
 LOOP
  EXECUTE format('REVOKE ALL ON SEQUENCE public.%I FROM PUBLIC,flow_geo_app',row.relname);
 END LOOP;
 FOREACH name IN ARRAY ARRAY['plot_versions','review_records'] LOOP
  sequence_name := pg_get_serial_sequence(format('public.%I',name),'id');
  IF sequence_name IS NULL THEN RAISE EXCEPTION 'missing allowed insert sequence'; END IF;
  EXECUTE format('GRANT USAGE,SELECT ON SEQUENCE %s TO flow_geo_app',sequence_name);
 END LOOP;
END $sequences$;
COMMIT;
