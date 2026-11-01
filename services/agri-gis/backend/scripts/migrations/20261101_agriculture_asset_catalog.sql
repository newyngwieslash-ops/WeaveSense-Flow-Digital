BEGIN;

CREATE TABLE IF NOT EXISTS public.agriculture_asset_catalog (
 tenant_id text NOT NULL, workspace_id text NOT NULL,
 asset_id text NOT NULL CHECK(asset_id ~ '^asset_[0-9a-f]{32}$'),
 farm_id text, kind text NOT NULL CHECK(kind IN ('raster','vector','table','photo','audio','model')),
 data_class text NOT NULL CHECK(data_class IN ('demo','operational')),
 current_revision bigint NOT NULL CHECK(current_revision BETWEEN 1 AND 9007199254740991),
 current_version_id text NOT NULL CHECK(current_version_id ~ '^av_[0-9a-f]{32}$'),
 status text NOT NULL CHECK(status IN ('validating','ready','quarantined','rejected','revoked')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,asset_id),
 FOREIGN KEY(tenant_id,workspace_id,farm_id) REFERENCES public.agriculture_farms(tenant_id,workspace_id,farm_id) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS public.agriculture_asset_catalog_versions (
 tenant_id text NOT NULL, workspace_id text NOT NULL, asset_id text NOT NULL,
 version bigint NOT NULL CHECK(version BETWEEN 1 AND 9007199254740991),
 asset_version_id text NOT NULL CHECK(asset_version_id ~ '^av_[0-9a-f]{32}$'),
 metadata_json jsonb NOT NULL CHECK(jsonb_typeof(metadata_json)='object'),
 metadata_hash text NOT NULL CHECK(metadata_hash ~ '^[0-9a-f]{64}$'),
 rights_snapshot jsonb NOT NULL CHECK(jsonb_typeof(rights_snapshot)='object'),
 object_id text, object_version bigint,
 created_by text NOT NULL, created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,asset_id,version),
 UNIQUE(tenant_id,workspace_id,asset_id,asset_version_id),
 CHECK((object_id IS NULL)=(object_version IS NULL)),
 FOREIGN KEY(tenant_id,workspace_id,asset_id) REFERENCES public.agriculture_asset_catalog ON DELETE RESTRICT,
 FOREIGN KEY(tenant_id,workspace_id,object_id,object_version) REFERENCES public.agriculture_private_objects(tenant_id,workspace_id,object_id,version) ON DELETE RESTRICT
);
ALTER TABLE public.agriculture_asset_catalog DROP CONSTRAINT IF EXISTS agriculture_asset_current_version;
ALTER TABLE public.agriculture_asset_catalog ADD CONSTRAINT agriculture_asset_current_version
 FOREIGN KEY(tenant_id,workspace_id,asset_id,current_version_id)
 REFERENCES public.agriculture_asset_catalog_versions(tenant_id,workspace_id,asset_id,asset_version_id)
 DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE IF NOT EXISTS public.agriculture_asset_catalog_commands (
 tenant_id text NOT NULL, workspace_id text NOT NULL, operation_id text NOT NULL,
 actor_id text NOT NULL, action text NOT NULL CHECK(action IN ('asset.create','asset.version')),
 request_hash text NOT NULL CHECK(request_hash ~ '^[0-9a-f]{64}$'),
 grant_plan_hash text NOT NULL CHECK(grant_plan_hash ~ '^[0-9a-f]{64}$'),
 receipt_json jsonb NOT NULL CHECK(jsonb_typeof(receipt_json)='object'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,operation_id)
);
CREATE TABLE IF NOT EXISTS public.agriculture_asset_catalog_outbox (
 tenant_id text NOT NULL, workspace_id text NOT NULL, event_id text NOT NULL,
 operation_id text NOT NULL, asset_id text NOT NULL, revision bigint NOT NULL CHECK(revision>0),
 event_type text NOT NULL CHECK(event_type IN ('agri.asset.registered','agri.asset.version_registered')),
 payload_json jsonb NOT NULL CHECK(jsonb_typeof(payload_json)='object'),
 payload_hash text NOT NULL CHECK(payload_hash ~ '^[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(tenant_id,workspace_id,event_id), UNIQUE(tenant_id,workspace_id,operation_id),
 FOREIGN KEY(tenant_id,workspace_id,asset_id) REFERENCES public.agriculture_asset_catalog ON DELETE RESTRICT
);

CREATE OR REPLACE FUNCTION public.agriculture_asset_catalog_append_only()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN RAISE EXCEPTION 'immutable asset catalog history' USING ERRCODE='23514'; END;
$$;
REVOKE ALL ON FUNCTION public.agriculture_asset_catalog_append_only() FROM PUBLIC;
CREATE OR REPLACE FUNCTION public.agriculture_asset_catalog_transition()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $$
BEGIN
 IF TG_OP='DELETE' THEN RAISE EXCEPTION 'asset history must be retained' USING ERRCODE='23514'; END IF;
 IF ROW(NEW.tenant_id,NEW.workspace_id,NEW.asset_id,NEW.farm_id,NEW.kind,NEW.data_class,NEW.created_at)
  IS DISTINCT FROM ROW(OLD.tenant_id,OLD.workspace_id,OLD.asset_id,OLD.farm_id,OLD.kind,OLD.data_class,OLD.created_at)
 THEN RAISE EXCEPTION 'immutable asset identity' USING ERRCODE='23514'; END IF;
 IF OLD.status='revoked' OR NEW.current_revision<>OLD.current_revision+1 OR NEW.current_version_id=OLD.current_version_id
 THEN RAISE EXCEPTION 'invalid asset transition' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION public.agriculture_asset_catalog_transition() FROM PUBLIC;
DROP TRIGGER IF EXISTS agriculture_asset_catalog_transition ON public.agriculture_asset_catalog;
CREATE TRIGGER agriculture_asset_catalog_transition BEFORE UPDATE OR DELETE ON public.agriculture_asset_catalog
 FOR EACH ROW EXECUTE FUNCTION public.agriculture_asset_catalog_transition();

CREATE OR REPLACE FUNCTION public.agriculture_asset_version_guard()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
DECLARE head public.agriculture_asset_catalog%ROWTYPE; prior jsonb; old_until timestamptz; new_until timestamptz;
BEGIN
 IF jsonb_typeof(NEW.metadata_json->'retention') IS DISTINCT FROM 'object'
  OR jsonb_typeof(NEW.metadata_json->'retention'->'legal_hold') IS DISTINCT FROM 'boolean'
  OR NOT (NEW.metadata_json->'retention' ? 'retain_until')
  OR COALESCE(NEW.metadata_json->>'sensitivity','') NOT IN ('public','internal','confidential','restricted')
 THEN RAISE EXCEPTION 'asset security metadata missing' USING ERRCODE='23514'; END IF;
 SELECT * INTO head FROM public.agriculture_asset_catalog
  WHERE tenant_id=NEW.tenant_id AND workspace_id=NEW.workspace_id AND asset_id=NEW.asset_id;
 IF NOT FOUND OR head.current_revision<>NEW.version OR head.current_version_id<>NEW.asset_version_id
  OR NEW.metadata_json->>'kind' IS DISTINCT FROM head.kind
  OR NEW.metadata_json->>'data_class' IS DISTINCT FROM head.data_class
  OR NEW.metadata_json->>'farm_id' IS DISTINCT FROM head.farm_id
 THEN RAISE EXCEPTION 'asset version identity mismatch' USING ERRCODE='23514'; END IF;
 IF NEW.version>1 THEN
  SELECT metadata_json INTO prior FROM public.agriculture_asset_catalog_versions
   WHERE tenant_id=NEW.tenant_id AND workspace_id=NEW.workspace_id AND asset_id=NEW.asset_id AND version=NEW.version-1;
  IF NOT FOUND THEN RAISE EXCEPTION 'asset version predecessor missing' USING ERRCODE='23514'; END IF;
  IF (prior->'retention'->>'legal_hold')::boolean AND NOT (NEW.metadata_json->'retention'->>'legal_hold')::boolean
   THEN RAISE EXCEPTION 'asset hold cannot be released' USING ERRCODE='23514'; END IF;
  old_until:=(prior->'retention'->>'retain_until')::timestamptz;
  new_until:=(NEW.metadata_json->'retention'->>'retain_until')::timestamptz;
  IF new_until IS NOT NULL AND (old_until IS NULL OR new_until<old_until)
   THEN RAISE EXCEPTION 'asset retention cannot be reduced' USING ERRCODE='23514'; END IF;
  IF array_position(ARRAY['public','internal','confidential','restricted'],NEW.metadata_json->>'sensitivity')
   < array_position(ARRAY['public','internal','confidential','restricted'],prior->>'sensitivity')
   THEN RAISE EXCEPTION 'asset sensitivity cannot be reduced' USING ERRCODE='23514'; END IF;
 END IF;
 IF head.status='ready' AND NEW.object_id IS NULL
  THEN RAISE EXCEPTION 'ready asset requires verified entity' USING ERRCODE='23514'; END IF;
 IF NEW.object_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM public.agriculture_private_objects o
   WHERE o.tenant_id=NEW.tenant_id AND o.workspace_id=NEW.workspace_id AND o.object_id=NEW.object_id AND o.version=NEW.object_version
   AND o.resource_kind='asset' AND o.resource_id=NEW.asset_id AND o.resource_revision=NEW.version
   AND o.data_class=head.data_class AND o.state='active' AND o.distribution_allowed)
  THEN RAISE EXCEPTION 'asset entity binding mismatch' USING ERRCODE='23514'; END IF;
 RETURN NEW;
END;
$$;
REVOKE ALL ON FUNCTION public.agriculture_asset_version_guard() FROM PUBLIC;
DROP TRIGGER IF EXISTS agriculture_asset_version_guard ON public.agriculture_asset_catalog_versions;
CREATE TRIGGER agriculture_asset_version_guard BEFORE INSERT ON public.agriculture_asset_catalog_versions
 FOR EACH ROW EXECUTE FUNCTION public.agriculture_asset_version_guard();

DO $$
DECLARE relation text;
BEGIN
 REVOKE UPDATE(current_revision,current_version_id,status,updated_at) ON public.agriculture_asset_catalog FROM flow_geo_app;
 FOREACH relation IN ARRAY ARRAY['agriculture_asset_catalog','agriculture_asset_catalog_versions','agriculture_asset_catalog_commands','agriculture_asset_catalog_outbox'] LOOP
  EXECUTE format('ALTER TABLE public.%I ENABLE ROW LEVEL SECURITY',relation);
  EXECUTE format('ALTER TABLE public.%I FORCE ROW LEVEL SECURITY',relation);
  EXECUTE format('REVOKE ALL ON public.%I FROM PUBLIC,flow_geo_app,flow_geo_worker,flow_geo_query_guard',relation);
  EXECUTE format('GRANT SELECT,INSERT ON public.%I TO flow_geo_app',relation);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_asset_owner ON public.%I',relation);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_asset_runtime ON public.%I',relation);
  EXECUTE format('DROP POLICY IF EXISTS agriculture_asset_scope ON public.%I',relation);
  EXECUTE format('CREATE POLICY agriculture_asset_owner ON public.%I TO flow_geo_migrate USING(true) WITH CHECK(true)',relation);
  EXECUTE format('CREATE POLICY agriculture_asset_runtime ON public.%I TO flow_geo_app USING(true) WITH CHECK(true)',relation);
  EXECUTE format('CREATE POLICY agriculture_asset_scope ON public.%I AS RESTRICTIVE TO flow_geo_app USING(tenant_id=nullif(current_setting(''app.tenant_ids'',true),'''') AND workspace_id=nullif(current_setting(''app.workspace_id'',true),'''')) WITH CHECK(tenant_id=nullif(current_setting(''app.tenant_ids'',true),'''') AND workspace_id=nullif(current_setting(''app.workspace_id'',true),''''))',relation);
  IF relation<>'agriculture_asset_catalog' THEN
   EXECUTE format('DROP TRIGGER IF EXISTS agriculture_asset_immutable ON public.%I',relation);
   EXECUTE format('CREATE TRIGGER agriculture_asset_immutable BEFORE UPDATE OR DELETE ON public.%I FOR EACH ROW EXECUTE FUNCTION public.agriculture_asset_catalog_append_only()',relation);
  END IF;
  EXECUTE format('DROP POLICY IF EXISTS agriculture_domain_closed ON public.%I',relation);
 END LOOP;
END $$;
GRANT UPDATE(current_revision,current_version_id,status,updated_at) ON public.agriculture_asset_catalog TO flow_geo_app;

COMMIT;
