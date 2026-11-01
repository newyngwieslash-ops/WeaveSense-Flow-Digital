BEGIN;

DO $guard$ BEGIN
 IF current_database()<>'flow_geo' OR current_user<>'flow_geo_migrate' THEN
  RAISE EXCEPTION 'farm migration requires GIS owner';
 END IF;
END $guard$;

CREATE TABLE IF NOT EXISTS public.agriculture_farms (
 tenant_id text NOT NULL CHECK (tenant_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 workspace_id text NOT NULL CHECK (workspace_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 farm_id text NOT NULL CHECK (farm_id ~ '^farm_[A-Za-z0-9_-]{1,100}$'),
 external_code text NOT NULL CHECK (length(external_code) BETWEEN 1 AND 128),
 name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
 operator_name text NOT NULL CHECK (length(operator_name) BETWEEN 1 AND 200),
 contact jsonb NOT NULL DEFAULT '{}'::jsonb CHECK(jsonb_typeof(contact)='object'),
 region_code text NOT NULL, dictionary_version text NOT NULL,
 timezone text NOT NULL, area_unit text NOT NULL CHECK(area_unit IN ('m2','ha','mu')),
 geom geometry(MultiPolygon,4326) NOT NULL,
 area_m2 numeric(24,4) GENERATED ALWAYS AS
  (round(ST_Area(geom::geography,true)::numeric,4)) STORED,
 geometry_hash text NOT NULL CHECK(geometry_hash ~ '^[0-9a-f]{64}$'),
 provenance jsonb NOT NULL CHECK(jsonb_typeof(provenance)='object'),
 data_class text NOT NULL CHECK(data_class IN ('demo','operational')),
 status text NOT NULL CHECK(status IN ('active','archived')),
 revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
 state_revision bigint NOT NULL CHECK(state_revision BETWEEN 1 AND 9007199254740991),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,farm_id),
 UNIQUE(tenant_id,workspace_id,external_code),
 CHECK(ST_IsValid(geom) AND NOT ST_IsEmpty(geom) AND ST_NDims(geom)=2),
 CHECK(area_m2>0)
);
CREATE INDEX IF NOT EXISTS agriculture_farms_geometry ON public.agriculture_farms USING gist(geom);

CREATE TABLE IF NOT EXISTS public.agriculture_farm_revisions (
 tenant_id text NOT NULL, workspace_id text NOT NULL, farm_id text NOT NULL,
 revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
 actor_id text NOT NULL, operation_id text NOT NULL,
 snapshot_json jsonb NOT NULL CHECK(jsonb_typeof(snapshot_json)='object'),
 snapshot_hash text NOT NULL CHECK(snapshot_hash ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,farm_id,revision),
 FOREIGN KEY(tenant_id,workspace_id,farm_id) REFERENCES public.agriculture_farms
  (tenant_id,workspace_id,farm_id) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS public.agriculture_farm_commands (
 tenant_id text NOT NULL, workspace_id text NOT NULL,
 operation_id text NOT NULL, actor_id text NOT NULL,
 operation_kind text NOT NULL CHECK(operation_kind IN ('farm.create','farm.update','farm.archive')),
 idempotency_key_hash text NOT NULL CHECK(idempotency_key_hash ~ '^[0-9a-f]{64}$'),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 grant_plan_hash text NOT NULL CHECK(grant_plan_hash ~ '^[0-9a-f]{64}$'),
 farm_id text NOT NULL, receipt_json jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,operation_id),
 UNIQUE(tenant_id,operation_id),
 UNIQUE(tenant_id,actor_id,operation_kind,idempotency_key_hash),
 FOREIGN KEY(tenant_id,workspace_id,farm_id) REFERENCES public.agriculture_farms
  (tenant_id,workspace_id,farm_id) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS public.agriculture_farm_outbox (
 tenant_id text NOT NULL, workspace_id text NOT NULL, event_id text NOT NULL,
 operation_id text NOT NULL, event_type text NOT NULL,
 farm_id text NOT NULL, aggregate_revision bigint NOT NULL,
 payload_json jsonb NOT NULL, payload_hash text NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,event_id),
 UNIQUE(tenant_id,workspace_id,operation_id),
 FOREIGN KEY(tenant_id,workspace_id,farm_id) REFERENCES public.agriculture_farms
  (tenant_id,workspace_id,farm_id) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS public.agriculture_farm_dictionaries (
 tenant_id text NOT NULL, workspace_id text NOT NULL, version text NOT NULL,
 document jsonb NOT NULL CHECK(jsonb_typeof(document)='object'),
 document_hash text NOT NULL CHECK(document_hash ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,version)
);

CREATE OR REPLACE FUNCTION public.agriculture_farm_append_only()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN RAISE EXCEPTION 'immutable farm history' USING ERRCODE='23514'; END;
$$;
REVOKE ALL ON FUNCTION public.agriculture_farm_append_only() FROM PUBLIC;
CREATE OR REPLACE FUNCTION public.agriculture_farm_transition()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'farm history cannot be deleted' USING ERRCODE='23514';
 END IF;
 IF (NEW.tenant_id,NEW.workspace_id,NEW.farm_id,NEW.external_code,NEW.data_class,NEW.created_at)
  IS DISTINCT FROM (OLD.tenant_id,OLD.workspace_id,OLD.farm_id,OLD.external_code,OLD.data_class,OLD.created_at)
  OR OLD.status='archived' OR NEW.state_revision<>OLD.state_revision+1 THEN
  RAISE EXCEPTION 'invalid farm identity transition' USING ERRCODE='23514';
 END IF;
 IF NEW.status NOT IN ('active','archived') OR NEW.revision<>OLD.revision+1 THEN
  RAISE EXCEPTION 'invalid farm revision transition' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION public.agriculture_farm_transition() FROM PUBLIC;
DROP TRIGGER IF EXISTS agriculture_farm_transition ON public.agriculture_farms;
CREATE TRIGGER agriculture_farm_transition BEFORE UPDATE OR DELETE ON public.agriculture_farms
 FOR EACH ROW EXECUTE FUNCTION public.agriculture_farm_transition();

DO $policies$ DECLARE n text; BEGIN
 FOREACH n IN ARRAY ARRAY['agriculture_farms','agriculture_farm_revisions',
  'agriculture_farm_commands','agriculture_farm_outbox','agriculture_farm_dictionaries'] LOOP
  EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY',n);
  EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY',n);
  EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC,flow_geo_app,flow_geo_worker',n);
  EXECUTE format('GRANT SELECT,INSERT ON public.%I TO flow_geo_app',n);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_domain_closed ON public.%I',n);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_farm_owner ON public.%I',n);
  EXECUTE format('CREATE POLICY agriculture_farm_owner ON public.%I TO flow_geo_migrate USING(true) WITH CHECK(true)',n);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_farm_runtime ON public.%I',n);
  EXECUTE format('CREATE POLICY agriculture_farm_runtime ON public.%I TO flow_geo_app USING(true) WITH CHECK(true)',n);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_farm_scope ON public.%I',n);
  EXECUTE format('CREATE POLICY agriculture_farm_scope ON public.%I AS RESTRICTIVE TO flow_geo_app USING(tenant_id=nullif(current_setting(''app.tenant_ids'',true),'''') AND workspace_id=nullif(current_setting(''app.workspace_id'',true),'''')) WITH CHECK(tenant_id=nullif(current_setting(''app.tenant_ids'',true),'''') AND workspace_id=nullif(current_setting(''app.workspace_id'',true),''''))',n);
  IF n<>'agriculture_farms' THEN
   EXECUTE format('DROP TRIGGER IF EXISTS agriculture_farm_append_only ON public.%I',n);
   EXECUTE format('CREATE TRIGGER agriculture_farm_append_only BEFORE UPDATE OR DELETE OR TRUNCATE ON public.%I FOR EACH STATEMENT EXECUTE FUNCTION public.agriculture_farm_append_only()',n);
  END IF;
 END LOOP;
END $policies$;
GRANT UPDATE ON public.agriculture_farms TO flow_geo_app;

COMMIT;
