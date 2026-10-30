"""Owned real-PG test process. Not imported by production; no test HTTP controls."""

import asyncio
import base64
import json
import socket
import sys

import httpx
import uvicorn
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from job_fixture_support import FixtureJobOwnership, FixtureLeaseProvider, prepare_jobs

from app.agriculture.database import create_runtime_pool
from app.agriculture.identity.callback import CallbackKey, PlatformAuthority
from app.agriculture.identity.dependency import IdentityServices
from app.agriculture.identity.keys import KeyRing, utc_second
from app.agriculture.identity.replay import PostgresReplayLedger
from app.agriculture.identity.verify import Verifier
from app.agriculture.job_authority import JobAuthority
from app.agriculture.job_callback import PlatformJobAuthority
from app.agriculture.job_store import PostgresJobAuthorityStore
from app.agriculture.main import create_app


async def main():
    config = json.loads(open(sys.argv[1], encoding="utf-8").read())
    owner = await prepare_jobs(config["owner_dsn"])
    pool = await create_runtime_pool(config["app_dsn"])
    worker_pool = await create_runtime_pool(
        config["worker_dsn"], role="flow_geo_worker"
    )
    tenant, workspace = config["tenant"], config["workspace"]
    for kind, resource in (("farm", "farm_a"), ("asset", "asset_a")):
        await owner.execute(
            "INSERT INTO public.fixture_job_resources "
            "VALUES($1,$2,$3,$4,'farm_a',1,'active') ON CONFLICT DO NOTHING",
            tenant,
            workspace,
            kind,
            resource,
        )
    key = config["callback_key"]
    callback_key = CallbackKey(
        key["kid"],
        Ed25519PrivateKey.from_private_bytes(base64.b64decode(key["seed"])),
        utc_second(key["not_before"]),
        utc_second(key["not_after"]),
    )
    async with httpx.AsyncClient(trust_env=False) as client:
        store = PostgresJobAuthorityStore(pool)
        await store.require_ready()
        await PostgresJobAuthorityStore(worker_pool).require_ready()
        replay = PostgresReplayLedger(pool)
        await replay.require_safe_runtime()
        app = create_app(
            IdentityServices(
                Verifier(KeyRing.from_config(config["platform_keys"])),
                ownership=FixtureJobOwnership(pool),
                authority=PlatformAuthority(config["platform"], callback_key, client),
                replay=replay,
            )
        )
        app.state.job_authority = JobAuthority(
            store,
            PlatformJobAuthority(config["platform"], callback_key, client),
            FixtureLeaseProvider(worker_pool),
        )
        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        sock.listen(128)
        server = uvicorn.Server(
            uvicorn.Config(
                app, log_config=None, log_level="critical", access_log=False, http="h11"
            )
        )
        serving = asyncio.create_task(server.serve(sockets=[sock]))
        print(json.dumps({"port": sock.getsockname()[1]}), flush=True)
        try:
            while True:
                line = await asyncio.to_thread(sys.stdin.readline)
                if not line:
                    break
                command = json.loads(line)
                if command["command"] == "lease":
                    await owner.execute(
                        "INSERT INTO public.fixture_job_leases "
                        "VALUES($1,$2,$3,'worker_fixture',1,1,'running')",
                        tenant,
                        workspace,
                        command["job_id"],
                    )
                elif command["command"] == "retry":
                    await owner.execute(
                        "UPDATE public.fixture_job_leases SET attempt=2,fencing=2 "
                        "WHERE tenant_id=$1 AND workspace_id=$2",
                        tenant,
                        workspace,
                    )
                elif command["command"] in {"withdraw", "restore"}:
                    await owner.execute(
                        "UPDATE public.fixture_job_resources SET state=$3 "
                        "WHERE tenant_id=$1 AND workspace_id=$2 AND kind='asset'",
                        tenant,
                        workspace,
                        "revoked" if command["command"] == "withdraw" else "active",
                    )
                elif command["command"] == "stats":
                    count = await owner.fetchval(
                        "SELECT count(*) FROM public.agriculture_job_authority_checks "
                        "WHERE tenant_id=$1 AND workspace_id=$2",
                        tenant,
                        workspace,
                    )
                    print(json.dumps({"checks": count}), flush=True)
                    continue
                else:
                    raise ValueError("unknown fixture command")
                print(json.dumps({"ok": True}), flush=True)
        finally:
            server.should_exit = True
            await serving
            await worker_pool.close()
            await pool.close()
            await owner.close()


if __name__ == "__main__":
    asyncio.run(main())
