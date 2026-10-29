"""GIS-owned durable replay claim. Nonces are not business idempotency keys."""

from datetime import datetime
from typing import Protocol

import asyncpg

from .verify import IdentityError


class ReplayLedger(Protocol):
    async def claim(
        self, *, jti: str, tenant_id: str, workspace_id: str, expires_at: datetime
    ) -> None: ...


class PostgresReplayLedger:
    def __init__(self, pool: asyncpg.Pool):
        self.pool = pool

    async def require_safe_runtime(self) -> None:
        try:
            async with self.pool.acquire(timeout=2) as connection:
                safe = await connection.fetchval(
                    """SELECT NOT (r.rolsuper OR r.rolbypassrls OR r.rolinherit
                    OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication)
                    AND r.rolcanlogin AND c.relrowsecurity AND c.relforcerowsecurity
                    AND NOT pg_catalog.pg_has_role(current_user,c.relowner,'MEMBER')
                    AND NOT has_table_privilege(
                        current_user,c.oid,'UPDATE,DELETE,TRUNCATE')
                    FROM pg_catalog.pg_roles r CROSS JOIN pg_catalog.pg_class c
                    WHERE r.rolname=current_user
                    AND c.oid='public.agriculture_identity_replay'::regclass""",
                    timeout=2,
                )
                if safe is not True:
                    raise IdentityError("identity_dependency_unavailable", 503)
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("identity_dependency_unavailable", 503) from None

    async def claim(
        self, *, jti: str, tenant_id: str, workspace_id: str, expires_at: datetime
    ) -> None:
        try:
            async with self.pool.acquire(timeout=2) as connection:
                async with connection.transaction():
                    await connection.execute(
                        "SELECT set_config('app.tenant_ids', $1, true)",
                        tenant_id,
                        timeout=2,
                    )
                    claimed = await connection.fetchval(
                        "INSERT INTO public.agriculture_identity_replay"
                        "(jti,tenant_id,workspace_id,expires_at) "
                        "SELECT $1,$2,$3,$4 WHERE clock_timestamp() < $4 "
                        "ON CONFLICT (jti) DO NOTHING RETURNING jti",
                        jti,
                        tenant_id,
                        workspace_id,
                        expires_at,
                        timeout=2,
                    )
                    unexpired = await connection.fetchval(
                        "SELECT clock_timestamp() < $1::timestamptz",
                        expires_at,
                        timeout=2,
                    )
            # Raise only after commit: an already-inserted nonce cannot be
            # released just because a subsequent lock/commit wait expired it.
            if not unexpired:
                raise IdentityError("delegation_expired", 401)
            if claimed is None:
                raise IdentityError("delegation_replayed", 409)
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("identity_dependency_unavailable", 503) from None
