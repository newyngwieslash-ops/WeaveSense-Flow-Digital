"""Native role/lease tests and mandatory real Go→Python job authority chain."""

import asyncio
import copy
import hashlib
import json
import os
import subprocess
import tempfile
from dataclasses import asdict
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import asyncpg
import pytest
from job_fixture_support import FixtureLeaseProvider, prepare_jobs
from test_agriculture_actors_postgres import pg_config
from test_agriculture_job_authority import job_fixture

from app.agriculture.database import create_runtime_pool
from app.agriculture.identity.verify import IdentityError, Verified
from app.agriculture.job_authority import JobCheckpoint, snapshot_bytes
from app.agriculture.job_store import PostgresJobAuthorityStore


def test_postgres_job_sidecar_scope_immutable_and_worker_privileges():
    async def run():
        owner_dsn, app_dsn = pg_config()
        worker_dsn = os.environ["AGRI_TEST_GEO_WORKER_DSN"]
        owner = await prepare_jobs(owner_dsn)
        app = await create_runtime_pool(app_dsn, min_size=1, max_size=1)
        worker = await create_runtime_pool(
            worker_dsn, role="flow_geo_worker", min_size=1, max_size=1
        )
        try:
            store = PostgresJobAuthorityStore(app)
            worker_store = PostgresJobAuthorityStore(worker)
            await store.require_ready()
            await worker_store.require_ready()
            snapshot, verified = job_fixture(
                "tenant_" + uuid4().hex, "workspace_" + uuid4().hex
            )
            digest = hashlib.sha256(snapshot_bytes(snapshot)).hexdigest()
            await store.insert(verified, snapshot, digest, verified.expires_at)
            await store.insert(verified, snapshot, digest, verified.expires_at)
            assert await worker_store.get(verified, snapshot["job_id"]) == snapshot
            with pytest.raises(asyncpg.InsufficientPrivilegeError):
                await worker_store.insert(
                    verified, snapshot, digest, verified.expires_at
                )
            other = copy.deepcopy(verified.claims)
            other["tenant_id"] = "tenant_foreign"
            with pytest.raises(IdentityError):
                await worker_store.get(
                    Verified(other, verified.expires_at, verified.body),
                    snapshot["job_id"],
                )
            assert await worker_store.get(verified, snapshot["job_id"]) == snapshot
            async with worker.acquire() as connection:
                assert (
                    await connection.fetchval(
                        "SELECT count(*) FROM public.agriculture_job_authorities"
                    )
                    == 0
                )
                for table in (
                    "agriculture_job_authorities",
                    "agriculture_job_authority_checks",
                ):
                    assert not await connection.fetchval(
                        "SELECT has_table_privilege(current_user,$1,"
                        "'UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')",
                        "public." + table,
                    )
            with pytest.raises(asyncpg.CheckViolationError):
                await owner.execute(
                    "UPDATE public.agriculture_job_authorities SET snapshot_hash=$1 "
                    "WHERE tenant_id=$2",
                    "0" * 64,
                    snapshot["tenant_id"],
                )
            changed = copy.deepcopy(snapshot)
            changed["input_hash"] = "0" * 64
            with pytest.raises(IdentityError) as conflict:
                await store.insert(
                    verified,
                    changed,
                    hashlib.sha256(snapshot_bytes(changed)).hexdigest(),
                    verified.expires_at,
                )
            assert conflict.value.status == 409
            # Genuine PostgreSQL lock wait crosses the short identity deadline.
            # Releasing the lock must roll back rather than commit a stale job.
            delayed = copy.deepcopy(snapshot)
            delayed["job_id"] = "job_delayed"
            until = datetime.now(UTC) + timedelta(milliseconds=150)
            short = Verified(verified.claims, until, verified.body)
            async with owner.transaction():
                await owner.execute(
                    "LOCK TABLE public.agriculture_job_authorities "
                    "IN ACCESS EXCLUSIVE MODE"
                )
                pending = asyncio.create_task(
                    store.insert(
                        short,
                        delayed,
                        hashlib.sha256(snapshot_bytes(delayed)).hexdigest(),
                        short.expires_at,
                    )
                )
                await asyncio.sleep(0.03)
                assert not pending.done(), "write did not encounter held PG lock"
                await asyncio.sleep(
                    max(0, (until - datetime.now(UTC)).total_seconds()) + 0.02
                )
            with pytest.raises(IdentityError) as expired:
                await pending
            assert expired.value.code == "expired_job_identity"
            assert (
                await owner.fetchval(
                    "SELECT count(*) FROM public.agriculture_job_authorities "
                    "WHERE tenant_id=$1 AND job_id='job_delayed'",
                    snapshot["tenant_id"],
                )
                == 0
            )
            # A fresh caller must also not commit after the callback's shorter
            # current-authority deadline (independent of request expiry).
            checked = datetime.now(UTC).replace(microsecond=0)
            current_until = checked + timedelta(seconds=2)
            checkpoint = JobCheckpoint(
                snapshot["job_id"],
                digest,
                "worker_fixture",
                1,
                1,
                "publish",
                snapshot["input_hash"],
            )
            current = {
                "checked_at": checked.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "valid_until": current_until.strftime("%Y-%m-%dT%H:%M:%SZ"),
            }
            async with owner.transaction():
                await owner.execute(
                    "LOCK TABLE public.agriculture_job_authority_checks "
                    "IN ACCESS EXCLUSIVE MODE"
                )
                pending = asyncio.create_task(
                    store.record(verified, snapshot, checkpoint, current)
                )
                await asyncio.sleep(0.03)
                assert not pending.done(), "audit did not encounter held PG lock"
                await asyncio.sleep(
                    max(0, (current_until - datetime.now(UTC)).total_seconds()) + 0.02
                )
            with pytest.raises(IdentityError) as expired:
                await pending
            assert expired.value.code == "job_authority_expired"
            assert (
                await owner.fetchval(
                    "SELECT count(*) FROM public.agriculture_job_authority_checks "
                    "WHERE tenant_id=$1",
                    snapshot["tenant_id"],
                )
                == 0
            )
        finally:
            await worker.close()
            await app.close()
            await owner.close()

    asyncio.run(run())


def test_postgres_job_execution_lease_fencing_and_retry_scope():
    async def run():
        owner_dsn, app_dsn = pg_config()
        owner = await prepare_jobs(owner_dsn)
        app = await create_runtime_pool(app_dsn)
        try:
            snapshot, _ = job_fixture(
                "tenant_" + uuid4().hex, "workspace_" + uuid4().hex
            )
            digest = hashlib.sha256(snapshot_bytes(snapshot)).hexdigest()
            checkpoint = JobCheckpoint(
                snapshot["job_id"],
                digest,
                "worker_one",
                1,
                1,
                "claim",
                snapshot["input_hash"],
            )
            await owner.execute(
                "INSERT INTO public.fixture_job_leases "
                "VALUES($1,$2,$3,$4,1,1,'running')",
                snapshot["tenant_id"],
                snapshot["workspace_id"],
                snapshot["job_id"],
                checkpoint.worker_id,
            )
            provider = FixtureLeaseProvider(app)
            await provider.check(snapshot, checkpoint)
            await owner.execute(
                "UPDATE public.fixture_job_leases "
                "SET attempt=2,fencing=2,worker_id='worker_two' WHERE tenant_id=$1",
                snapshot["tenant_id"],
            )
            with pytest.raises(IdentityError):
                await provider.check(snapshot, checkpoint)
            next_checkpoint = JobCheckpoint(
                **{
                    **asdict(checkpoint),
                    "worker_id": "worker_two",
                    "attempt": 2,
                    "fencing": 2,
                    "phase": "publish",
                }
            )
            await provider.check(snapshot, next_checkpoint)
            with pytest.raises(IdentityError):
                await provider.check(
                    {**snapshot, "tenant_id": "tenant_other"}, next_checkpoint
                )
            await owner.execute(
                "UPDATE public.fixture_job_leases SET state='cancelled' "
                "WHERE tenant_id=$1",
                snapshot["tenant_id"],
            )
            with pytest.raises(IdentityError):
                await provider.check(snapshot, next_checkpoint)
        finally:
            await app.close()
            await owner.close()

    asyncio.run(run())


def configured_go_mod_cache(root, value):
    path = Path(value) if value is not None else root / ".local/go-mod-cache"
    if not path.is_absolute() or not path.is_dir() or path == Path("/"):
        raise ValueError("explicit existing absolute Go module cache required")
    return str(path.resolve(strict=True))


def test_job_cache_configuration_accepts_only_explicit_existing_directory(tmp_path):
    default = tmp_path / ".local/go-mod-cache"
    default.mkdir(parents=True)
    assert configured_go_mod_cache(tmp_path, None) == str(default)
    actual_ci = tmp_path / "runner/go/pkg/mod"
    actual_ci.mkdir(parents=True)
    assert configured_go_mod_cache(tmp_path, str(actual_ci)) == str(actual_ci)
    file = tmp_path / "not_directory"
    file.write_text("not a cache")
    for invalid in ("", "relative/cache", "/", str(file), str(tmp_path / "absent")):
        with pytest.raises(ValueError):
            configured_go_mod_cache(tmp_path, invalid)


def test_real_go_python_job_authority_chain():
    pg_config()
    go = os.environ.get("AGRI_TEST_GO_BIN")
    python = os.environ.get("AGRI_IDENTITY_PYTHON")
    if not go or not python:
        pytest.fail("required explicit Go/Python executables missing")
    root = Path(__file__).resolve().parents[4]
    module_cache = configured_go_mod_cache(root, os.getenv("AGRI_TEST_GO_MOD_CACHE"))
    with tempfile.TemporaryDirectory(prefix="job-chain-") as private:
        output = Path(private) / "go.json"
        with output.open("wb") as log:
            result = subprocess.run(
                [
                    go,
                    "test",
                    "-json",
                    "./internal/agriculture",
                    "-run",
                    "^TestJobAuthorityRealHTTPPostgres$",
                    "-count=1",
                ],
                cwd=root / "backend",
                env={
                    **{
                        key: os.environ[key]
                        for key in (
                            "PATH",
                            "HOME",
                            "TMPDIR",
                            "LANG",
                            "LC_ALL",
                            "LD_LIBRARY_PATH",
                            "AGRI_TEST_PG_ISOLATED",
                            "AGRI_TEST_GEO_MIGRATION_DSN",
                            "AGRI_TEST_GEO_RUNTIME_DSN",
                            "AGRI_TEST_GEO_WORKER_DSN",
                            "AGRI_IDENTITY_PYTHON",
                        )
                        if key in os.environ
                    },
                    "GOMODCACHE": module_cache,
                    "GOCACHE": str(root / ".local/go-cache"),
                    "GOTOOLCHAIN": "local",
                    "GOPROXY": "off",
                },
                stdout=log,
                stderr=log,
                timeout=120,
            )
        states = {}
        for line in output.read_text(errors="replace").splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if event.get("Test") and event.get("Action") in {"pass", "fail", "skip"}:
                states[event["Test"]] = event["Action"]
        required = {"TestJobAuthorityRealHTTPPostgres"}
        required |= {
            "TestJobAuthorityRealHTTPPostgres/" + name
            for name in (
                "creation_and_checkpoint",
                "forged_scope_and_expired_identity",
                "retry_fencing_and_withdrawal",
                "current_platform_revocation",
            )
        }
        assert result.returncode == 0 and all(
            states.get(name) == "pass" for name in required
        ), "real job-chain required roots missing/failed/skipped: " + json.dumps(
            states, sort_keys=True
        )
