"""Narrow asyncpg sidecar transactions, never held across an HTTP callback."""

import hashlib
import json
from datetime import UTC, datetime

from .identity.keys import utc_second
from .identity.verify import Verified, require
from .job_authority import match_snapshot, snapshot_bytes


class PostgresJobAuthorityStore:
    def __init__(self, pool):
        self.pool = pool

    async def require_ready(self):
        async with self.pool.acquire(timeout=3) as connection:
            rows = await connection.fetch("""
SELECT c.relname, c.relrowsecurity AND c.relforcerowsecurity
 AND pg_get_userbyid(c.relowner)='flow_geo_migrate'
 AND has_table_privilege(current_user,c.oid,'SELECT')
 AND NOT has_table_privilege(current_user,c.oid,
  'UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 AND (has_table_privilege(current_user,c.oid,'INSERT') =
   (current_user='flow_geo_app' OR c.relname='agriculture_job_authority_checks'))
 AND EXISTS(SELECT 1 FROM pg_catalog.pg_policy p WHERE p.polrelid=c.oid
  AND p.polname='agriculture_job_scope' AND NOT p.polpermissive)
 AND EXISTS(SELECT 1 FROM pg_catalog.pg_trigger t WHERE t.tgrelid=c.oid
  AND t.tgname='agriculture_job_authority_immutable' AND t.tgenabled='O') AS ready
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relname IN
 ('agriculture_job_authorities','agriculture_job_authority_checks')
 AND current_user IN ('flow_geo_app','flow_geo_worker')
""")
            require(
                len(rows) == 2 and all(row["ready"] is True for row in rows),
                "job_store_unavailable",
                503,
            )

    async def _scope(self, connection, verified):
        require(
            isinstance(verified, Verified) and verified.expires_at > datetime.now(UTC),
            "expired_job_identity",
            401,
        )
        await connection.execute(
            "SELECT set_config('app.tenant_ids',$1,true),"
            "set_config('app.workspace_id',$2,true)",
            verified.claims["tenant_id"],
            verified.claims["workspace_id"],
        )

    async def insert(self, verified, snapshot, digest):
        match_snapshot(verified, snapshot)
        raw = snapshot_bytes(snapshot)
        require(hashlib.sha256(raw).hexdigest() == digest, "job_scope_mismatch", 403)
        async with self.pool.acquire(timeout=3) as connection:
            async with connection.transaction():
                await self._scope(connection, verified)
                await connection.execute(
                    """INSERT INTO public.agriculture_job_authorities
 (tenant_id,workspace_id,job_id,snapshot_hash,snapshot_json) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(tenant_id,workspace_id,job_id) DO NOTHING""",
                    snapshot["tenant_id"],
                    snapshot["workspace_id"],
                    snapshot["job_id"],
                    digest,
                    raw.decode(),
                )
                existing = await connection.fetchval(
                    "SELECT snapshot_hash FROM public.agriculture_job_authorities "
                    "WHERE tenant_id=$1 AND workspace_id=$2 AND job_id=$3",
                    snapshot["tenant_id"],
                    snapshot["workspace_id"],
                    snapshot["job_id"],
                )
                require(existing == digest, "job_snapshot_conflict", 409)

    async def get(self, verified, job_id):
        async with self.pool.acquire(timeout=3) as connection:
            async with connection.transaction():
                await self._scope(connection, verified)
                row = await connection.fetchrow(
                    "SELECT snapshot_json,snapshot_hash "
                    "FROM public.agriculture_job_authorities "
                    "WHERE tenant_id=$1 AND workspace_id=$2 AND job_id=$3",
                    verified.claims["tenant_id"],
                    verified.claims["workspace_id"],
                    job_id,
                )
        require(row is not None, "job_not_found", 404)
        value = json.loads(row["snapshot_json"])
        require(
            hashlib.sha256(snapshot_bytes(value)).hexdigest() == row["snapshot_hash"],
            "job_store_corrupt",
            503,
        )
        return value

    async def record(self, verified, snapshot, checkpoint, current):
        checked, expires = (
            utc_second(current["checked_at"]),
            utc_second(current["valid_until"]),
        )
        require(checked <= datetime.now(UTC) < expires, "job_authority_expired", 403)
        async with self.pool.acquire(timeout=3) as connection:
            async with connection.transaction():
                await self._scope(connection, verified)
                await connection.execute(
                    """INSERT INTO public.agriculture_job_authority_checks
 (tenant_id,workspace_id,job_id,snapshot_hash,check_id,worker_id,attempt,fencing,phase,checked_at,valid_until)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)""",
                    snapshot["tenant_id"],
                    snapshot["workspace_id"],
                    checkpoint.job_id,
                    checkpoint.snapshot_hash,
                    verified.claims["jti"],
                    checkpoint.worker_id,
                    checkpoint.attempt,
                    checkpoint.fencing,
                    checkpoint.phase,
                    checked,
                    expires,
                )
