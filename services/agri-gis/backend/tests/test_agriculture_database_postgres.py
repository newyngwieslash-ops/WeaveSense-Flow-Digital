"""Real single-connection runtime reuse; explicit synthetic RLS table, not domains."""

import asyncio
import os
import uuid
from datetime import UTC, datetime, timedelta

import asyncpg
import pytest
from sqlalchemy import Column, MetaData, String, Table, func, select, text
from sqlalchemy.exc import DBAPIError

from app.agriculture.database import GISRuntimeDatabase, create_runtime_pool
from app.agriculture.identity.verify import IdentityError, Verified


def configuration():
    owner = os.getenv("AGRI_TEST_GEO_MIGRATION_DSN")
    app = os.getenv("AGRI_TEST_GEO_RUNTIME_DSN")
    if not owner or not app or os.getenv("AGRI_TEST_PG_ISOLATED") != "1":
        pytest.skip("explicit isolated PostgreSQL migration/runtime DSNs required")
    return owner, app


def ticket(tenant, workspace):
    return Verified(
        {
            "tenant_id": tenant,
            "workspace_id": workspace,
            "actor": {"kind": "user", "id": "1fixture"},
        },
        datetime.now(UTC) + timedelta(seconds=25),
        {},
    )


def test_postgres_runtime_factory_role_admission():
    async def run():
        owner_dsn, app_dsn = configuration()
        for factory in (GISRuntimeDatabase.open, create_runtime_pool):
            with pytest.raises(IdentityError):
                await factory(owner_dsn)
            with pytest.raises(IdentityError):
                await factory(app_dsn, role="flow_geo_worker")
            resource = await factory(app_dsn)
            await resource.close()

    asyncio.run(run())


def test_postgres_worker_factory_admission_without_business_privileges():
    async def run():
        configuration()
        worker_dsn = os.getenv("AGRI_TEST_GEO_WORKER_DSN")
        if not worker_dsn:
            pytest.skip("explicit isolated worker DSN required")
        for factory in (GISRuntimeDatabase.open, create_runtime_pool):
            with pytest.raises(IdentityError):
                await factory(worker_dsn)
            runtime = await factory(worker_dsn, role="flow_geo_worker")
            await runtime.close()
        pool = await create_runtime_pool(worker_dsn, role="flow_geo_worker")
        try:
            async with pool.acquire() as connection:
                assert (
                    await connection.fetchval("SELECT current_user")
                    == "flow_geo_worker"
                )
                assert not await connection.fetchval("""SELECT EXISTS(
                  SELECT 1 FROM pg_catalog.pg_class c
                  JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
                  WHERE n.nspname='public' AND c.relkind IN ('r','p')
                  AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
                    WHERE d.classid='pg_catalog.pg_class'::regclass
                    AND d.objid=c.oid AND d.deptype='e'
                    AND d.refclassid='pg_catalog.pg_extension'::regclass)
                  AND has_table_privilege(current_user,c.oid,
                    'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER'))""")
        finally:
            await pool.close()

    asyncio.run(run())


def test_postgres_runtime_scope_pool_reuse_and_rollback():
    async def run():
        owner_dsn, app_dsn = configuration()
        name = "agri_scope_fixture_" + uuid.uuid4().hex
        table = Table(
            name,
            MetaData(),
            Column("id", String),
            Column("tenant_id", String),
            Column("workspace_id", String),
        )
        owner = await asyncpg.connect(owner_dsn)
        runtime = None
        try:
            # Owner creates a test-only relation. These policies verify factory
            # transaction behavior; production domain policies have separate tests.
            await owner.execute(f"""CREATE TABLE public.{name}
                (id text PRIMARY KEY, tenant_id text NOT NULL,
                 workspace_id text NOT NULL);
                INSERT INTO public.{name} VALUES
                ('a','tenant_a','workspace_a'),('b','tenant_a','workspace_b'),
                ('c','tenant_b','workspace_c');
                ALTER TABLE public.{name} ENABLE ROW LEVEL SECURITY;
                ALTER TABLE public.{name} FORCE ROW LEVEL SECURITY;
                CREATE POLICY scoped ON public.{name}
                USING (tenant_id=current_setting('app.tenant_ids',true)
                    AND workspace_id=current_setting('app.workspace_id',true))
                WITH CHECK (tenant_id=current_setting('app.tenant_ids',true)
                    AND workspace_id=current_setting('app.workspace_id',true));
                REVOKE ALL ON public.{name} FROM PUBLIC,flow_geo_app;
                GRANT SELECT,INSERT ON public.{name} TO flow_geo_app;""")
            runtime = await GISRuntimeDatabase.open(app_dsn, pool_size=1)
            async with runtime._engine.connect() as conn:
                pid = (await conn.execute(select(func.pg_backend_pid()))).scalar_one()
                assert (await conn.execute(select(table.c.id))).all() == []
                with pytest.raises(DBAPIError) as denied:
                    await conn.execute(
                        table.insert().values(
                            id="unscoped",
                            tenant_id="tenant_a",
                            workspace_id="workspace_a",
                        )
                    )
                assert denied.value.orig.sqlstate == "42501"
                await conn.rollback()

            for tenant, workspace, expected in (
                ("tenant_a", "workspace_a", ["a"]),
                ("tenant_a", "workspace_b", ["b"]),
                ("tenant_b", "workspace_c", ["c"]),
            ):
                async with runtime.session(ticket(tenant, workspace)) as db:
                    assert (
                        await db.execute(select(func.pg_backend_pid()))
                    ).scalar_one() == pid
                    assert (
                        await db.execute(select(table.c.id))
                    ).scalars().all() == expected
                    await db.rollback()
                    # after_begin must reapply scope for the next transaction.
                    assert (
                        await db.execute(select(table.c.id))
                    ).scalars().all() == expected

                async with runtime._engine.connect() as conn:
                    assert (
                        await conn.execute(select(func.pg_backend_pid()))
                    ).scalar_one() == pid
                    assert (await conn.execute(select(table.c.id))).all() == []
                    row = (
                        await conn.execute(
                            text(
                                "SELECT current_setting('app.tenant_ids',true), "
                                "current_setting('app.workspace_id',true)"
                            )
                        )
                    ).one()
                    assert all(v in (None, "") for v in row)

            with pytest.raises(RuntimeError, match="test_failure"):
                async with runtime.session(ticket("tenant_a", "workspace_a")) as db:
                    assert (await db.execute(select(table.c.id))).scalars().all() == [
                        "a"
                    ]
                    raise RuntimeError("test_failure")
            async with runtime._engine.connect() as conn:
                assert (await conn.execute(select(table.c.id))).all() == []
            with pytest.raises(IdentityError):
                async with runtime.session(None):
                    pytest.fail("unverified scope admitted")
        finally:
            if runtime is not None:
                await runtime.close()
            await owner.execute(f"DROP TABLE IF EXISTS public.{name}")
            await owner.close()

    asyncio.run(run())
