"""Runtime factory boundaries; simulated connections are unit evidence only."""

from contextlib import asynccontextmanager
from dataclasses import FrozenInstanceError
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace

import pytest
from sqlalchemy import text

from app.agriculture import database as database_module
from app.agriculture.actor_session import prohibit_bulk
from app.agriculture.database import (
    ROLE_CHECK,
    DatabaseScope,
    GISRuntimeDatabase,
    ScopedAgricultureSession,
    apply_transaction_scope,
    create_runtime_pool,
    validate_runtime_connection,
)
from app.agriculture.identity.verify import IdentityError, Verified


def verified(tenant="tenant_a", workspace="workspace_a"):
    return Verified(
        {
            "tenant_id": tenant,
            "workspace_id": workspace,
            "actor": {"kind": "user", "id": "1fixture"},
        },
        datetime.now(UTC) + timedelta(seconds=25),
        {},
    )


@pytest.mark.parametrize("value", [None, {}, "tenant_a"])
def test_no_unverified_or_absent_scope(value):
    with pytest.raises(IdentityError):
        DatabaseScope.from_verified(value)
    with pytest.raises(IdentityError):
        ScopedAgricultureSession(database_scope=value)


@pytest.mark.parametrize("bad", [None, "", "a,b", "*", "a b", "x" * 129])
def test_single_tenant_and_workspace_only(bad):
    for ticket in (verified(bad), verified(workspace=bad)):
        with pytest.raises(IdentityError):
            DatabaseScope.from_verified(ticket)


def test_service_assertion_not_business_session_and_no_mutable_alias():
    ticket = verified()
    scope = DatabaseScope.from_verified(ticket)
    ticket.claims["tenant_id"] = "tenant_other"
    assert scope.tenant_id == "tenant_a"
    with pytest.raises(FrozenInstanceError):
        scope.tenant_id = "tenant_other"
    del ticket.claims["actor"]
    with pytest.raises(IdentityError):
        DatabaseScope.from_verified(ticket)


def test_expired_and_naive_scope_rejected():
    for expiry in (datetime.now(UTC) - timedelta(seconds=1), datetime.now()):
        with pytest.raises(IdentityError):
            DatabaseScope("tenant_a", "workspace_a", expiry).validate()


def test_fixed_session_scope_cannot_be_rebound_by_later_verified_actor_call():
    session = ScopedAgricultureSession(
        database_scope=DatabaseScope.from_verified(verified())
    )
    statement = text(
        "SELECT set_config('app.tenant_ids', :tenant, true), "
        "set_config('app.workspace_id', :workspace, true)"
    )
    state = SimpleNamespace(
        session=session,
        statement=statement,
        parameters={"tenant": "tenant_a", "workspace": "workspace_a"},
    )
    prohibit_bulk(state)
    for parameters in (
        {"tenant": "tenant_other", "workspace": "workspace_a"},
        {"tenant": "tenant_a", "workspace": "workspace_b"},
    ):
        state.parameters = parameters
        with pytest.raises(IdentityError):
            prohibit_bulk(state)
    session.close()


def test_each_transaction_uses_parameterized_local_scope_without_actor_rebinding():
    class Connection:
        def __init__(self):
            self.calls = []

        def execute(self, statement, params):
            self.calls.append((str(statement), params))

    session = ScopedAgricultureSession(
        database_scope=DatabaseScope.from_verified(verified())
    )
    conn = Connection()
    apply_transaction_scope(session, None, conn)
    apply_transaction_scope(session, None, conn)
    assert len(conn.calls) == 2
    assert conn.calls[0] == conn.calls[1]
    assert conn.calls[0][1] == {"tenant": "tenant_a", "workspace": "workspace_a"}
    assert "set_config('app.workspace_id', :workspace, true)" in conn.calls[0][0]
    assert "agriculture_resolved_actor" not in session.info
    session.close()


@pytest.mark.asyncio
async def test_role_guard_requires_exact_true_and_sanitizes_driver_error():
    class Connection:
        def __init__(self, result):
            self.result = result

        async def fetchval(self, sql, role, **kwargs):
            assert sql == ROLE_CHECK and role == "flow_geo_app"
            if isinstance(self.result, Exception):
                raise self.result
            return self.result

    await validate_runtime_connection(Connection(True))
    for value in (False, None, 1, RuntimeError("secret_dsn_sentinel")):
        with pytest.raises(IdentityError) as error:
            await validate_runtime_connection(Connection(value))
        assert "secret_dsn_sentinel" not in str(error.value)
    for role in ("flow_geo_migrate", "postgres", "flow_geo_system", ""):
        with pytest.raises(IdentityError):
            await validate_runtime_connection(Connection(True), role)


@pytest.mark.asyncio
async def test_no_dsn_or_migration_role_fallback(monkeypatch):
    monkeypatch.setenv("DATABASE_URL", "private_migration_sentinel")
    for dsn, role in (
        (None, "flow_geo_app"),
        ("", "flow_geo_app"),
        ("unused", "flow_geo_migrate"),
    ):
        with pytest.raises(IdentityError):
            await GISRuntimeDatabase.open(dsn, role=role)
        with pytest.raises(IdentityError):
            await create_runtime_pool(dsn, role=role)


@pytest.mark.asyncio
@pytest.mark.parametrize("role", ["flow_geo_app", "flow_geo_worker"])
async def test_domain_factory_requires_role_specific_boundary_before_use(
    monkeypatch, role
):
    called = []

    class Connection:
        async def fetchval(self, *args, **kwargs):
            return True

        async def close(self):
            called.append("connection_closed")

    async def connect(*args, **kwargs):
        return Connection()

    async def unavailable_boundary(connection, *, role):
        called.append(role)
        raise IdentityError("gis_domain_boundary_unavailable", 503)

    def engine_factory(*args, **kwargs):
        class Engine:
            @asynccontextmanager
            async def connect(self):
                yield await kwargs["async_creator"]()

            async def dispose(self):
                called.append("engine_disposed")

        return Engine()

    monkeypatch.setattr(database_module.asyncpg, "connect", connect)
    monkeypatch.setattr(
        database_module, "require_domain_boundary", unavailable_boundary
    )
    monkeypatch.setattr(database_module, "create_async_engine", engine_factory)
    with pytest.raises(IdentityError):
        await GISRuntimeDatabase.open("unused_test_dsn", role=role)
    assert called == [role, "connection_closed", "engine_disposed"]
