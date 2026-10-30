"""Real isolated domain RLS and legacy quarantine; no live farm acceptance."""

from contextlib import AsyncExitStack, asynccontextmanager
from uuid import uuid4

import asyncpg
import pytest
from domain_fixture_support import bind_fixture_plot, install_domain_schema
from sqlalchemy.exc import DBAPIError
from test_agriculture_actors_postgres import pg_config
from test_agriculture_legacy_write_integration import legacy_fixture_context

from app.agriculture.domain_boundary import (
    IDENTITY_TABLES,
    LEGACY_TABLES,
    require_domain_boundary,
)
from app.agriculture.identity.verify import IdentityError


@asynccontextmanager
async def fixture_context(owner_dsn, runtime_dsn, identity):
    generator = legacy_fixture_context(owner_dsn, runtime_dsn, identity)
    value = await anext(generator)
    try:
        yield value
    finally:
        await generator.aclose()


async def scope(connection, tenant, workspace):
    await connection.execute(
        "SELECT set_config('app.tenant_ids',$1,tru"
        "e),set_config('app.workspace_id',$2,true)",
        tenant,
        workspace,
    )


@asynccontextmanager
async def fixtures():
    owner_dsn, runtime_dsn = pg_config()
    await install_domain_schema(owner_dsn)
    suffix = uuid4().hex
    identities = [
        {
            "tenant": "tenant_a" + suffix,
            "workspace": "workspace_a" + suffix,
            "actor": "1" + suffix,
        },
        {
            "tenant": "tenant_a" + suffix,
            "workspace": "workspace_b" + suffix,
            "actor": "1" + suffix,
        },
        {
            "tenant": "tenant_b" + suffix,
            "workspace": "workspace_c" + suffix,
            "actor": "2" + suffix,
        },
    ]
    async with AsyncExitStack() as stack:
        items = [
            await stack.enter_async_context(
                fixture_context(owner_dsn, runtime_dsn, identity)
            )
            for identity in identities
        ]
        for item in items:
            await bind_fixture_plot(item["owner"], item)
            async with item["owner"].transaction():
                await scope(item["owner"], item["tenant"], item["workspace"])
                await item["owner"].execute(
                    "INSERT INTO agriculture_actor_audit "
                    "(id,tenant_id,workspace_id,actor_id,project_id,legacy_member_id,"
                    "legacy_user_code,legacy_role,policy_version,authorization_version,"
                    "request_id,action,resources,changes) "
                    "VALUES($1,$2,$3,$4,$5,$6,'legacy_a','interpreter','fixture',1,"
                    "'fixture_scope','fixture.scope','[]','[]')",
                    str(uuid4()),
                    item["tenant"],
                    item["workspace"],
                    item["actor"],
                    item["project"],
                    item["member"],
                )
                await item["owner"].execute(
                    "INSERT INTO agriculture_identity_replay "
                    "(jti,tenant_id,workspace_id,expires_at) VALUES($1,$2,$3,"
                    "clock_timestamp()+interval '1 minute')",
                    "fixture_" + uuid4().hex,
                    item["tenant"],
                    item["workspace"],
                )
        runtime = await asyncpg.connect(runtime_dsn)
        stack.push_async_callback(runtime.close)
        yield items, runtime, owner_dsn


@pytest.mark.asyncio
async def test_postgres_domain_scope_inventory_and_unmapped_denial():
    async with fixtures() as (items, runtime, owner_dsn):
        a, b, foreign = items
        owner = a["owner"]
        await require_domain_boundary(runtime)
        count = await owner.fetchval(
            "SELECT count(*) FROM pg_class c JOIN p"
            "g_namespace n ON n.oid=c.relnamespace "
            "WHERE n.nspname='public' AND c.relname=ANY($1) "
            "AND c.relrowsecurity AND c.relforcerowsecurity",
            sorted(LEGACY_TABLES),
        )
        assert count == 93
        assert await runtime.fetchval("SELECT count(*) FROM farmland_plots") == 0
        assert await runtime.fetchval("SELECT count(*) FROM monitoring_projects") == 0
        for table in IDENTITY_TABLES:
            assert await runtime.fetchval(f"SELECT count(*) FROM public.{table}") == 0
        with pytest.raises(asyncpg.InsufficientPrivilegeError):
            await runtime.execute("UPDATE project_users SET role_code='admin'")
        assert (
            await owner.fetchval(
                "SELECT role_code FROM project_users WHERE id=$1", a["member"]
            )
            == "interpreter"
        )
        with pytest.raises(asyncpg.InsufficientPrivilegeError):
            await runtime.execute("SELECT * FROM imagery_assets")
        with pytest.raises(asyncpg.InsufficientPrivilegeError):
            await runtime.execute("DELETE FROM agriculture_legacy_plot_ownership")
        # Old global geometry remains stored, but no tenant is guessed for it.
        unbound = "unbound_" + uuid4().hex
        await owner.execute(
            "INSERT INTO farmland_plots(plot_code,geom,cus"
            "tom_attributes,interpretation_status,version) "
            "SELECT $1,geom,'{}','interpreting',1 "
            "FROM farmland_plots WHERE plot_code=$2",
            unbound,
            a["plot_code"],
        )
        for item in items:
            async with runtime.transaction():
                await scope(runtime, item["tenant"], item["workspace"])
                for table in IDENTITY_TABLES:
                    assert (
                        await runtime.fetchval(
                            f"SELECT count(*) FROM public.{table} "
                            "WHERE workspace_id=$1",
                            item["workspace"],
                        )
                        == 1
                    )
                    assert (
                        await runtime.fetchval(
                            f"SELECT count(*) FROM public.{table} "
                            "WHERE workspace_id<>$1",
                            item["workspace"],
                        )
                        == 0
                    )
                assert (
                    await runtime.fetchval(
                        "SELECT count(*) FROM farmland_plots WHERE plot_code=$1",
                        item["plot_code"],
                    )
                    == 1
                )
                assert (
                    await runtime.fetchval(
                        "SELECT count(*) FROM farmland_plots WHERE plot_code=$1",
                        unbound,
                    )
                    == 0
                )
                assert (
                    await runtime.fetchval(
                        "SELECT count(*) FROM farmland_plots WHERE plot_code=ANY($1)",
                        [x["plot_code"] for x in items if x is not item],
                    )
                    == 0
                )
                assert (
                    await runtime.execute(
                        "UPDATE farmland_plots SET crop_t"
                        "ype='fixture' WHERE plot_code=$1",
                        item["plot_code"],
                    )
                    == "UPDATE 1"
                )
            # Same physical connection, no session-level scope leakage.
            assert await runtime.fetchval("SELECT count(*) FROM farmland_plots") == 0
            assert not await runtime.fetchval(
                "SELECT current_setting('app.workspace_id',true)"
            )
        async with runtime.transaction():
            await scope(runtime, a["tenant"] + "," + foreign["tenant"], a["workspace"])
            assert await runtime.fetchval("SELECT count(*) FROM farmland_plots") == 0
            for table in IDENTITY_TABLES:
                assert (
                    await runtime.fetchval(f"SELECT count(*) FROM public.{table}") == 0
                )
        async with runtime.transaction():
            await scope(runtime, a["tenant"], "")
            assert await runtime.fetchval("SELECT count(*) FROM farmland_plots") == 0
            for table in IDENTITY_TABLES:
                assert (
                    await runtime.fetchval(f"SELECT count(*) FROM public.{table}") == 0
                )
        async with runtime.transaction():
            await scope(runtime, a["tenant"], a["workspace"])
            with pytest.raises(asyncpg.InsufficientPrivilegeError):
                async with runtime.transaction():
                    await runtime.execute(
                        "INSERT INTO agriculture_identity_replay "
                        "(jti,tenant_id,workspace_id,expires_at) VALUES($1,$2,$3,"
                        "clock_timestamp()+interval '1 minute')",
                        "fixture_" + uuid4().hex,
                        a["tenant"],
                        b["workspace"],
                    )
        # Future tables have no default runtime grant, before any policy exists.
        future = "fixture_future_" + uuid4().hex
        await owner.execute(f"CREATE TABLE public.{future}(id integer)")
        try:
            assert not await runtime.fetchval(
                "SELECT has_table_privilege(current_u"
                "ser,$1,'SELECT,INSERT,UPDATE,DELETE')",
                "public." + future,
            )
            await install_domain_schema(owner_dsn)
            assert await owner.fetchval(
                "SELECT relrowsecurity AND relforcerowsecu"
                "rity FROM pg_class WHERE oid=$1::regclass",
                "public." + future,
            )
        finally:
            await owner.execute(f"DROP TABLE public.{future}")
        assert (
            await owner.fetchval(
                "SELECT count(*) FROM farmland_plots WHERE plot_code=$1", unbound
            )
            == 1
        )


@pytest.mark.asyncio
async def test_postgres_domain_cross_workspace_parent_and_policy_denial():
    async with fixtures() as (items, runtime, _):
        a, b, foreign = items
        owner = a["owner"]
        policy = "fixture_permissive_" + uuid4().hex
        await owner.execute(
            f"CREATE POLICY {policy} ON farmland_plots T"
            f"O flow_geo_app USING(true) WITH CHECK(true)"
        )
        try:
            assert await runtime.fetchval("SELECT count(*) FROM farmland_plots") == 0
            async with runtime.transaction():
                await scope(runtime, a["tenant"], a["workspace"])
                assert (
                    await runtime.fetchval(
                        "SELECT count(*) FROM farmland_plots WHERE plot_code=$1",
                        b["plot_code"],
                    )
                    == 0
                )
                assert (
                    await runtime.execute(
                        "UPDATE farmland_plots SET crop_ty"
                        "pe='forbidden' WHERE plot_code=$1",
                        foreign["plot_code"],
                    )
                    == "UPDATE 0"
                )
                with pytest.raises(asyncpg.InsufficientPrivilegeError):
                    async with runtime.transaction():
                        await runtime.execute(
                            "INSERT INTO plot_versions(plot"
                            "_code,version,geom,land_class,"
                            "custom_attributes,interpretation_status,created_by) "
                            "SELECT $1,2,geom,'fixture','{}','interpreting','fixture' "
                            "FROM farmland_plots WHERE plot_code=$2",
                            b["plot_code"],
                            a["plot_code"],
                        )
            # A legacy inconsistent parent link is not a shortcut to B's plot.
            await owner.execute(
                "INSERT INTO task_plots(task_id,plot_co"
                "de,assigned_by) VALUES($1,$2,'fixture')",
                a["ids"][0],
                b["plot_code"],
            )
            async with runtime.transaction():
                await scope(runtime, a["tenant"], a["workspace"])
                assert (
                    await runtime.fetchval(
                        "SELECT count(*) FROM task_plots "
                        "WHERE task_id=$1 AND plot_code=$2",
                        a["ids"][0],
                        b["plot_code"],
                    )
                    == 0
                )
            # Explicit unique plot ownership cannot be claimed by another project.
            with pytest.raises(asyncpg.UniqueViolationError):
                await owner.execute(
                    "INSERT INTO agriculture_legacy_plot_o"
                    "wnership VALUES($1,$2,$3,$4,'active')",
                    a["plot_code"],
                    b["tenant"],
                    b["workspace"],
                    b["project"],
                )
        finally:
            await owner.execute(f"DROP POLICY {policy} ON farmland_plots")


@pytest.mark.asyncio
async def test_postgres_domain_workbench_commit_and_audit_rollback():
    """Post-migration service component proof; HTTP signatures are tested by Go."""
    async with fixtures() as (items, runtime, _):
        a = items[0]
        assert a["domain_migrated"] and a["runtime_factory"] is not None
        before = await a["snapshot"]()
        await a["mutate"]()
        after = await a["snapshot"]()
        assert after[0] == ("玉米", 2)
        assert after[1] == before[1] + 1 and after[2] == before[2] + 1
        assert after[3] > before[3] and after[4:6] == before[4:6]
        async with runtime.transaction():
            await scope(runtime, a["tenant"], a["workspace"])
            assert (
                await runtime.fetchval(
                    "SELECT user_code FROM project_users WHERE id=$1", a["member"]
                )
                == "legacy_a"
            )
            audit = await runtime.fetch(
                "SELECT actor_id,legacy_user_code,legacy_role "
                "FROM agriculture_actor_audit "
                "WHERE tenant_id=$1 AND workspace_id=$2",
                a["tenant"],
                a["workspace"],
            )
            assert audit and all(
                tuple(row) == (a["actor"], "legacy_a", "interpreter") for row in audit
            )
        with pytest.raises(IdentityError):
            await a["mutate"](operator="admin")
        assert await a["snapshot"]() == after
        owner = a["owner"]
        name = "fixture_domain_audit_fail_" + uuid4().hex
        await owner.execute(
            f"CREATE FUNCTION public.{name}() RETURNS trigger LANGUAGE plpgsql AS $$ "
            f"BEGIN IF NEW.tenant_id = '{a['tenant']}' THEN "
            "RAISE EXCEPTION 'fixture audit unavailable' USING ERRCODE='23514'; "
            "END IF; RETURN NEW; END $$"
        )
        await owner.execute(
            f"CREATE TRIGGER {name} BEFORE INSERT ON agriculture_actor_audit "
            f"FOR EACH ROW EXECUTE FUNCTION public.{name}()"
        )
        try:
            with pytest.raises(DBAPIError):
                await a["mutate"]()
            assert await a["snapshot"]() == after
        finally:
            await owner.execute(f"DROP TRIGGER {name} ON agriculture_actor_audit")
            await owner.execute(f"DROP FUNCTION public.{name}()")
