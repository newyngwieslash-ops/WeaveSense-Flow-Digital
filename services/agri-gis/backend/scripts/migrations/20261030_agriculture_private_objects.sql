BEGIN;

-- Immutable byte references, not an asset/farm lifecycle or upload API.
CREATE TABLE IF NOT EXISTS public.agriculture_private_objects (
 tenant_id varchar(128) NOT NULL CHECK (tenant_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 workspace_id varchar(128) NOT NULL CHECK (workspace_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 object_id varchar(128) NOT NULL CHECK (object_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 version bigint NOT NULL CHECK (version BETWEEN 1 AND 9007199254740991),
 resource_kind varchar(16) NOT NULL CHECK (resource_kind IN ('asset','report')),
 resource_id varchar(128) NOT NULL CHECK (resource_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 resource_revision bigint NOT NULL CHECK (resource_revision BETWEEN 1 AND 9007199254740991),
 category varchar(16) NOT NULL CHECK (category IN ('original','thumbnail','tile','report','media')),
 object_key text NOT NULL,
 content_sha256 varchar(64) NOT NULL CHECK (content_sha256 ~ '^[0-9a-f]{64}$'),
 size_bytes bigint NOT NULL CHECK (size_bytes BETWEEN 0 AND 67108864),
 media_type varchar(80) NOT NULL CHECK (media_type IN (
  'application/octet-stream','application/pdf','application/json',
  'application/vnd.mapbox-vector-tile','image/png','image/jpeg','image/tiff',
  'audio/mpeg','video/mp4')),
 data_class varchar(16) NOT NULL CHECK (data_class IN ('demo','operational')),
 distribution_allowed boolean NOT NULL,
 state varchar(16) NOT NULL DEFAULT 'active' CHECK (state IN ('active','revoked')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY (tenant_id,workspace_id,object_id,version),
 UNIQUE (object_key),
 CHECK (object_key = 'tenants/'||tenant_id||'/workspaces/'||workspace_id||
                    '/objects/'||object_id||'/v'||version::text)
);
ALTER TABLE public.agriculture_private_objects ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agriculture_private_objects FORCE ROW LEVEL SECURITY;
REVOKE ALL ON public.agriculture_private_objects FROM PUBLIC,flow_geo_app,flow_geo_worker;
GRANT SELECT ON public.agriculture_private_objects TO flow_geo_app;
DROP POLICY IF EXISTS agriculture_object_owner ON public.agriculture_private_objects;
CREATE POLICY agriculture_object_owner ON public.agriculture_private_objects
 TO flow_geo_migrate USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS agriculture_object_select ON public.agriculture_private_objects;
CREATE POLICY agriculture_object_select ON public.agriculture_private_objects
 FOR SELECT TO flow_geo_app USING (true);
DROP POLICY IF EXISTS agriculture_object_scope ON public.agriculture_private_objects;
CREATE POLICY agriculture_object_scope ON public.agriculture_private_objects
 AS RESTRICTIVE TO flow_geo_app
 USING (tenant_id = nullif(current_setting('app.tenant_ids',true),'')
  AND workspace_id = nullif(current_setting('app.workspace_id',true),'')
  AND state='active' AND distribution_allowed)
 WITH CHECK (false);
-- Earlier migration replay may quarantine an unknown table before this exact
-- reviewed migration restores its narrow read-only scope policy.
DROP POLICY IF EXISTS agriculture_domain_closed ON public.agriculture_private_objects;

CREATE OR REPLACE FUNCTION public.agriculture_object_immutable()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'immutable object version' USING ERRCODE='23514';
 END IF;
 IF (to_jsonb(NEW)-'state') IS DISTINCT FROM (to_jsonb(OLD)-'state')
  OR OLD.state='revoked' OR NEW.state<>'revoked' THEN
  RAISE EXCEPTION 'immutable object version' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION public.agriculture_object_immutable() FROM PUBLIC;
DROP TRIGGER IF EXISTS agriculture_object_immutable ON public.agriculture_private_objects;
CREATE TRIGGER agriculture_object_immutable BEFORE UPDATE OR DELETE
 ON public.agriculture_private_objects FOR EACH ROW
 EXECUTE FUNCTION public.agriculture_object_immutable();

COMMIT;
