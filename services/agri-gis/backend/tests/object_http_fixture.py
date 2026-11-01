"""Test-process-only real PostgreSQL/HTTP object fixture; no public controls."""

import asyncio
import base64
import hashlib
import json
import os
import socket
import sys
import tempfile
import threading
from dataclasses import replace
from pathlib import Path
from uuid import uuid4

import asyncpg
import httpx
import uvicorn
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from domain_fixture_support import install_domain_schema
from test_agriculture_objects_postgres import FixtureDatabaseOwnership, insert

from app.agriculture.database import create_runtime_pool
from app.agriculture.domain_boundary import LEGACY_TABLES
from app.agriculture.identity.callback import CallbackKey, PlatformAuthority
from app.agriculture.identity.dependency import IdentityServices
from app.agriculture.identity.keys import KeyRing, utc_second
from app.agriculture.identity.verify import Verifier
from app.agriculture.main import create_app
from app.agriculture.object_storage import PrivateLocalStorage, object_key
from app.agriculture.object_store import ObjectReference, PostgresObjectStore

BODY = b"synthetic-private-object-v1\n0123456789\n"


class IsolationControlledStorage:
    """Private test control only; actual bytes still use validated local storage."""

    def __init__(self, root):
        self.storage = PrivateLocalStorage(root)
        self.lock = threading.Lock()
        self.armed = False
        self.started = threading.Event()
        self.release = threading.Event()

    def arm(self):
        with self.lock:
            if self.armed or (self.started.is_set() and not self.release.is_set()):
                raise ValueError("fixture read already armed")
            self.started.clear()
            self.release.clear()
            self.armed = True

    def read(self, **kwargs):
        with self.lock:
            pause = self.armed
            self.armed = False
        if pause:
            self.started.set()
            if not self.release.wait(timeout=15):
                raise ValueError("fixture read control deadline")
        return self.storage.read(**kwargs)


async def isolation_snapshot(owner, scopes):
    """Committed full domain rows, assertion owner only; output hash only.

    Replay/security storage is excluded: verification may legitimately consume
    a nonce. No object, domain, job sidecar or actor-audit state is excluded.
    """
    tables = sorted(
        LEGACY_TABLES
        | {
            "agriculture_project_bindings",
            "agriculture_actor_bindings",
            "agriculture_actor_audit",
            "agriculture_legacy_plot_ownership",
            "agriculture_legacy_resource_references",
            "agriculture_private_objects",
            "agriculture_job_authorities",
            "agriculture_job_authority_checks",
        }
    )
    rows = {}
    async with owner.transaction():
        await owner.execute(
            "SELECT set_config('app.tenant_ids',$1,true)",
            ",".join(sorted({t for t, _ in scopes})),
        )
        for table in tables:
            rows[table] = await owner.fetchval(
                "SELECT coalesce(jsonb_agg(to_jsonb(t) "
                "ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text "
                f'FROM public."{table}" t'
            )
        rows["fixture_ownership"] = await owner.fetchval(
            "SELECT coalesce(jsonb_agg(to_jsonb(t) "
            "ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text "
            "FROM object_test_fixture.authority t"
        )
    return hashlib.sha256(
        json.dumps(rows, sort_keys=True, separators=(",", ":")).encode()
    ).hexdigest()


def private_config(path):
    path = Path(path)
    if path.is_symlink() or path.stat().st_mode & 0o077:
        raise ValueError("private fixture configuration required")
    config = json.loads(path.read_text())
    legacy_fields = {
        "owner_dsn",
        "app_dsn",
        "tenant",
        "workspace",
        "platform",
        "platform_keys",
        "callback_key",
    }
    extended_fields = {
        "extended",
        "foreign_tenant",
        "foreign_workspace",
        "second_workspace",
    }
    if set(config) not in (legacy_fields, legacy_fields | extended_fields):
        raise ValueError("invalid fixture configuration")
    if not isinstance(config.get("extended", False), bool):
        raise ValueError("invalid fixture mode")
    return config


async def main():
    os.umask(0o077)
    config = {
        "extended": False,
        "foreign_tenant": "",
        "foreign_workspace": "",
        "second_workspace": "",
        **private_config(sys.argv[1]),
    }
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
    items = [item]
    scopes = {(tenant, workspace)}
    foreign_id, report_id = "", ""
    if config["extended"]:
        scopes |= {
            (config["foreign_tenant"], config["foreign_workspace"]),
            (tenant, config["second_workspace"]),
        }
        foreign_id, report_id = "foreign_" + uuid4().hex, "report_" + uuid4().hex
        for other_id, version, resource_kind, resource_id, category, t, w in (
            (
                foreign_id,
                1,
                "asset",
                "asset_foreign",
                "original",
                config["foreign_tenant"],
                config["foreign_workspace"],
            ),
            (
                item.object_id,
                2,
                "asset",
                "asset_foreign",
                "original",
                config["foreign_tenant"],
                config["foreign_workspace"],
            ),
            (report_id, 1, "report", "report_a", "report", tenant, workspace),
        ):
            other = replace(
                item,
                tenant_id=t,
                workspace_id=w,
                object_id=other_id,
                version=version,
                resource_kind=resource_kind,
                resource_id=resource_id,
                category=category,
                object_key=object_key(t, w, other_id, version),
            )
            await insert(owner, other)
            items.append(other)
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
    if config["extended"]:
        for t, w, kind, resource in (
            (tenant, workspace, "report", "report_a"),
            (
                config["foreign_tenant"],
                config["foreign_workspace"],
                "asset",
                "asset_foreign",
            ),
        ):
            await owner.execute(
                "INSERT INTO object_test_fixture.authority VALUES "
                "($1,$2,$3,$4,'[\"farm_a\"]',1,'active') "
                "ON CONFLICT(tenant,workspace,kind,id) "
                "DO UPDATE SET revision=1,state='active'",
                t,
                w,
                kind,
                resource,
            )
        # Explicitly synthetic canaries, never claims of real farm activity.
        # Verify owner FORCE RLS sees every intended audit scope before tests.
        async with owner.transaction():
            await owner.execute(
                "SELECT set_config('app.tenant_ids',$1,true)",
                ",".join(sorted({t for t, _ in scopes})),
            )
            for t, w in sorted(scopes):
                audit_id = str(uuid4())
                await owner.execute(
                    "INSERT INTO agriculture_actor_audit "
                    "(id,tenant_id,workspace_id,actor_id,project_id,legacy_member_id,"
                    "legacy_user_code,legacy_role,policy_version,"
                    "authorization_version,request_id,action,resources,changes) "
                    "VALUES($1,$2,$3,'object_fixture',0,0,'fixture','fixture',"
                    "'agriculture-policy/1.0.0',1,'fixture','fixture.canary','[]','[]')",
                    audit_id,
                    t,
                    w,
                )
                assert (
                    await owner.fetchval(
                        "SELECT count(*) FROM agriculture_actor_audit WHERE id=$1",
                        audit_id,
                    )
                    == 1
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
            for registered in items:
                file = root / registered.object_key
                file.parent.mkdir(parents=True, exist_ok=True)
                file.write_bytes(BODY)
            storage = IsolationControlledStorage(root)
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
                        object_storage=storage,
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
                                "foreign_object_id": foreign_id,
                                "report_object_id": report_id,
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
                        elif name == "snapshot":
                            digest = await isolation_snapshot(owner, scopes)
                            print(json.dumps({"sha256": digest}), flush=True)
                            continue
                        elif name == "arm_read":
                            storage.arm()
                        elif name == "read_state":
                            print(
                                json.dumps({"started": storage.started.is_set()}),
                                flush=True,
                            )
                            continue
                        elif name == "release_read":
                            storage.release.set()
                        else:
                            raise ValueError("unknown fixture command")
                        print(json.dumps({"ok": True}), flush=True)
                finally:
                    storage.release.set()
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
