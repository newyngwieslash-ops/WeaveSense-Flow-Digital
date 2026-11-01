"""Real PostgreSQL lease fixture ONLY; not an agricultural W06 scheduler."""

from pathlib import Path

import asyncpg

from app.agriculture.identity.providers import Ownership
from app.agriculture.identity.verify import IdentityError, require


async def prepare_jobs(owner_dsn):
    owner = await asyncpg.connect(owner_dsn)
    root = Path(__file__).parents[1] / "scripts/migrations"
    for name in (
        "20261029_agriculture_identity_replay.sql",
        "20261030_agriculture_job_authority.sql",
    ):
        await owner.execute((root / name).read_text())
    await owner.execute("""
CREATE TABLE IF NOT EXISTS public.fixture_job_leases (
 tenant_id text NOT NULL, workspace_id text NOT NULL, job_id text NOT NULL,
 worker_id text NOT NULL, attempt bigint NOT NULL, fencing bigint NOT NULL,
 state text NOT NULL, PRIMARY KEY(tenant_id,workspace_id,job_id));
CREATE TABLE IF NOT EXISTS public.fixture_job_resources (
 tenant_id text NOT NULL, workspace_id text NOT NULL, kind text NOT NULL,
 resource_id text NOT NULL, farm_id text NOT NULL, revision bigint NOT NULL,
 state text NOT NULL, PRIMARY KEY(tenant_id,workspace_id,kind,resource_id));
REVOKE ALL ON public.fixture_job_leases,public.fixture_job_resources
 FROM PUBLIC,flow_geo_app,flow_geo_worker;
GRANT SELECT ON public.fixture_job_leases,public.fixture_job_resources
 TO flow_geo_app,flow_geo_worker;
ALTER TABLE public.fixture_job_leases ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.fixture_job_leases FORCE ROW LEVEL SECURITY;
ALTER TABLE public.fixture_job_resources ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.fixture_job_resources FORCE ROW LEVEL SECURITY;
DO $$ DECLARE n text; BEGIN
FOREACH n IN ARRAY ARRAY['fixture_job_leases','fixture_job_resources'] LOOP
 EXECUTE format('DROP POLICY IF EXISTS fixture_owner ON public.%I',n);
 EXECUTE format('CREATE POLICY fixture_owner ON public.%I
 TO flow_geo_migrate USING(true) WITH CHECK(true)',n);
 EXECUTE format('DROP POLICY IF EXISTS fixture_scope ON public.%I',n);
 EXECUTE format('CREATE POLICY fixture_scope ON public.%I
 TO flow_geo_app,flow_geo_worker
 USING(tenant_id=nullif(current_setting(''app.tenant_ids'',true),'''')
 AND workspace_id=nullif(current_setting(''app.workspace_id'',true),''''))',n);
END LOOP; END $$;
""")
    return owner


async def scope(connection, tenant, workspace):
    await connection.execute(
        "SELECT set_config('app.tenant_ids',$1,true),"
        "set_config('app.workspace_id',$2,true)",
        tenant,
        workspace,
    )


class FixtureLeaseProvider:
    def __init__(self, pool):
        self.pool = pool

    async def check(self, snapshot, checkpoint):
        async with self.pool.acquire() as connection:
            async with connection.transaction():
                await scope(connection, snapshot["tenant_id"], snapshot["workspace_id"])
                row = await connection.fetchrow(
                    "SELECT worker_id,attempt,fencing,state "
                    "FROM public.fixture_job_leases "
                    "WHERE tenant_id=$1 AND workspace_id=$2 AND job_id=$3",
                    snapshot["tenant_id"],
                    snapshot["workspace_id"],
                    checkpoint.job_id,
                )
        require(
            row is not None
            and row["state"] == "running"
            and row["worker_id"] == checkpoint.worker_id
            and row["attempt"] == checkpoint.attempt
            and row["fencing"] == checkpoint.fencing,
            "job_lease_denied",
            403,
        )


class FixtureJobOwnership:
    def __init__(self, pool):
        self.pool = pool

    async def resolve(self, tenant_id, workspace_id, resources):
        facts = []
        async with self.pool.acquire() as connection:
            async with connection.transaction():
                await scope(connection, tenant_id, workspace_id)
                for ref in resources:
                    row = await connection.fetchrow(
                        "SELECT * FROM public.fixture_job_resources "
                        "WHERE tenant_id=$1 AND workspace_id=$2 "
                        "AND kind=$3 AND resource_id=$4",
                        tenant_id,
                        workspace_id,
                        ref["kind"],
                        ref["id"],
                    )
                    if row is None:
                        raise IdentityError("resource_not_found", 404)
                    facts.append(
                        Ownership(
                            row["kind"],
                            row["resource_id"],
                            row["tenant_id"],
                            row["workspace_id"],
                            (row["farm_id"],),
                            row["revision"],
                            row["state"],
                        )
                    )
        return facts
