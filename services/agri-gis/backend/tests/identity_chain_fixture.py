"""Test-process-only real GIS identity/write chain; never a production router.

The private configuration supplies fixture identities, not request claims. GIS
facts live in an independently seeded PostgreSQL catalog. No credential, DSN or
delegation is printed; stdout contains one readiness record only.
"""

import asyncio
import base64
import ipaddress
import json
import os
import socket
import stat
import sys
import traceback
from contextlib import AsyncExitStack, asynccontextmanager
from pathlib import Path
from urllib.parse import parse_qsl, urlsplit

import asyncpg
import httpx
import uvicorn
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from domain_fixture_support import bind_fixture_plot, install_domain_schema
from fastapi import Depends
from test_agriculture_legacy_write_integration import legacy_fixture_context

from app.agriculture.actors import resolve_trusted_actor
from app.agriculture.database import GISRuntimeDatabase, create_runtime_pool
from app.agriculture.identity.callback import CallbackKey, PlatformAuthority
from app.agriculture.identity.dependency import IdentityServices, delegation_dependency
from app.agriculture.identity.keys import KeyRing, utc_second
from app.agriculture.identity.providers import Ownership
from app.agriculture.identity.replay import PostgresReplayLedger
from app.agriculture.identity.verify import Verifier, require
from app.agriculture.main import create_app
from app.schemas.workbench import PlotAttributeMutationRequest
from app.services.workbench_service import WorkbenchService

PATH = "/internal/agri/v1/test/workbench-attributes"


def safe_failure(error):
    frames = traceback.extract_tb(error.__traceback__)
    return {
        "code": "identity_chain_fixture_failed",
        "type": type(error).__name__,
        "file": Path(frames[-1].filename).name if frames else None,
        "line": frames[-1].lineno if frames else None,
    }


def read_private_config(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, encoding="utf-8") as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600:
            raise ValueError(
                "private fixture configuration must be a regular 0600 file"
            )
        if info.st_size > 65536:
            raise ValueError("fixture configuration too large")
        value = json.load(source)
    contexts = value.get("contexts")
    if not isinstance(contexts, list) or not 1 <= len(contexts) <= 8:
        raise ValueError("bounded explicit fixture contexts required")
    if len({(c["tenant"], c["workspace"]) for c in contexts}) != len(contexts):
        raise ValueError("duplicate fixture scope")
    for context in contexts:
        if context.get("role", "interpreter") != "interpreter":
            raise ValueError("this bounded fixture adapts the interpreter role only")
    for name in ("geo_migration_dsn", "geo_runtime_dsn"):
        parsed = urlsplit(value[name])
        if parsed.scheme not in {"postgres", "postgresql"} or not parsed.hostname:
            raise ValueError("explicit local PostgreSQL fixture DSN required")
        if (
            "%" in parsed.hostname
            or not ipaddress.ip_address(parsed.hostname).is_loopback
        ):
            raise ValueError("only literal loopback fixture PostgreSQL is allowed")
        if (
            parse_qsl(parsed.query) not in ([], [("sslmode", "disable")])
            or parsed.fragment
        ):
            raise ValueError("fixture DSN overrides are not allowed")
    return value


@asynccontextmanager
async def domain_fixture(owner_dsn, runtime_dsn, identity):
    generator = legacy_fixture_context(owner_dsn, runtime_dsn, identity)
    value = await anext(generator)
    try:
        yield value
    finally:
        await generator.aclose()


class DatabaseOwnership:
    def __init__(self, pool):
        self.pool = pool

    async def resolve(self, tenant_id, workspace_id, resources):
        facts = []
        async with self.pool.acquire() as connection:
            for ref in resources:
                row = await connection.fetchrow(
                    "SELECT tenant_id,workspace_id,parcel_id,farm_id FROM "
                    "agriculture_test_fixture.resources WHERE tenant_id=$1 "
                    "AND workspace_id=$2 AND parcel_id=$3 AND $4='parcel'",
                    tenant_id,
                    workspace_id,
                    ref["id"],
                    ref["kind"],
                )
                if row is not None:
                    facts.append(
                        Ownership(
                            "parcel",
                            row["parcel_id"],
                            row["tenant_id"],
                            row["workspace_id"],
                            (row["farm_id"],),
                            1,
                        )
                    )
        return facts

    async def target(self, body):
        require(
            isinstance(body, dict)
            and set(body) == {"task_code", "plot_code", "crop_type"},
            "invalid_fixture_body",
            400,
        )
        require(
            all(isinstance(body[k], str) and 0 < len(body[k]) <= 128 for k in body),
            "invalid_fixture_body",
            400,
        )
        async with self.pool.acquire() as connection:
            row = await connection.fetchrow(
                "SELECT * FROM agriculture_test_fixture.resources "
                "WHERE task_code=$1 AND plot_code=$2",
                body["task_code"],
                body["plot_code"],
            )
            require(row is not None, "resource_not_found", 404)
            # The test-owned immutable catalog supplies independent domain facts,
            # never claims. Actual relationships are checked under runtime RLS.
            async with connection.transaction():
                await connection.execute(
                    "SELECT set_config('app.tenant_ids',$1,true), "
                    "set_config('app.workspace_id',$2,true)",
                    row["tenant_id"],
                    row["workspace_id"],
                )
                actual = await connection.fetchval(
                    "SELECT EXISTS(SELECT 1 FROM public.monitoring_tasks t "
                    "JOIN public.task_plots p ON p.task_id=t.id "
                    "WHERE t.id=$1 AND t.project_id=$2 AND p.plot_code=$3)",
                    row["task_id"],
                    row["project_id"],
                    row["plot_code"],
                )
                require(actual, "resource_not_found", 404)
        require(row is not None, "resource_not_found", 404)
        return row


async def build_chain(config, stack):
    await install_domain_schema(config["geo_migration_dsn"])
    fixtures = []
    for identity in config["contexts"]:
        fixtures.append(
            await stack.enter_async_context(
                domain_fixture(
                    config["geo_migration_dsn"], config["geo_runtime_dsn"], identity
                )
            )
        )
    owner = await asyncpg.connect(config["geo_migration_dsn"])
    stack.push_async_callback(owner.close)
    await owner.execute(
        (
            Path(__file__).parents[1] / "scripts/migrations/"
            "20261029_agriculture_identity_replay.sql"
        ).read_text()
    )
    # This catalog is test-owned synthetic metadata, not a new domain model or
    # production provisioning endpoint. Runtime has SELECT only; no write path.
    await owner.execute("""CREATE SCHEMA IF NOT EXISTS agriculture_test_fixture;
        REVOKE ALL ON SCHEMA agriculture_test_fixture FROM PUBLIC;
        GRANT USAGE ON SCHEMA agriculture_test_fixture TO flow_geo_app;
        CREATE TABLE IF NOT EXISTS agriculture_test_fixture.resources (
        tenant_id text NOT NULL,workspace_id text NOT NULL,project_id integer NOT NULL,
        task_id integer PRIMARY KEY,plot_code text UNIQUE NOT NULL,
        parcel_id text UNIQUE NOT NULL,farm_id text NOT NULL,
        task_code text UNIQUE NOT NULL);
        REVOKE ALL ON agriculture_test_fixture.resources FROM PUBLIC,flow_geo_app;
        GRANT SELECT ON agriculture_test_fixture.resources TO flow_geo_app;""")
    ready = []
    for fixture in fixtures:
        await bind_fixture_plot(owner, fixture)
        farm = "farm_" + fixture["parcel_id"].removeprefix("parcel_")
        await owner.execute(
            "INSERT INTO agriculture_test_fixture.resources "
            "VALUES($1,$2,$3,$4,$5,$6,$7,$8)",
            fixture["tenant"],
            fixture["workspace"],
            fixture["project"],
            fixture["ids"][0],
            fixture["plot_code"],
            fixture["parcel_id"],
            farm,
            fixture["task_code"],
        )
        ready.append(
            {
                "tenant": fixture["tenant"],
                "workspace": fixture["workspace"],
                "actor": fixture["actor"],
                "project_id": fixture["project"],
                "task_id": fixture["ids"][0],
                "plot_id": fixture["ids"][1],
                "historical_version_id": fixture["ids"][2],
                "historical_review_id": fixture["ids"][3],
                "task_code": fixture["task_code"],
                "plot_code": fixture["plot_code"],
                "parcel_id": fixture["parcel_id"],
                "farm_id": farm,
            }
        )
    pool = await create_runtime_pool(config["geo_runtime_dsn"], max_size=4)
    stack.push_async_callback(pool.close)
    runtime = await GISRuntimeDatabase.open(config["geo_runtime_dsn"])
    stack.push_async_callback(runtime.close)
    replay = PostgresReplayLedger(pool)
    await replay.require_safe_runtime()
    ownership = DatabaseOwnership(pool)
    client = await stack.enter_async_context(httpx.AsyncClient(trust_env=False))
    key = config["callback_key"]
    callback = PlatformAuthority(
        config["platform"],
        CallbackKey(
            key["kid"],
            Ed25519PrivateKey.from_private_bytes(base64.b64decode(key["seed"])),
            utc_second(key["not_before"]),
            utc_second(key["not_after"]),
        ),
        client,
    )
    app = create_app(
        IdentityServices(
            Verifier(KeyRing.from_config(config["platform_keys"])),
            ownership=ownership,
            authority=callback,
            replay=replay,
        )
    )

    async def route_resources(_request, body):
        row = await ownership.target(body)
        return [{"kind": "parcel", "id": row["parcel_id"]}]

    verified_dependency = Depends(
        delegation_dependency("agri.parcel.update", route_resources)
    )

    @app.post(PATH)
    async def mutate(verified=verified_dependency):
        row = await ownership.target(verified.body)
        fixture = next(f for f in fixtures if f["project"] == row["project_id"])
        async with runtime.session(
            verified,
            audit_adapters=fixture["adapters"],
            operation_adapters=fixture["registry"],
        ) as db:
            actor = await resolve_trusted_actor(db, verified, row["project_id"])
            await WorkbenchService().update_plot_attributes(
                db,
                verified.body["task_code"],
                verified.body["plot_code"],
                PlotAttributeMutationRequest(
                    land_class="耕地",
                    crop_type=verified.body["crop_type"],
                    operator_code=actor.legacy_user_code,
                ),
            )
            require(
                "agriculture_resolved_actor" not in db.info,
                "fixture_commit_missing",
                500,
            )
        return {"updated": True}

    return app, ready


async def main():
    config = read_private_config(sys.argv[1])
    async with AsyncExitStack() as stack:
        app, ready = await build_chain(config, stack)
        sock = socket.socket()
        stack.callback(sock.close)
        sock.bind(("127.0.0.1", 0))
        sock.listen(128)
        print(
            json.dumps({"port": sock.getsockname()[1], "contexts": ready}), flush=True
        )
        server = uvicorn.Server(
            uvicorn.Config(
                app,
                log_config=None,
                log_level="critical",
                access_log=False,
                http="h11",
                h11_max_incomplete_event_size=262144,
            )
        )
        await server.serve(sockets=[sock])


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except Exception as error:
        # Never emit driver exceptions which can contain a DSN or bound values.
        print(json.dumps(safe_failure(error)), file=sys.stderr)
        sys.exit(1)
