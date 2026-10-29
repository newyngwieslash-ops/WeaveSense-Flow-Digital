-- Execute only with flow_geo_migrate in flow_geo; never a platform migration.
-- No legacy GIS data migration or broad old-table RLS claim is made here.
BEGIN;
DO $scope$
BEGIN
    IF current_database() <> 'flow_geo' OR current_user <> 'flow_geo_migrate' THEN
        RAISE EXCEPTION 'identity replay migration requires geo migration ownership';
    END IF;
END;
$scope$;
CREATE TABLE IF NOT EXISTS public.agriculture_identity_replay (
    jti varchar(128) PRIMARY KEY CHECK (jti ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
    tenant_id varchar(128) NOT NULL CHECK (tenant_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
    workspace_id varchar(128) NOT NULL CHECK (workspace_id ~ '^[A-Za-z][A-Za-z0-9_-]{0,127}$'),
    expires_at timestamptz NOT NULL,
    claimed_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS agriculture_identity_replay_expiry ON public.agriculture_identity_replay(expires_at);
ALTER TABLE public.agriculture_identity_replay ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agriculture_identity_replay FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS agriculture_identity_replay_scope ON public.agriculture_identity_replay;
CREATE POLICY agriculture_identity_replay_scope ON public.agriculture_identity_replay
 USING (tenant_id <> '' AND tenant_id = ANY(string_to_array(current_setting('app.tenant_ids', true), ',')))
 WITH CHECK (tenant_id <> '' AND tenant_id = ANY(string_to_array(current_setting('app.tenant_ids', true), ',')));
REVOKE ALL ON public.agriculture_identity_replay FROM PUBLIC;
REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON public.agriculture_identity_replay FROM flow_geo_app;
GRANT SELECT, INSERT ON public.agriculture_identity_replay TO flow_geo_app;
COMMIT;
