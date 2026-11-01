BEGIN;
-- Authority sidecar only: no queue, scheduling, result payload or lease ownership.
CREATE TABLE IF NOT EXISTS public.agriculture_job_authorities (
 tenant_id varchar(128) NOT NULL,
 workspace_id varchar(128) NOT NULL,
 job_id varchar(128) NOT NULL,
 snapshot_hash varchar(64) NOT NULL CHECK(snapshot_hash ~ '^[0-9a-f]{64}$'),
 snapshot_json text NOT NULL CHECK(octet_length(snapshot_json)<=131072),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,job_id),
 UNIQUE(tenant_id,workspace_id,job_id,snapshot_hash)
);
CREATE TABLE IF NOT EXISTS public.agriculture_job_authority_checks (
 tenant_id varchar(128) NOT NULL,
 workspace_id varchar(128) NOT NULL,
 job_id varchar(128) NOT NULL,
 snapshot_hash varchar(64) NOT NULL,
 check_id varchar(128) NOT NULL,
 worker_id varchar(128) NOT NULL,
 attempt bigint NOT NULL CHECK(attempt BETWEEN 1 AND 9007199254740991),
 fencing bigint NOT NULL CHECK(fencing BETWEEN 1 AND 9007199254740991),
 phase varchar(16) NOT NULL CHECK(phase IN ('claim','read_input','publish')),
 checked_at timestamptz NOT NULL,
 valid_until timestamptz NOT NULL CHECK(valid_until>checked_at AND valid_until<=checked_at+interval '30 seconds'),
 PRIMARY KEY(tenant_id,workspace_id,check_id),
 FOREIGN KEY(tenant_id,workspace_id,job_id,snapshot_hash)
 REFERENCES public.agriculture_job_authorities(tenant_id,workspace_id,job_id,snapshot_hash)
);
REVOKE ALL ON public.agriculture_job_authorities,public.agriculture_job_authority_checks FROM PUBLIC,flow_geo_app,flow_geo_worker;
GRANT SELECT,INSERT ON public.agriculture_job_authorities TO flow_geo_app;
GRANT SELECT ON public.agriculture_job_authorities TO flow_geo_worker;
GRANT SELECT,INSERT ON public.agriculture_job_authority_checks TO flow_geo_app,flow_geo_worker;
ALTER TABLE public.agriculture_job_authorities ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agriculture_job_authorities FORCE ROW LEVEL SECURITY;
ALTER TABLE public.agriculture_job_authority_checks ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agriculture_job_authority_checks FORCE ROW LEVEL SECURITY;
DO $$ DECLARE name text; BEGIN
 FOREACH name IN ARRAY ARRAY['agriculture_job_authorities','agriculture_job_authority_checks'] LOOP
  EXECUTE format('DROP POLICY IF EXISTS agriculture_job_owner ON public.%I',name);
  EXECUTE format('CREATE POLICY agriculture_job_owner ON public.%I TO flow_geo_migrate USING(true) WITH CHECK(true)',name);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_job_runtime ON public.%I',name);
  EXECUTE format('CREATE POLICY agriculture_job_runtime ON public.%I TO flow_geo_app,flow_geo_worker USING(true) WITH CHECK(true)',name);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_job_scope ON public.%I',name);
  EXECUTE format('CREATE POLICY agriculture_job_scope ON public.%I AS RESTRICTIVE TO flow_geo_app,flow_geo_worker USING(tenant_id=nullif(current_setting(''app.tenant_ids'',true),'''') AND workspace_id=nullif(current_setting(''app.workspace_id'',true),'''')) WITH CHECK(tenant_id=nullif(current_setting(''app.tenant_ids'',true),'''') AND workspace_id=nullif(current_setting(''app.workspace_id'',true),''''))',name);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_domain_closed ON public.%I',name);
 END LOOP;
END $$;
CREATE OR REPLACE FUNCTION public.agriculture_job_authority_immutable()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN RAISE EXCEPTION 'immutable job authority' USING ERRCODE='23514'; END;
$$;
REVOKE ALL ON FUNCTION public.agriculture_job_authority_immutable() FROM PUBLIC;
DROP TRIGGER IF EXISTS agriculture_job_authority_immutable ON public.agriculture_job_authorities;
CREATE TRIGGER agriculture_job_authority_immutable BEFORE UPDATE OR DELETE ON public.agriculture_job_authorities FOR EACH ROW EXECUTE FUNCTION public.agriculture_job_authority_immutable();
DROP TRIGGER IF EXISTS agriculture_job_authority_immutable ON public.agriculture_job_authority_checks;
CREATE TRIGGER agriculture_job_authority_immutable BEFORE UPDATE OR DELETE ON public.agriculture_job_authority_checks FOR EACH ROW EXECUTE FUNCTION public.agriculture_job_authority_immutable();
COMMIT;
