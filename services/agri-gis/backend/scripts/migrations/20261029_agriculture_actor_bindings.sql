-- GIS owner provisioning only; existing project/user rows are never rewritten.
BEGIN;
DO $guard$ BEGIN
 IF current_database() <> 'flow_geo' OR current_user <> 'flow_geo_migrate' THEN
  RAISE EXCEPTION 'actor migration requires geo migration ownership';
 END IF;
END; $guard$;
CREATE UNIQUE INDEX IF NOT EXISTS agriculture_project_users_project_id_id ON public.project_users(project_id,id);
CREATE TABLE IF NOT EXISTS public.agriculture_project_bindings (
 tenant_id varchar(128) NOT NULL CHECK (tenant_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 workspace_id varchar(128) NOT NULL CHECK (workspace_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 project_id integer NOT NULL UNIQUE REFERENCES public.monitoring_projects(id) ON DELETE RESTRICT,
 state varchar(16) NOT NULL CHECK (state IN ('active','revoked')),
 PRIMARY KEY(tenant_id,workspace_id), UNIQUE(tenant_id,workspace_id,project_id)
);
CREATE TABLE IF NOT EXISTS public.agriculture_actor_bindings (
 tenant_id varchar(128) NOT NULL,
 workspace_id varchar(128) NOT NULL,
 actor_id varchar(128) NOT NULL CHECK (actor_id ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$'),
 project_id integer NOT NULL,
 legacy_member_id integer NOT NULL,
 state varchar(16) NOT NULL CHECK (state IN ('active','revoked')),
 PRIMARY KEY(tenant_id,workspace_id,actor_id),
 UNIQUE(project_id,legacy_member_id),
 FOREIGN KEY(tenant_id,workspace_id,project_id) REFERENCES public.agriculture_project_bindings(tenant_id,workspace_id,project_id) ON DELETE RESTRICT,
 FOREIGN KEY(project_id,legacy_member_id) REFERENCES public.project_users(project_id,id) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS public.agriculture_actor_audit (
 id varchar(36) PRIMARY KEY,
 tenant_id varchar(128) NOT NULL CHECK (tenant_id <> ''),
 workspace_id varchar(128) NOT NULL CHECK (workspace_id <> ''),
 actor_id varchar(128) NOT NULL CHECK (actor_id <> ''),
 project_id integer NOT NULL,
 legacy_member_id integer NOT NULL,
 legacy_user_code varchar(50) NOT NULL,
 legacy_role varchar(40) NOT NULL,
 policy_version varchar(64) NOT NULL,
 authorization_version bigint NOT NULL CHECK(authorization_version BETWEEN 1 AND 9007199254740991),
 request_id varchar(128) NOT NULL,
 operation_id varchar(128),
 action varchar(80) NOT NULL,
 resources json NOT NULL CHECK(json_typeof(resources)='array'),
 changes json NOT NULL CHECK(json_typeof(changes)='array'),
 created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE OR REPLACE FUNCTION public.agriculture_actor_audit_immutable() RETURNS trigger
 LANGUAGE plpgsql SET search_path=pg_catalog AS $body$
 BEGIN RAISE EXCEPTION 'actor audit is append only' USING ERRCODE='42501'; END;
 $body$;
REVOKE ALL ON FUNCTION public.agriculture_actor_audit_immutable() FROM PUBLIC;
DROP TRIGGER IF EXISTS agriculture_actor_audit_immutable ON public.agriculture_actor_audit;
CREATE TRIGGER agriculture_actor_audit_immutable BEFORE UPDATE OR DELETE OR TRUNCATE ON public.agriculture_actor_audit
 FOR EACH STATEMENT EXECUTE FUNCTION public.agriculture_actor_audit_immutable();

ALTER TABLE public.agriculture_project_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agriculture_project_bindings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS agriculture_project_bindings_scope ON public.agriculture_project_bindings;
CREATE POLICY agriculture_project_bindings_scope ON public.agriculture_project_bindings
 USING(tenant_id <> '' AND tenant_id = ANY(string_to_array(current_setting('app.tenant_ids',true),',')))
 WITH CHECK(tenant_id <> '' AND tenant_id = ANY(string_to_array(current_setting('app.tenant_ids',true),',')));
REVOKE ALL ON public.agriculture_project_bindings FROM PUBLIC, flow_geo_app;
GRANT SELECT ON public.agriculture_project_bindings TO flow_geo_app;

ALTER TABLE public.agriculture_actor_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agriculture_actor_bindings FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS agriculture_actor_bindings_scope ON public.agriculture_actor_bindings;
CREATE POLICY agriculture_actor_bindings_scope ON public.agriculture_actor_bindings
 USING(tenant_id <> '' AND tenant_id = ANY(string_to_array(current_setting('app.tenant_ids',true),',')))
 WITH CHECK(tenant_id <> '' AND tenant_id = ANY(string_to_array(current_setting('app.tenant_ids',true),',')));
REVOKE ALL ON public.agriculture_actor_bindings FROM PUBLIC, flow_geo_app;
GRANT SELECT ON public.agriculture_actor_bindings TO flow_geo_app;

ALTER TABLE public.agriculture_actor_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agriculture_actor_audit FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS agriculture_actor_audit_scope ON public.agriculture_actor_audit;
CREATE POLICY agriculture_actor_audit_scope ON public.agriculture_actor_audit
 USING(tenant_id <> '' AND tenant_id = ANY(string_to_array(current_setting('app.tenant_ids',true),',')))
 WITH CHECK(tenant_id <> '' AND tenant_id = ANY(string_to_array(current_setting('app.tenant_ids',true),',')));
REVOKE ALL ON public.agriculture_actor_audit FROM PUBLIC, flow_geo_app;
GRANT SELECT, INSERT ON public.agriculture_actor_audit TO flow_geo_app;
GRANT USAGE ON SCHEMA public TO flow_geo_actor_guard;
GRANT SELECT ON public.agriculture_project_bindings, public.agriculture_actor_bindings,
 public.monitoring_projects, public.project_users TO flow_geo_actor_guard;
GRANT UPDATE(project_id) ON public.agriculture_project_bindings, public.agriculture_actor_bindings TO flow_geo_actor_guard;
GRANT UPDATE(id) ON public.monitoring_projects, public.project_users TO flow_geo_actor_guard;
DO $install$ BEGIN
IF to_regprocedure('public.agriculture_lock_actor(text,text,text,integer)') IS NULL THEN
EXECUTE $ddl$CREATE FUNCTION public.agriculture_lock_actor(tenant text, workspace text, actor text, project integer)
 RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog AS $lock$
DECLARE found_row integer;
BEGIN
 IF tenant IS NULL OR tenant = '' OR current_setting('app.tenant_ids',true) IS DISTINCT FROM tenant THEN
  RETURN false;
 END IF;
 SELECT u.id INTO found_row FROM public.agriculture_project_bindings p
 JOIN public.agriculture_actor_bindings a ON a.tenant_id=p.tenant_id AND a.workspace_id=p.workspace_id AND a.project_id=p.project_id
 JOIN public.project_users u ON u.id=a.legacy_member_id AND u.project_id=a.project_id
 JOIN public.monitoring_projects m ON m.id=u.project_id
 WHERE p.tenant_id=tenant AND p.workspace_id=workspace AND a.actor_id=actor AND p.project_id=project
  AND p.state='active' AND a.state='active' AND u.status='active' AND m.status='active'
 FOR UPDATE OF p,a,u,m;
 RETURN found_row IS NOT NULL;
END; $lock$ $ddl$;
REVOKE ALL ON FUNCTION public.agriculture_lock_actor(text,text,text,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.agriculture_lock_actor(text,text,text,integer) TO flow_geo_app;
GRANT CREATE ON SCHEMA public TO flow_geo_actor_guard;
ALTER FUNCTION public.agriculture_lock_actor(text,text,text,integer) OWNER TO flow_geo_actor_guard;
REVOKE CREATE ON SCHEMA public FROM flow_geo_actor_guard;
END IF;
END; $install$;
COMMIT;

