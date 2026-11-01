BEGIN;

DO $guard$ BEGIN
 IF current_database()<>'flow_geo' OR current_user<>'flow_geo_migrate' THEN
  RAISE EXCEPTION 'parcel migration requires GIS owner';
 END IF;
END $guard$;

CREATE TABLE IF NOT EXISTS public.agriculture_parcels (
 tenant_id text NOT NULL CHECK(tenant_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 workspace_id text NOT NULL CHECK(workspace_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
 parcel_id text NOT NULL CHECK(parcel_id ~ '^parcel_[0-9a-f]{32}$'),
 farm_id text NOT NULL,
 external_code text NOT NULL CHECK(length(external_code) BETWEEN 1 AND 128),
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200),
 data_class text NOT NULL CHECK(data_class IN ('demo','operational')),
 status text NOT NULL CHECK(status='active'),
 current_revision bigint NOT NULL CHECK(current_revision BETWEEN 1 AND 9007199254740991),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,parcel_id),
 UNIQUE(tenant_id,workspace_id,farm_id,external_code),
 UNIQUE(tenant_id,workspace_id,parcel_id,farm_id),
 FOREIGN KEY(tenant_id,workspace_id,farm_id) REFERENCES public.agriculture_farms
  (tenant_id,workspace_id,farm_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS public.agriculture_parcel_revisions (
 tenant_id text NOT NULL, workspace_id text NOT NULL, parcel_id text NOT NULL,
 revision bigint NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
 geom geometry(MultiPolygon,4326) NOT NULL,
 area_m2 numeric(24,4) GENERATED ALWAYS AS
  (round(ST_Area(geom::geography,true)::numeric,4)) STORED,
 geometry_hash text NOT NULL CHECK(geometry_hash ~ '^[0-9a-f]{64}$'),
 provenance jsonb NOT NULL CHECK(jsonb_typeof(provenance)='object'),
 effective_at timestamptz NOT NULL,
 recorded_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 actor_id text NOT NULL CHECK(length(actor_id) BETWEEN 1 AND 128),
 reason text NOT NULL CHECK(length(reason) BETWEEN 1 AND 500),
 operation_id text NOT NULL,
 PRIMARY KEY(tenant_id,workspace_id,parcel_id,revision),
 UNIQUE(tenant_id,workspace_id,parcel_id,effective_at),
 FOREIGN KEY(tenant_id,workspace_id,parcel_id) REFERENCES public.agriculture_parcels
  (tenant_id,workspace_id,parcel_id) ON DELETE RESTRICT,
 CHECK(ST_IsValid(geom) AND NOT ST_IsEmpty(geom) AND ST_NDims(geom)=2),
 CHECK(area_m2>0),
 CHECK(isfinite(effective_at) AND isfinite(recorded_at))
);
CREATE INDEX IF NOT EXISTS agriculture_parcel_revisions_geometry
 ON public.agriculture_parcel_revisions USING gist(geom);

-- Identity is inserted/advanced before the immutable revision in the same
-- transaction; the referenced revision must exist when that transaction commits.
DO $pointer$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint
  WHERE conrelid='public.agriculture_parcels'::regclass
   AND conname='agriculture_parcel_current_revision_fk') THEN
  ALTER TABLE public.agriculture_parcels
   ADD CONSTRAINT agriculture_parcel_current_revision_fk
   FOREIGN KEY(tenant_id,workspace_id,parcel_id,current_revision)
   REFERENCES public.agriculture_parcel_revisions
    (tenant_id,workspace_id,parcel_id,revision)
   ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED;
 END IF;
END $pointer$;

CREATE TABLE IF NOT EXISTS public.agriculture_parcel_commands (
 tenant_id text NOT NULL, workspace_id text NOT NULL,
 operation_id text NOT NULL, actor_id text NOT NULL,
 operation_kind text NOT NULL CHECK(operation_kind IN ('parcel.create','parcel.revise')),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 parcel_id text NOT NULL, farm_id text NOT NULL,
 receipt_json jsonb NOT NULL CHECK(jsonb_typeof(receipt_json)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,operation_id),
 UNIQUE(tenant_id,operation_id),
 FOREIGN KEY(tenant_id,workspace_id,parcel_id,farm_id) REFERENCES public.agriculture_parcels
  (tenant_id,workspace_id,parcel_id,farm_id) ON DELETE RESTRICT
);

CREATE TABLE IF NOT EXISTS public.agriculture_parcel_outbox (
 tenant_id text NOT NULL, workspace_id text NOT NULL,
 event_id text NOT NULL, operation_id text NOT NULL,
 event_type text NOT NULL CHECK(event_type IN ('agri.parcel.created','agri.parcel.revised')),
 parcel_id text NOT NULL, farm_id text NOT NULL,
 aggregate_revision bigint NOT NULL CHECK(aggregate_revision BETWEEN 1 AND 9007199254740991),
 payload_json jsonb NOT NULL CHECK(jsonb_typeof(payload_json)='object'),
 payload_hash text NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,event_id),
 UNIQUE(tenant_id,workspace_id,operation_id),
 FOREIGN KEY(tenant_id,workspace_id,parcel_id,farm_id) REFERENCES public.agriculture_parcels
  (tenant_id,workspace_id,parcel_id,farm_id) ON DELETE RESTRICT,
 FOREIGN KEY(tenant_id,workspace_id,parcel_id,aggregate_revision)
  REFERENCES public.agriculture_parcel_revisions
  (tenant_id,workspace_id,parcel_id,revision) ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION public.agriculture_parcel_append_only()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN RAISE EXCEPTION 'immutable parcel history' USING ERRCODE='23514'; END;
$$;
REVOKE ALL ON FUNCTION public.agriculture_parcel_append_only() FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.agriculture_parcel_revision_insert()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE current_pointer bigint; prior_revision bigint; prior_effective timestamptz;
BEGIN
 SELECT p.current_revision INTO current_pointer FROM public.agriculture_parcels p
  WHERE p.tenant_id=NEW.tenant_id AND p.workspace_id=NEW.workspace_id
   AND p.parcel_id=NEW.parcel_id FOR UPDATE;
 IF current_pointer IS NULL OR current_pointer<>NEW.revision THEN
  RAISE EXCEPTION 'parcel revision must match current pointer' USING ERRCODE='23514';
 END IF;
 SELECT r.revision,r.effective_at INTO prior_revision,prior_effective
  FROM public.agriculture_parcel_revisions r
  WHERE r.tenant_id=NEW.tenant_id AND r.workspace_id=NEW.workspace_id
   AND r.parcel_id=NEW.parcel_id ORDER BY r.revision DESC LIMIT 1;
 IF (prior_revision IS NULL AND NEW.revision<>1)
  OR (prior_revision IS NOT NULL AND
      (NEW.revision<>prior_revision+1 OR NEW.effective_at<=prior_effective)) THEN
  RAISE EXCEPTION 'invalid parcel revision chronology' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION public.agriculture_parcel_revision_insert() FROM PUBLIC;
DROP TRIGGER IF EXISTS agriculture_parcel_revision_insert ON public.agriculture_parcel_revisions;
CREATE TRIGGER agriculture_parcel_revision_insert BEFORE INSERT ON public.agriculture_parcel_revisions
 FOR EACH ROW EXECUTE FUNCTION public.agriculture_parcel_revision_insert();

CREATE OR REPLACE FUNCTION public.agriculture_parcel_transition()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  RAISE EXCEPTION 'parcel history cannot be deleted' USING ERRCODE='23514';
 END IF;
 IF (NEW.tenant_id,NEW.workspace_id,NEW.parcel_id,NEW.farm_id,
     NEW.external_code,NEW.name,NEW.data_class,NEW.status,NEW.created_at)
  IS DISTINCT FROM
    (OLD.tenant_id,OLD.workspace_id,OLD.parcel_id,OLD.farm_id,
     OLD.external_code,OLD.name,OLD.data_class,OLD.status,OLD.created_at)
  OR NEW.current_revision<>OLD.current_revision+1 THEN
  RAISE EXCEPTION 'invalid parcel identity transition' USING ERRCODE='23514';
 END IF;
 RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION public.agriculture_parcel_transition() FROM PUBLIC;
DROP TRIGGER IF EXISTS agriculture_parcel_transition ON public.agriculture_parcels;
CREATE TRIGGER agriculture_parcel_transition BEFORE UPDATE OR DELETE ON public.agriculture_parcels
 FOR EACH ROW EXECUTE FUNCTION public.agriculture_parcel_transition();
DROP TRIGGER IF EXISTS agriculture_parcel_no_truncate ON public.agriculture_parcels;
CREATE TRIGGER agriculture_parcel_no_truncate BEFORE TRUNCATE ON public.agriculture_parcels
 FOR EACH STATEMENT EXECUTE FUNCTION public.agriculture_parcel_append_only();

DO $policies$ DECLARE n text; BEGIN
 FOREACH n IN ARRAY ARRAY['agriculture_parcels','agriculture_parcel_revisions',
  'agriculture_parcel_commands','agriculture_parcel_outbox'] LOOP
  EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY',n);
  EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY',n);
  EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC,flow_geo_app,flow_geo_worker,flow_geo_query_guard',n);
  EXECUTE format('GRANT SELECT,INSERT ON public.%I TO flow_geo_app',n);
  -- Earlier domain quarantine runs again during replay: remove only this table's
  -- closed policy, then restore the exact immutable-role/single-scope policies.
  EXECUTE format('DROP POLICY IF EXISTS agriculture_domain_closed ON public.%I',n);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_parcel_owner ON public.%I',n);
  EXECUTE format('CREATE POLICY agriculture_parcel_owner ON public.%I TO flow_geo_migrate USING(true) WITH CHECK(true)',n);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_parcel_runtime ON public.%I',n);
  EXECUTE format('CREATE POLICY agriculture_parcel_runtime ON public.%I TO flow_geo_app USING(true) WITH CHECK(true)',n);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_parcel_scope ON public.%I',n);
  EXECUTE format('CREATE POLICY agriculture_parcel_scope ON public.%I AS RESTRICTIVE TO flow_geo_app USING(tenant_id=nullif(current_setting(''app.tenant_ids'',true),'''') AND workspace_id=nullif(current_setting(''app.workspace_id'',true),'''')) WITH CHECK(tenant_id=nullif(current_setting(''app.tenant_ids'',true),'''') AND workspace_id=nullif(current_setting(''app.workspace_id'',true),''''))',n);
  IF n<>'agriculture_parcels' THEN
   EXECUTE format('DROP TRIGGER IF EXISTS agriculture_parcel_append_only ON public.%I',n);
   EXECUTE format('CREATE TRIGGER agriculture_parcel_append_only BEFORE UPDATE OR DELETE OR TRUNCATE ON public.%I FOR EACH STATEMENT EXECUTE FUNCTION public.agriculture_parcel_append_only()',n);
  END IF;
 END LOOP;
END $policies$;
GRANT UPDATE ON public.agriculture_parcels TO flow_geo_app;

COMMIT;
