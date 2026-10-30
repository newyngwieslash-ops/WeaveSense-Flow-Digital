-- 仅显式引用适配，不创建农场实体、边界或自动认领旧图斑。
BEGIN;
DO $preflight$ BEGIN
 IF current_user<>'flow_geo_migrate' OR current_database()<>'flow_geo' THEN
  RAISE EXCEPTION 'geo migration owner required';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname='flow_geo_query_guard'
   AND NOT(rolcanlogin OR rolsuper OR rolbypassrls OR rolinherit OR rolcreatedb OR rolcreaterole OR rolreplication)) THEN
  RAISE EXCEPTION 'restricted query guard role required';
 END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint WHERE conrelid='public.agriculture_legacy_plot_ownership'::regclass AND conname='agriculture_plot_owner_reference') THEN
  ALTER TABLE public.agriculture_legacy_plot_ownership ADD CONSTRAINT agriculture_plot_owner_reference UNIQUE(plot_code,tenant_id,workspace_id,project_id);
 END IF;
END $preflight$;
CREATE TABLE IF NOT EXISTS public.agriculture_legacy_resource_references (
 plot_code varchar(50) PRIMARY KEY,
 tenant_id varchar(128) NOT NULL, workspace_id varchar(128) NOT NULL,
 project_id integer NOT NULL,
 parcel_id varchar(128) NOT NULL CHECK(parcel_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 farm_id varchar(128) NOT NULL CHECK(farm_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 state varchar(16) NOT NULL CHECK(state IN ('active','revoked')),
 UNIQUE(tenant_id,workspace_id,parcel_id),
 FOREIGN KEY(plot_code,tenant_id,workspace_id,project_id)
 REFERENCES public.agriculture_legacy_plot_ownership(plot_code,tenant_id,workspace_id,project_id)
);
ALTER TABLE public.agriculture_legacy_resource_references ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agriculture_legacy_resource_references FORCE ROW LEVEL SECURITY;
-- A preceding older boundary replay quarantines newer tables by design. This
-- explicitly reviewed reference table is reopened only under its exact policy.
DROP POLICY IF EXISTS agriculture_domain_closed ON public.agriculture_legacy_resource_references;
REVOKE ALL ON public.agriculture_legacy_resource_references FROM PUBLIC,flow_geo_app,flow_geo_worker;
GRANT SELECT ON public.agriculture_legacy_resource_references TO flow_geo_app;
DROP POLICY IF EXISTS agriculture_reference_owner ON public.agriculture_legacy_resource_references;
CREATE POLICY agriculture_reference_owner ON public.agriculture_legacy_resource_references TO flow_geo_migrate USING(true) WITH CHECK(true);
DROP POLICY IF EXISTS agriculture_reference_scope ON public.agriculture_legacy_resource_references;
CREATE POLICY agriculture_reference_scope ON public.agriculture_legacy_resource_references AS RESTRICTIVE
 TO flow_geo_app,flow_geo_query_guard USING(
 tenant_id=NULLIF(current_setting('app.tenant_ids',true),'') AND workspace_id=NULLIF(current_setting('app.workspace_id',true),'') AND state='active'
 AND public.agriculture_domain_project_scope(project_id)
 AND EXISTS(SELECT 1 FROM public.agriculture_legacy_plot_ownership o
 WHERE o.plot_code=agriculture_legacy_resource_references.plot_code AND o.state='active'
 AND o.project_id=agriculture_legacy_resource_references.project_id));
DROP POLICY IF EXISTS agriculture_reference_allow ON public.agriculture_legacy_resource_references;
CREATE POLICY agriculture_reference_allow ON public.agriculture_legacy_resource_references FOR SELECT TO flow_geo_app,flow_geo_query_guard USING(true);

GRANT USAGE ON SCHEMA public TO flow_geo_query_guard;
GRANT SELECT(tenant_id,workspace_id,project_id,state) ON public.agriculture_project_bindings TO flow_geo_query_guard;
GRANT SELECT(plot_code,tenant_id,workspace_id,project_id,state) ON public.agriculture_legacy_plot_ownership TO flow_geo_query_guard;
GRANT SELECT(plot_code,tenant_id,workspace_id,project_id,parcel_id,farm_id,state) ON public.agriculture_legacy_resource_references TO flow_geo_query_guard;
GRANT SELECT(id,project_id,task_code) ON public.monitoring_tasks TO flow_geo_query_guard;
GRANT SELECT(task_id,plot_code) ON public.task_plots TO flow_geo_query_guard;
DROP POLICY IF EXISTS agriculture_query_guard ON public.agriculture_project_bindings;
CREATE POLICY agriculture_query_guard ON public.agriculture_project_bindings FOR SELECT TO flow_geo_query_guard USING(
 tenant_id=NULLIF(current_setting('app.tenant_ids',true),'') AND workspace_id=NULLIF(current_setting('app.workspace_id',true),'') AND state='active');
DROP POLICY IF EXISTS agriculture_query_guard ON public.agriculture_legacy_plot_ownership;
CREATE POLICY agriculture_query_guard ON public.agriculture_legacy_plot_ownership FOR SELECT TO flow_geo_query_guard USING(
 tenant_id=NULLIF(current_setting('app.tenant_ids',true),'') AND workspace_id=NULLIF(current_setting('app.workspace_id',true),'') AND state='active');
GRANT EXECUTE ON FUNCTION public.agriculture_domain_project_scope(integer) TO flow_geo_query_guard;
DROP POLICY IF EXISTS agriculture_query_guard ON public.monitoring_tasks;
CREATE POLICY agriculture_query_guard ON public.monitoring_tasks FOR SELECT TO flow_geo_query_guard USING(public.agriculture_domain_project_scope(project_id));
DROP POLICY IF EXISTS agriculture_query_guard ON public.task_plots;
CREATE POLICY agriculture_query_guard ON public.task_plots FOR SELECT TO flow_geo_query_guard USING(
 EXISTS(SELECT 1 FROM public.monitoring_tasks t WHERE t.id=task_id AND public.agriculture_domain_project_scope(t.project_id)));

CREATE OR REPLACE FUNCTION public.agriculture_query_plot_allowed(code text)
 RETURNS boolean LANGUAGE sql STABLE SECURITY INVOKER SET search_path=pg_catalog AS $body$
 SELECT EXISTS(SELECT 1 FROM public.agriculture_legacy_resource_references r
 JOIN public.agriculture_legacy_plot_ownership o ON o.plot_code=r.plot_code
  AND o.tenant_id=r.tenant_id AND o.workspace_id=r.workspace_id AND o.project_id=r.project_id
 WHERE r.plot_code=code AND r.state='active' AND o.state='active'
  AND NULLIF(current_setting('app.query_actor',true),'') IS NOT NULL
  AND clock_timestamp()<NULLIF(current_setting('app.query_expiry',true),'')::timestamptz
  AND r.tenant_id=NULLIF(current_setting('app.tenant_ids',true),'')
  AND r.workspace_id=NULLIF(current_setting('app.workspace_id',true),'')
  AND jsonb_exists(COALESCE(NULLIF(current_setting('app.query_parent_farms',true),''),'[]')::jsonb,r.farm_id)
  AND (jsonb_exists(COALESCE(NULLIF(current_setting('app.query_parcels',true),''),'[]')::jsonb,r.parcel_id)
    OR jsonb_exists(COALESCE(NULLIF(current_setting('app.query_farms',true),''),'[]')::jsonb,r.farm_id)));
$body$;
REVOKE ALL ON FUNCTION public.agriculture_query_plot_allowed(text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.agriculture_query_plot_allowed(text) TO flow_geo_app,flow_geo_query_guard;

DO $install$ BEGIN
 IF to_regprocedure('public.agriculture_query_task_scope(integer,boolean)') IS NULL THEN
 EXECUTE $ddl$CREATE FUNCTION public.agriculture_query_task_scope(task integer,require_all boolean)
 RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog AS $scope$
 SELECT EXISTS(SELECT 1 FROM public.monitoring_tasks t
 WHERE t.id=task AND public.agriculture_domain_project_scope(t.project_id)
 AND EXISTS(SELECT 1 FROM public.task_plots p WHERE p.task_id=t.id)
 AND CASE WHEN require_all THEN NOT EXISTS(
  SELECT 1 FROM public.task_plots p WHERE p.task_id=t.id AND NOT (
   public.agriculture_query_plot_allowed(p.plot_code) AND EXISTS(
    SELECT 1 FROM public.agriculture_legacy_plot_ownership o WHERE o.plot_code=p.plot_code AND o.project_id=t.project_id)))
 ELSE EXISTS(SELECT 1 FROM public.task_plots p WHERE p.task_id=t.id
  AND public.agriculture_query_plot_allowed(p.plot_code) AND EXISTS(
   SELECT 1 FROM public.agriculture_legacy_plot_ownership o WHERE o.plot_code=p.plot_code AND o.project_id=t.project_id)) END);
 $scope$ $ddl$;
 REVOKE ALL ON FUNCTION public.agriculture_query_task_scope(integer,boolean) FROM PUBLIC;
 GRANT EXECUTE ON FUNCTION public.agriculture_query_task_scope(integer,boolean) TO flow_geo_app;
 GRANT CREATE ON SCHEMA public TO flow_geo_query_guard;
 ALTER FUNCTION public.agriculture_query_task_scope(integer,boolean) OWNER TO flow_geo_query_guard;
 REVOKE CREATE ON SCHEMA public FROM flow_geo_query_guard;
 END IF;
 IF to_regclass('public.agriculture_query_task_locators') IS NULL THEN
  CREATE VIEW public.agriculture_query_task_locators WITH (security_barrier=true) AS
   SELECT id,task_code,project_id FROM public.monitoring_tasks
   WHERE public.agriculture_query_task_scope(id,false);
  REVOKE ALL ON public.agriculture_query_task_locators FROM PUBLIC,flow_geo_app,flow_geo_worker;
  GRANT SELECT ON public.agriculture_query_task_locators TO flow_geo_app;
  GRANT CREATE ON SCHEMA public TO flow_geo_query_guard;
  ALTER VIEW public.agriculture_query_task_locators OWNER TO flow_geo_query_guard;
  REVOKE CREATE ON SCHEMA public FROM flow_geo_query_guard;
 END IF;
END $install$;

-- Complete task aggregates are not a partial-farm read surface.
DROP POLICY IF EXISTS agriculture_query_resources ON public.monitoring_tasks;
CREATE POLICY agriculture_query_resources ON public.monitoring_tasks AS RESTRICTIVE TO flow_geo_app
 USING(public.agriculture_query_task_scope(id,true)) WITH CHECK(public.agriculture_query_task_scope(id,true));
-- Related rows use only the scoped safe locator, never recurse into full-task RLS.
DROP POLICY IF EXISTS agriculture_domain_scope ON public.task_plots;
CREATE POLICY agriculture_domain_scope ON public.task_plots AS RESTRICTIVE TO flow_geo_app USING(
 EXISTS(SELECT 1 FROM public.agriculture_query_task_locators t WHERE t.id=task_id AND public.agriculture_domain_plot_scope(plot_code,t.project_id)));
DROP POLICY IF EXISTS agriculture_domain_scope ON public.review_records;
CREATE POLICY agriculture_domain_scope ON public.review_records AS RESTRICTIVE TO flow_geo_app USING(
 quality_run_code IS NULL AND public.agriculture_query_task_scope(task_id,true))
 WITH CHECK(quality_run_code IS NULL AND public.agriculture_query_task_scope(task_id,true));
DROP POLICY IF EXISTS agriculture_query_resources ON public.review_records;
CREATE POLICY agriculture_query_resources ON public.review_records AS RESTRICTIVE TO flow_geo_app
 USING(public.agriculture_query_task_scope(task_id,true)) WITH CHECK(public.agriculture_query_task_scope(task_id,true));
DO $resource_policies$
DECLARE name text;
BEGIN
 FOREACH name IN ARRAY ARRAY['farmland_plots','plot_versions','task_plots'] LOOP
  EXECUTE format('DROP POLICY IF EXISTS agriculture_query_resources ON public.%I',name);
  EXECUTE format('CREATE POLICY agriculture_query_resources ON public.%I AS RESTRICTIVE TO flow_geo_app USING(public.agriculture_query_plot_allowed(plot_code)) WITH CHECK(public.agriculture_query_plot_allowed(plot_code))',name);
 END LOOP;
END $resource_policies$;
DROP POLICY IF EXISTS agriculture_query_resources ON public.plot_edit_operations;
CREATE POLICY agriculture_query_resources ON public.plot_edit_operations AS RESTRICTIVE TO flow_geo_app USING(
 public.agriculture_query_task_scope(task_id,true)
 AND NOT EXISTS(SELECT 1 FROM json_array_elements_text(source_plot_codes) c WHERE NOT public.agriculture_query_plot_allowed(c))
 AND NOT EXISTS(SELECT 1 FROM json_array_elements_text(result_plot_codes) c WHERE NOT public.agriculture_query_plot_allowed(c)));

-- Progress is project-wide; only safe identity/join columns are exposed here.
REVOKE SELECT ON public.monitoring_projects FROM flow_geo_app;
GRANT SELECT(id,project_code,monitor_year,status) ON public.monitoring_projects TO flow_geo_app;
DROP POLICY IF EXISTS agriculture_query_project ON public.monitoring_projects;
CREATE POLICY agriculture_query_project ON public.monitoring_projects AS RESTRICTIVE TO flow_geo_app USING(
 EXISTS(SELECT 1 FROM public.agriculture_legacy_resource_references r
 WHERE r.project_id=monitoring_projects.id AND public.agriculture_query_plot_allowed(r.plot_code)));
DROP POLICY IF EXISTS agriculture_query_actor ON public.project_users;
CREATE POLICY agriculture_query_actor ON public.project_users AS RESTRICTIVE TO flow_geo_app USING(
 EXISTS(SELECT 1 FROM public.agriculture_actor_bindings a WHERE a.legacy_member_id=project_users.id AND a.project_id=project_users.project_id
 AND a.actor_id=NULLIF(current_setting('app.query_actor',true),'') AND a.state='active'));
COMMIT;
