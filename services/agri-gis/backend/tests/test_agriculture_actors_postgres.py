"""Real isolated geo roles/RLS/audit transactions; no live farm data."""

import asyncio
import os
import uuid
from dataclasses import replace
from datetime import UTC, datetime, timedelta
from pathlib import Path

import asyncpg
import pytest
from app.agriculture.actor_session import AgricultureAsyncSession, AuditAdapter
from app.agriculture.actors import resolve_trusted_actor
from app.agriculture.identity.verify import IdentityError
from app.models.workbench import MonitoringProject, ProjectUser
from sqlalchemy.engine import make_url
from sqlalchemy.ext.asyncio import create_async_engine
from test_agriculture_actors import ticket


def pg_config():
    owner = os.getenv("AGRI_TEST_GEO_MIGRATION_DSN")
    runtime = os.getenv("AGRI_TEST_GEO_RUNTIME_DSN")
    if os.getenv("AGRI_TEST_PG_ISOLATED") != "1" or not owner or not runtime:
        pytest.skip("not_run: isolated geo migration/runtime DSNs required")
    return owner, runtime


def engine_for(dsn):
    url = make_url(dsn).set(drivername="postgresql+asyncpg")
    sslmode = url.query.get("sslmode")
    assert sslmode in (None, "disable"), "only isolated fixture DSNs supported"
    url = url.difference_update_query(["sslmode"])
    return create_async_engine(
        url, connect_args={"ssl": False} if sslmode else {}, hide_parameters=True
    )


async def prepare(owner_dsn, identity=None):
    engine = engine_for(owner_dsn)
    async with engine.begin() as conn:
        await conn.run_sync(
            lambda c: MonitoringProject.__table__.create(c, checkfirst=True)
        )
        await conn.run_sync(lambda c: ProjectUser.__table__.create(c, checkfirst=True))
    await engine.dispose()
    owner = await asyncpg.connect(owner_dsn)
    migration = (
        Path(__file__).parents[1]
        / "scripts/migrations/20261029_agriculture_actor_bindings.sql"
    )
    await owner.execute(migration.read_text())
    await owner.execute(migration.read_text())
    suffix = uuid.uuid4().hex
    tenant, workspace, actor = (
        "tenant_" + suffix,
        "workspace_" + suffix,
        "7" + suffix + "x" * 95,
    )
    if identity is not None:
        # Trusted test-process configuration, never HTTP claims or browser input.
        tenant, workspace, actor = (
            identity["tenant"],
            identity["workspace"],
            identity["actor"],
        )
    async with owner.transaction():
        await owner.execute("SELECT set_config('app.tenant_ids',$1,true)", tenant)
        project = await owner.fetchval(
            "INSERT INTO monitoring_projects(project_code,project_name,province,"
            "monitor_year,status,progress) "
            "VALUES($1,'fixture','fixture',2026,'active',0) RETURNING id",
            "project_" + suffix,
        )
        member = await owner.fetchval(
            "INSERT INTO project_users(project_id,user_code,display_name,role_code,"
            "role_name,status,is_default) "
            "VALUES($1,'legacy_a','fixture','interpreter','fixture','active',true) "
            "RETURNING id",
            project,
        )
        await owner.execute(
            "INSERT INTO agriculture_project_bindings VALUES($1,$2,$3,'active')",
            tenant,
            workspace,
            project,
        )
        await owner.execute(
            "INSERT INTO agriculture_actor_bindings VALUES($1,$2,$3,$4,$5,'active')",
            tenant,
            workspace,
            actor,
            project,
            member,
        )
    return owner, tenant, workspace, actor, project, member


@pytest.mark.asyncio
async def test_postgres_actor_mapping_strict_scope_and_atomic_audit():
    owner_dsn, runtime_dsn = pg_config()
    owner, tenant, workspace, actor, project, member = await prepare(owner_dsn)
    runtime = await asyncpg.connect(runtime_dsn)
    engine = engine_for(runtime_dsn)
    adapter = AuditAdapter(
        ProjectUser,
        "agri.farm.update",
        frozenset({"update"}),
        lambda obj, actor: (("farm", "farm_a"),),
    )
    try:
        assert (
            await runtime.fetchval("SELECT count(*) FROM agriculture_actor_bindings")
            == 0
        )
        assert not await runtime.fetchval(
            "SELECT pg_has_role(current_user,'flow_geo_actor_guard','MEMBER')"
        )
        with pytest.raises(asyncpg.InsufficientPrivilegeError):
            await runtime.execute("SET ROLE flow_geo_actor_guard")
        assert not await runtime.fetchval(
            "SELECT rolcanlogin OR rolsuper OR rolbypassrls OR rolinherit "
            "FROM pg_roles WHERE rolname='flow_geo_actor_guard'"
        )
        assert not await runtime.fetchval(
            "SELECT EXISTS(SELECT 1 FROM pg_proc p, LATERAL aclexplode(p.proacl) a "
            "WHERE p.oid='public.agriculture_lock_actor(text,text,text,integer)'"
            "::regprocedure AND a.grantee=0 AND a.privilege_type='EXECUTE')"
        )
        assert not await runtime.fetchval(
            "SELECT has_schema_privilege('flow_geo_actor_guard','public','CREATE')"
        )
        assert not await runtime.fetchval(
            "SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user"
        )
        assert not await runtime.fetchval(
            "SELECT public.agriculture_lock_actor($1,$2,$3,$4)",
            tenant,
            workspace,
            actor,
            project,
        )
        async with runtime.transaction():
            await runtime.execute("SELECT set_config('app.tenant_ids',$1,true)", tenant)
            assert (
                await runtime.fetchval(
                    "SELECT count(*) FROM agriculture_actor_bindings "
                    "WHERE tenant_id=$1",
                    tenant,
                )
                == 1
            )
            for statement in (
                "UPDATE agriculture_actor_bindings SET state='revoked'",
                "DELETE FROM agriculture_project_bindings",
                "INSERT INTO agriculture_project_bindings "
                "VALUES('other','other',999,'active')",
            ):
                with pytest.raises(asyncpg.InsufficientPrivilegeError):
                    async with runtime.transaction():
                        await runtime.execute(statement)
        async with AgricultureAsyncSession(
            engine, audit_adapters=(adapter,), expire_on_commit=False
        ) as db:
            trusted = await resolve_trusted_actor(
                db, ticket(actor, tenant, workspace), project
            )
            assert trusted.stable_actor_id == actor and len(actor) == 128
            row = await db.get(ProjectUser, member)
            row.display_name = "changed"
            await db.commit()
            assert "agriculture_resolved_actor" not in db.info
            row.display_name = "untrusted"
            with pytest.raises(IdentityError):
                await db.commit()
            await db.rollback()
        async with runtime.transaction():
            await runtime.execute("SELECT set_config('app.tenant_ids',$1,true)", tenant)
            audit = await runtime.fetchrow(
                "SELECT actor_id,legacy_user_code,legacy_role "
                "FROM agriculture_actor_audit WHERE tenant_id=$1",
                tenant,
            )
            assert tuple(audit) == (actor, "legacy_a", "interpreter")
            for stmt in (
                "UPDATE agriculture_actor_audit SET actor_id='forged'",
                "DELETE FROM agriculture_actor_audit",
                "TRUNCATE agriculture_actor_audit",
            ):
                with pytest.raises(asyncpg.InsufficientPrivilegeError):
                    async with runtime.transaction():
                        await runtime.execute(stmt)
        # Business failure after ledger insertion rolls both records back.
        async with AgricultureAsyncSession(engine, audit_adapters=(adapter,)) as db:
            await resolve_trusted_actor(db, ticket(actor, tenant, workspace), project)
            row = await db.get(ProjectUser, member)
            row.display_name = None
            from sqlalchemy.exc import IntegrityError

            with pytest.raises(IntegrityError):
                await db.commit()
            await db.rollback()
        async with owner.transaction():
            await owner.execute("SELECT set_config('app.tenant_ids',$1,true)", tenant)
            assert (
                await owner.fetchval(
                    "SELECT count(*) FROM agriculture_actor_audit WHERE tenant_id=$1",
                    tenant,
                )
                == 1
            )
            assert (
                await owner.fetchval(
                    "SELECT display_name FROM project_users WHERE id=$1", member
                )
                == "changed"
            )
            # Even the owner cannot silently rewrite historical audit rows.
            with pytest.raises(asyncpg.InsufficientPrivilegeError):
                async with owner.transaction():
                    await owner.execute(
                        "DELETE FROM agriculture_actor_audit WHERE tenant_id=$1", tenant
                    )
        async with owner.transaction():
            await owner.execute(
                "SELECT set_config('app.tenant_ids',$1,true)", tenant + "x"
            )
            with pytest.raises(asyncpg.UniqueViolationError):
                async with owner.transaction():
                    await owner.execute(
                        "INSERT INTO agriculture_project_bindings "
                        "VALUES($1,$2,$3,'active')",
                        tenant + "x",
                        workspace,
                        project,
                    )
    finally:
        await engine.dispose()
        await runtime.close()
        await owner.close()


@pytest.mark.asyncio
async def test_postgres_actor_binding_lock_blocks_concurrent_revocation():
    owner_dsn, runtime_dsn = pg_config()
    owner, tenant, workspace, actor, project, member = await prepare(owner_dsn)
    engine = engine_for(runtime_dsn)
    try:
        async with AgricultureAsyncSession(engine) as db:
            await resolve_trusted_actor(db, ticket(actor, tenant, workspace), project)

            async def revoke():
                async with owner.transaction():
                    await owner.execute(
                        "SELECT set_config('app.tenant_ids',$1,true)", tenant
                    )
                    await owner.execute(
                        "UPDATE agriculture_actor_bindings SET state='revoked' "
                        "WHERE actor_id=$1",
                        actor,
                    )

            pending = asyncio.create_task(revoke())
            await asyncio.sleep(0.1)
            assert not pending.done()
            await db.rollback()
            await asyncio.wait_for(pending, 2)
        async with AgricultureAsyncSession(engine) as db:
            with pytest.raises(IdentityError, match="actor_binding_denied"):
                await resolve_trusted_actor(
                    db, ticket(actor, tenant, workspace), project
                )
        async with owner.transaction():
            await owner.execute("SELECT set_config('app.tenant_ids',$1,true)", tenant)
            await owner.execute(
                "UPDATE agriculture_actor_bindings SET state='active' "
                "WHERE actor_id=$1",
                actor,
            )
        async with AgricultureAsyncSession(engine) as db:
            async with owner.transaction():
                await owner.execute(
                    "SELECT set_config('app.tenant_ids',$1,true)", tenant
                )
                await owner.execute(
                    "SELECT actor_id FROM agriculture_actor_bindings "
                    "WHERE actor_id=$1 FOR UPDATE",
                    actor,
                )
                short = replace(
                    ticket(actor, tenant, workspace),
                    expires_at=datetime.now(UTC) + timedelta(seconds=0.3),
                )
                pending = asyncio.create_task(resolve_trusted_actor(db, short, project))
                await asyncio.sleep(0.1)
                assert not pending.done()
                await asyncio.sleep(0.3)
            with pytest.raises(IdentityError, match="delegation_expired"):
                await asyncio.wait_for(pending, 2)
            assert "agriculture_resolved_actor" not in db.info
    finally:
        await engine.dispose()
        await owner.close()
