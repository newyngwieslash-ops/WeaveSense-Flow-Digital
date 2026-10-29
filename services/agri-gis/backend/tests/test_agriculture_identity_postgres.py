"""Real isolated flow_geo replay persistence; no SQLite substitute."""

import asyncio
import os
import uuid
from datetime import UTC, datetime, timedelta
from pathlib import Path

import asyncpg
import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from test_agriculture_identity import build_fixture, send, sign

from app.agriculture.identity.replay import PostgresReplayLedger
from app.agriculture.identity.verify import IdentityError


@pytest.mark.asyncio
async def test_postgres_replay_parallel_persistence_scope_and_fail_closed():
    owner_dsn = os.environ.get("AGRI_TEST_GEO_MIGRATION_DSN")
    runtime_dsn = os.environ.get("AGRI_TEST_GEO_RUNTIME_DSN")
    if (
        os.environ.get("AGRI_TEST_PG_ISOLATED") != "1"
        or not owner_dsn
        or not runtime_dsn
    ):
        pytest.skip("not_run: isolated flow_geo migration/runtime DSNs required")
    owner = await asyncpg.connect(owner_dsn)
    pool = await asyncpg.create_pool(runtime_dsn, min_size=1, max_size=3)
    suffix = uuid.uuid4().hex
    args = {
        "jti": "nonce_" + suffix,
        "tenant_id": "tenant_" + suffix,
        "workspace_id": "workspace_" + suffix,
        "expires_at": datetime.now(UTC) + timedelta(seconds=30),
    }
    try:
        migration = (
            Path(__file__).parents[1]
            / "scripts/migrations/20261029_agriculture_identity_replay.sql"
        )
        await owner.execute(migration.read_text())
        await owner.execute(migration.read_text())
        async with pool.acquire() as connection:
            assert not await connection.fetchval(
                "SELECT rolsuper OR rolbypassrls FROM pg_roles "
                "WHERE rolname=current_user"
            )
            assert not await connection.fetchval(
                "SELECT pg_has_role(current_user,relowner,'MEMBER') FROM pg_class "
                "WHERE oid='public.agriculture_identity_replay'::regclass"
            )
            with pytest.raises(asyncpg.InsufficientPrivilegeError):
                await connection.execute(
                    "INSERT INTO public.agriculture_identity_replay"
                    "(jti,tenant_id,workspace_id,expires_at) VALUES($1,$2,$3,$4)",
                    *args.values(),
                )
        ledger = PostgresReplayLedger(pool)
        await ledger.require_safe_runtime()
        results = await asyncio.gather(
            ledger.claim(**args), ledger.claim(**args), return_exceptions=True
        )
        assert sum(result is None for result in results) == 1
        errors = [result for result in results if result is not None]
        assert (
            len(errors) == 1
            and isinstance(errors[0], IdentityError)
            and errors[0].status == 409
        )
        # The claim transaction committed independently of any business handler;
        # closing all original runtime connections cannot release that claim.
        await pool.close()
        pool = await asyncpg.create_pool(runtime_dsn, min_size=1, max_size=2)
        ledger = PostgresReplayLedger(pool)
        with pytest.raises(IdentityError) as error:
            await ledger.claim(**args)
        assert error.value.status == 409
        # A real table-lock wait must not turn an expired token into a claim.
        delayed = {
            **args,
            "jti": "wait_" + suffix,
            "expires_at": datetime.now(UTC) + timedelta(seconds=0.4),
        }
        async with owner.transaction():
            await owner.execute(
                "LOCK TABLE public.agriculture_identity_replay IN SHARE MODE"
            )
            waiting = asyncio.create_task(ledger.claim(**delayed))
            await asyncio.sleep(0.1)
            assert not waiting.done()
            await asyncio.sleep(0.5)
        with pytest.raises(IdentityError) as error:
            await waiting
        assert error.value.status == 401
        async with pool.acquire() as connection:
            assert (
                await connection.fetchval(
                    "SELECT count(*) FROM public.agriculture_identity_replay "
                    "WHERE jti=$1",
                    args["jti"],
                )
                == 0
            )
            async with connection.transaction():
                await connection.execute(
                    "SELECT set_config('app.tenant_ids',$1,true)", args["tenant_id"]
                )
                assert (
                    await connection.fetchval(
                        "SELECT count(*) FROM public.agriculture_identity_replay "
                        "WHERE jti=$1",
                        args["jti"],
                    )
                    == 1
                )
            for operation in (
                "DELETE FROM public.agriculture_identity_replay",
                "UPDATE public.agriculture_identity_replay SET workspace_id='changed'",
                "TRUNCATE public.agriculture_identity_replay",
            ):
                with pytest.raises(asyncpg.InsufficientPrivilegeError):
                    await connection.execute(operation)
        await pool.close()
        with pytest.raises(IdentityError) as error:
            await ledger.claim(**{**args, "jti": "new_" + suffix})
        assert error.value.status == 503
    finally:
        await pool.close()
        async with owner.transaction():
            await owner.execute(
                "SELECT set_config('app.tenant_ids',$1,true)", args["tenant_id"]
            )
            await owner.execute(
                "DELETE FROM public.agriculture_identity_replay "
                "WHERE jti=ANY($1::text[])",
                [args["jti"], "wait_" + suffix],
            )
        await owner.close()


@pytest.mark.asyncio
async def test_postgres_actual_verifier_dependency_nonce_order_and_parallel_http():
    owner_dsn = os.environ.get("AGRI_TEST_GEO_MIGRATION_DSN")
    runtime_dsn = os.environ.get("AGRI_TEST_GEO_RUNTIME_DSN")
    if (
        os.environ.get("AGRI_TEST_PG_ISOLATED") != "1"
        or not owner_dsn
        or not runtime_dsn
    ):
        pytest.skip("not_run: isolated flow_geo migration/runtime DSNs required")
    owner = await asyncpg.connect(owner_dsn)
    pool = await asyncpg.create_pool(runtime_dsn, min_size=1, max_size=4)
    nonces = []
    fixture = build_fixture()
    fixture[1].replay = PostgresReplayLedger(pool)
    try:
        await owner.execute(
            (
                Path(__file__).parents[1]
                / "scripts/migrations/20261029_agriculture_identity_replay.sql"
            ).read_text()
        )
        await fixture[1].replay.require_safe_runtime()

        def new_nonce():
            nonce = "http_nonce_" + uuid.uuid4().hex
            fixture[3]["jti"] = nonce
            nonces.append(nonce)

        new_nonce()
        responses = await asyncio.gather(send(fixture), send(fixture))
        assert sorted(response.status_code for response in responses) == [200, 409]
        new_nonce()
        bad = sign(fixture[3], Ed25519PrivateKey.generate())
        assert (await send(fixture, bad)).status_code == 401
        assert (await send(fixture)).status_code == 200
        new_nonce()
        fixture[1].authority.denied = True
        assert (await send(fixture)).status_code == 403
        fixture[1].authority.denied = False
        assert (await send(fixture)).status_code == 200
        new_nonce()
        fixture[5]["crash"] = True
        assert (await send(fixture)).status_code == 500
        fixture[5]["crash"] = False
        assert (await send(fixture)).status_code == 409
    finally:
        await pool.close()
        async with owner.transaction():
            await owner.execute("SELECT set_config('app.tenant_ids','tenant_a',true)")
            await owner.execute(
                "DELETE FROM public.agriculture_identity_replay "
                "WHERE jti=ANY($1::text[])",
                nonces,
            )
        await owner.close()
