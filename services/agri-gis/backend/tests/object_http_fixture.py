"""Test-process-only real PostgreSQL/HTTP object fixture; no public controls."""

import asyncio
import base64
import hashlib
import json
import os
import socket
import sys
import tempfile
from pathlib import Path
from uuid import uuid4

import asyncpg
import httpx
import uvicorn
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from domain_fixture_support import install_domain_schema
from test_agriculture_objects_postgres import FixtureDatabaseOwnership, insert

from app.agriculture.database import create_runtime_pool
from app.agriculture.identity.callback import CallbackKey, PlatformAuthority
from app.agriculture.identity.dependency import IdentityServices
from app.agriculture.identity.keys import KeyRing, utc_second
from app.agriculture.identity.verify import Verifier
from app.agriculture.main import create_app
from app.agriculture.object_storage import PrivateLocalStorage, object_key
from app.agriculture.object_store import ObjectReference, PostgresObjectStore

BODY = b"synthetic-private-object-v1\n0123456789\n"


def private_config(path):
    path = Path(path)
    if path.is_symlink() or path.stat().st_mode & 0o077:
        raise ValueError("private fixture configuration required")
    config = json.loads(path.read_text())
    if set(config) != {
        "owner_dsn",
        "app_dsn",
        "tenant",
        "workspace",
        "platform",
        "platform_keys",
        "callback_key",
    }:
        raise ValueError("invalid fixture configuration")
    return config


async def main():
    os.umask(0o077)
    config = private_config(sys.argv[1])
    await install_domain_schema(config["owner_dsn"])
    owner = await asyncpg.connect(config["owner_dsn"])
    pool = await create_runtime_pool(config["app_dsn"])
    tenant, workspace = config["tenant"], config["workspace"]
    object_id = "object_" + uuid4().hex
    item = ObjectReference(
        tenant,
        workspace,
        object_id,
        1,
        "asset",
        "asset_a",
        1,
        "original",
        object_key(tenant, workspace, object_id, 1),
        hashlib.sha256(BODY).hexdigest(),
        len(BODY),
        "application/octet-stream",
        "demo",
    )
    await insert(owner, item)
    await owner.execute("""
CREATE SCHEMA IF NOT EXISTS object_test_fixture AUTHORIZATION flow_geo_migrate;
GRANT USAGE ON SCHEMA object_test_fixture TO flow_geo_app;
CREATE TABLE IF NOT EXISTS object_test_fixture.authority (
 tenant text,workspace text,kind text,id text,farms jsonb,revision bigint,state text,
 PRIMARY KEY(tenant,workspace,kind,id));
ALTER TABLE object_test_fixture.authority ENABLE ROW LEVEL SECURITY;
ALTER TABLE object_test_fixture.authority FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS fixture_owner ON object_test_fixture.authority;
CREATE POLICY fixture_owner ON object_test_fixture.authority TO flow_geo_migrate
 USING(true) WITH CHECK(true);
DROP POLICY IF EXISTS fixture_scope ON object_test_fixture.authority;
CREATE POLICY fixture_scope ON object_test_fixture.authority TO flow_geo_app
 USING(tenant=current_setting('app.tenant_ids',true)
 AND workspace=current_setting('app.workspace_id',true));
REVOKE ALL ON object_test_fixture.authority FROM PUBLIC,flow_geo_app,flow_geo_worker;
GRANT SELECT ON object_test_fixture.authority TO flow_geo_app;
""")
    await owner.execute(
        "INSERT INTO object_test_fixture.authority VALUES "
        "($1,$2,'asset','asset_a','[\"farm_a\"]',1,'active') "
        "ON CONFLICT(tenant,workspace,kind,id) DO UPDATE SET revision=1,state='active'",
        tenant,
        workspace,
    )
    key = config["callback_key"]
    callback = CallbackKey(
        key["kid"],
        Ed25519PrivateKey.from_private_bytes(
            base64.b64decode(key["seed"], validate=True)
        ),
        utc_second(key["not_before"]),
        utc_second(key["not_after"]),
    )
    try:
        with tempfile.TemporaryDirectory(
            prefix="private-objects-", dir=Path(sys.argv[1]).parent
        ) as directory:
            root = Path(directory).resolve()
            file = root / item.object_key
            file.parent.mkdir(parents=True)
            file.write_bytes(BODY)
            async with httpx.AsyncClient(trust_env=False) as client:
                store = PostgresObjectStore(pool)
                await store.require_ready()
                app = create_app(
                    IdentityServices(
                        Verifier(KeyRing.from_config(config["platform_keys"])),
                        ownership=FixtureDatabaseOwnership(pool),
                        authority=PlatformAuthority(
                            config["platform"], callback, client
                        ),
                        object_store=store,
                        object_storage=PrivateLocalStorage(root),
                    )
                )
                sock = socket.socket()
                sock.bind(("127.0.0.1", 0))
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
                serving = asyncio.create_task(server.serve(sockets=[sock]))
                try:
                    for _ in range(200):
                        if server.started:
                            break
                        await asyncio.sleep(0.01)
                    if not server.started:
                        raise RuntimeError("fixture listener unavailable")
                    print(
                        json.dumps(
                            {
                                "port": sock.getsockname()[1],
                                "object_id": object_id,
                                "version": 1,
                                "resource": {"kind": "asset", "id": "asset_a"},
                                "resource_revision": 1,
                                "content_sha256": item.content_sha256,
                                "size_bytes": item.size_bytes,
                            }
                        ),
                        flush=True,
                    )
                    while True:
                        line = await asyncio.to_thread(sys.stdin.readline)
                        if not line:
                            break
                        command = json.loads(line)
                        if set(command) != {"command"}:
                            raise ValueError("unknown fixture command")
                        name = command["command"]
                        if name in {"withdraw_resource", "restore_resource"}:
                            await owner.execute(
                                "UPDATE object_test_fixture.authority "
                                "SET state=$3 WHERE tenant=$1 AND workspace=$2 "
                                "AND kind='asset' AND id='asset_a'",
                                tenant,
                                workspace,
                                "revoked" if name == "withdraw_resource" else "active",
                            )
                        elif name == "revoke_object":
                            await owner.execute(
                                "UPDATE agriculture_private_objects "
                                "SET state='revoked' WHERE tenant_id=$1 "
                                "AND workspace_id=$2 AND object_id=$3",
                                tenant,
                                workspace,
                                object_id,
                            )
                        elif name == "stats":
                            count = await owner.fetchval(
                                "SELECT count(*) FROM "
                                "agriculture_private_objects WHERE tenant_id=$1 "
                                "AND workspace_id=$2 AND state='active'",
                                tenant,
                                workspace,
                            )
                            print(json.dumps({"active_objects": count}), flush=True)
                            continue
                        else:
                            raise ValueError("unknown fixture command")
                        print(json.dumps({"ok": True}), flush=True)
                finally:
                    server.should_exit = True
                    await asyncio.wait_for(serving, timeout=5)
                    sock.close()
    finally:
        await pool.close()
        await owner.close()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except Exception:
        print("private object fixture failed", file=sys.stderr)
        raise SystemExit(1) from None
