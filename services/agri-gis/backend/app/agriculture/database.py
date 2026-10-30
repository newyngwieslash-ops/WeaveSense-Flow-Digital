"""Fail-closed GIS runtime connections; never use the legacy unscoped factory.

Scope is constructed only from the verified service-dependency output. This is
not an authentication API and must not be populated from browser parameters.
Database RLS and domain ownership checks remain independent requirements.
"""

from contextlib import asynccontextmanager
from dataclasses import dataclass
from datetime import UTC, datetime

import asyncpg
from sqlalchemy import event, text
from sqlalchemy.ext.asyncio import create_async_engine

from .actor_session import AgricultureAsyncSession, AgricultureSession
from .domain_boundary import require_domain_boundary
from .identity.verify import ID, IdentityError, Verified

RUNTIME_ROLES = frozenset({"flow_geo_app", "flow_geo_worker"})
ROLE_CHECK = """
SELECT current_database() = 'flow_geo'
 AND current_user = $1 AND session_user = $1
 AND coalesce(current_setting('app.tenant_ids',true),'') = ''
 AND coalesce(current_setting('app.workspace_id',true),'') = ''
 AND r.rolcanlogin
 AND NOT (r.rolsuper OR r.rolbypassrls OR r.rolinherit OR r.rolcreatedb
          OR r.rolcreaterole OR r.rolreplication)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_auth_members m WHERE m.member=r.oid)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_database d WHERE d.datdba=r.oid)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace n WHERE n.nspowner=r.oid)
 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_class c WHERE c.relowner=r.oid)
FROM pg_catalog.pg_roles r WHERE r.rolname=current_user
"""


def unavailable():
    return IdentityError("gis_runtime_database_unavailable", 503)


async def validate_runtime_connection(connection, role="flow_geo_app"):
    if role not in RUNTIME_ROLES:
        raise unavailable()
    try:
        if await connection.fetchval(ROLE_CHECK, role, timeout=3) is not True:
            raise unavailable()
    except Exception:
        raise unavailable() from None


async def create_runtime_pool(dsn, *, role="flow_geo_app", min_size=1, max_size=5):
    """Retain asyncpg TLS/DSN parsing and validate every new physical connection."""
    if not isinstance(dsn, str) or not dsn or role not in RUNTIME_ROLES:
        raise unavailable()
    pool = None
    try:

        async def initialize(connection):
            await validate_runtime_connection(connection, role)

        pool = await asyncpg.create_pool(
            dsn, min_size=min_size, max_size=max_size, timeout=3, init=initialize
        )
        async with pool.acquire(timeout=3) as connection:
            await validate_runtime_connection(connection, role)
        return pool
    except Exception:
        if pool is not None:
            await pool.close()
        raise unavailable() from None


@dataclass(frozen=True)
class DatabaseScope:
    tenant_id: str
    workspace_id: str
    expires_at: datetime

    @classmethod
    def from_verified(cls, verified):
        if not isinstance(verified, Verified):
            raise IdentityError("trusted_database_scope_required", 403)
        c = verified.claims
        # Service-only assertions cannot be turned into a business ORM session.
        if (
            not isinstance(c, dict)
            or not isinstance(c.get("actor"), dict)
            or c["actor"].get("kind") != "user"
        ):
            raise IdentityError("trusted_database_scope_required", 403)
        scope = cls(c.get("tenant_id"), c.get("workspace_id"), verified.expires_at)
        scope.validate()
        return scope

    def validate(self):
        if not all(
            isinstance(v, str) and ID.fullmatch(v)
            for v in (self.tenant_id, self.workspace_id)
        ):
            raise IdentityError("trusted_database_scope_required", 403)
        if not isinstance(self.expires_at, datetime) or self.expires_at.tzinfo is None:
            raise IdentityError("trusted_database_scope_required", 403)
        if self.expires_at <= datetime.now(UTC):
            raise IdentityError("delegation_expired", 401)


class ScopedAgricultureSession(AgricultureSession):
    def __init__(self, *args, database_scope=None, **kwargs):
        if not isinstance(database_scope, DatabaseScope):
            raise IdentityError("trusted_database_scope_required", 403)
        database_scope.validate()
        self._database_scope = database_scope
        super().__init__(*args, **kwargs)


class ScopedAgricultureAsyncSession(AgricultureAsyncSession):
    sync_session_class = ScopedAgricultureSession


@event.listens_for(ScopedAgricultureSession, "after_begin")
def apply_transaction_scope(session, transaction, connection):
    scope = session._database_scope
    scope.validate()
    # Reapply after internal legacy commit; do not use session-level SET. This
    # direct connection call does not expand the ORM's raw-SQL allowlist.
    connection.execute(
        text(
            "SELECT set_config('app.tenant_ids', :tenant, true), "
            "set_config('app.workspace_id', :workspace, true)"
        ),
        {"tenant": scope.tenant_id, "workspace": scope.workspace_id},
    )


class GISRuntimeDatabase:
    def __init__(self, engine):
        self._engine = engine

    @classmethod
    async def open(cls, dsn, *, role="flow_geo_app", pool_size=5):
        if not isinstance(dsn, str) or not dsn or role not in RUNTIME_ROLES:
            raise unavailable()

        async def connect():
            connection = await asyncpg.connect(dsn, timeout=3)
            try:
                await validate_runtime_connection(connection, role)
                await require_domain_boundary(connection, role=role)
                return connection
            except Exception:
                await connection.close()
                raise unavailable() from None

        engine = create_async_engine(
            "postgresql+asyncpg://",
            async_creator=connect,
            pool_size=pool_size,
            max_overflow=0,
            pool_pre_ping=True,
            pool_reset_on_return="rollback",
            hide_parameters=True,
        )
        try:
            async with engine.connect():
                pass
            return cls(engine)
        except Exception:
            await engine.dispose()
            raise unavailable() from None

    @asynccontextmanager
    async def session(self, verified, *, audit_adapters=(), operation_adapters=()):
        scope = DatabaseScope.from_verified(verified)
        async with ScopedAgricultureAsyncSession(
            bind=self._engine,
            database_scope=scope,
            audit_adapters=audit_adapters,
            expire_on_commit=False,
            autoflush=False,
        ) as session:
            session.info["agriculture_operation_adapters"] = tuple(operation_adapters)
            try:
                yield session
            finally:
                await session.rollback()

    async def close(self):
        await self._engine.dispose()
