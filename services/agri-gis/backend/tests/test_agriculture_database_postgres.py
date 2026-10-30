"""Real single-connection reuse and production RLS with explicit domain fixtures."""

import asyncio
import os
from datetime import UTC, datetime, timedelta

import asyncpg
import pytest
from domain_fixture_support import install_domain_schema
from sqlalchemy import func, select, text

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
        await install_domain_schema(owner_dsn)
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
        owner_dsn, _ = configuration()
        await install_domain_schema(owner_dsn)
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
        from test_agriculture_domain_rls_postgres import fixtures

        from app.models.plot import FarmlandPlot

        _, app_dsn = configuration()
        table = FarmlandPlot.__table__
        async with fixtures() as (items, _, _owner_dsn):
            runtime = await GISRuntimeDatabase.open(app_dsn, pool_size=1)
            try:
                async with runtime._engine.connect() as conn:
                    pid = (
                        await conn.execute(select(func.pg_backend_pid()))
                    ).scalar_one()
                    assert (await conn.execute(select(table.c.plot_code))).all() == []
                    changed = await conn.execute(
                        table.update()
                        .values(crop_type="unscoped_fixture")
                        .returning(table.c.plot_code)
                    )
                    assert changed.all() == []
                    await conn.rollback()

                for item in items:
                    expected = [item["plot_code"]]
                    async with runtime.session(
                        ticket(item["tenant"], item["workspace"])
                    ) as db:
                        actual_pid = (
                            await db.execute(select(func.pg_backend_pid()))
                        ).scalar_one()
                        assert actual_pid == pid
                        assert (
                            await db.execute(select(table.c.plot_code))
                        ).scalars().all() == expected
                        await db.rollback()
                        # Same session, new transaction must reapply both scopes.
                        assert (
                            await db.execute(select(table.c.plot_code))
                        ).scalars().all() == expected
                    async with runtime._engine.connect() as conn:
                        assert (
                            await conn.execute(select(func.pg_backend_pid()))
                        ).scalar_one() == pid
                        assert (
                            await conn.execute(select(table.c.plot_code))
                        ).all() == []
                        row = (
                            await conn.execute(
                                text(
                                    "SELECT current_setting('app.tenant_ids',true), "
                                    "current_setting('app.workspace_id',true)"
                                )
                            )
                        ).one()
                        assert all(v in (None, "") for v in row)

                item = items[0]
                with pytest.raises(RuntimeError, match="test_failure"):
                    async with runtime.session(
                        ticket(item["tenant"], item["workspace"])
                    ) as db:
                        assert (
                            await db.execute(select(table.c.plot_code))
                        ).scalars().all() == [item["plot_code"]]
                        raise RuntimeError("test_failure")
                async with runtime._engine.connect() as conn:
                    assert (await conn.execute(select(table.c.plot_code))).all() == []
                with pytest.raises(IdentityError):
                    async with runtime.session(None):
                        pytest.fail("unverified scope admitted")
            finally:
                await runtime.close()

    asyncio.run(run())


def test_postgres_domain_factory_rejects_incomplete_boundary():
    async def run():
        owner_dsn, app_dsn = configuration()
        await install_domain_schema(owner_dsn)
        owner = await asyncpg.connect(owner_dsn)
        try:
            await owner.execute(
                "ALTER TABLE public.farmland_plots NO FORCE ROW LEVEL SECURITY"
            )
            with pytest.raises(IdentityError):
                await GISRuntimeDatabase.open(app_dsn)
            # The replay-only pool has its own independent identity-table gate;
            # constructing it is not a claim of domain access readiness.
            pool = await create_runtime_pool(app_dsn)
            await pool.close()
        finally:
            await owner.execute(
                "ALTER TABLE public.farmland_plots FORCE ROW LEVEL SECURITY"
            )
            await owner.close()
        database = await GISRuntimeDatabase.open(app_dsn)
        await database.close()

    asyncio.run(run())
