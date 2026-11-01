"""Real private PG/HTTP byte fixtures, not production asset registration."""

import asyncio
import copy
import json
import socket
from contextlib import asynccontextmanager
from dataclasses import asdict, replace
from uuid import uuid4

import asyncpg
import httpx
import pytest
import uvicorn
from domain_fixture_support import install_domain_schema
from test_agriculture_actors_postgres import pg_config
from test_agriculture_identity import sign
from test_agriculture_object_gateway import object_fixture

from app.agriculture.database import create_runtime_pool
from app.agriculture.identity.providers import Ownership
from app.agriculture.identity.verify import IdentityError
from app.agriculture.object_storage import object_key
from app.agriculture.object_store import PostgresObjectStore


async def set_scope(connection, tenant, workspace):
    await connection.execute(
        "SELECT set_config('app.tenant_ids',$1,true),"
        "set_config('app.workspace_id',$2,true)",
        tenant,
        workspace,
    )


async def request(fixture, *, path, headers=None):
    claims = copy.deepcopy(fixture[3])
    claims["request"] = {"method": "GET", "path": path}
    async with httpx.AsyncClient(
        base_url=fixture[0].state.test_url, trust_env=False
    ) as client:
        return await client.get(
            path,
            headers={
                "Authorization": "Bearer " + sign(claims, fixture[2]),
                **(headers or {}),
            },
        )


async def insert(owner, item):
    fields = asdict(item)
    await owner.execute(
        "INSERT INTO public.agriculture_private_objects "
        "("
        + ",".join(fields)
        + ",distribution_allowed) VALUES ("
        + ",".join("$" + str(i) for i in range(1, len(fields) + 1))
        + ",true)",
        *fields.values(),
    )


class FixtureDatabaseOwnership:
    """Separate authoritative fixture table; never infer facts from a token."""

    def __init__(self, pool):
        self.pool = pool

    async def resolve(self, tenant, workspace, resources):
        async with self.pool.acquire() as connection:
            async with connection.transaction():
                await set_scope(connection, tenant, workspace)
                result = []
                for ref in resources:
                    row = await connection.fetchrow(
                        "SELECT * FROM object_test_fixture.authority "
                        "WHERE tenant=$1 AND workspace=$2 AND kind=$3 AND id=$4",
                        tenant,
                        workspace,
                        ref["kind"],
                        ref["id"],
                    )
                    if row:
                        result.append(
                            Ownership(
                                row["kind"],
                                row["id"],
                                tenant,
                                workspace,
                                tuple(json.loads(row["farms"])),
                                row["revision"],
                                row["state"],
                            )
                        )
                return result


@asynccontextmanager
async def setup(tmp_path):
    owner_dsn, runtime_dsn = pg_config()
    await install_domain_schema(owner_dsn)
    owner = await asyncpg.connect(owner_dsn)
    pool = await create_runtime_pool(runtime_dsn)
    try:
        fixture = object_fixture(tmp_path)
        item = replace(fixture[4], object_id="object_" + uuid4().hex)
        item = replace(
            item,
            object_key=object_key(
                item.tenant_id, item.workspace_id, item.object_id, item.version
            ),
        )
        file = tmp_path / item.object_key
        file.parent.mkdir(parents=True)
        file.write_bytes(fixture[-1])
        await insert(owner, item)
        for tenant, workspace in [
            ("tenant_b", "workspace_a"),
            ("tenant_a", "workspace_b"),
        ]:
            other = replace(
                item,
                tenant_id=tenant,
                workspace_id=workspace,
                object_key=object_key(tenant, workspace, item.object_id, 1),
            )
            await insert(owner, other)
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
            "('tenant_a','workspace_a','asset','asset_a','[\"farm_a\"]',1,'active') "
            "ON CONFLICT(tenant,workspace,kind,id) "
            "DO UPDATE SET revision=1,state='active'"
        )
        fixture = (*fixture[:4], item, fixture[-1])
        fixture[1].object_store = PostgresObjectStore(pool)
        fixture[1].ownership = FixtureDatabaseOwnership(pool)
        await fixture[1].object_store.require_ready()
        yield fixture, owner, pool
    finally:
        await pool.close()
        await owner.close()


@pytest.mark.asyncio
async def test_postgres_private_objects_scope_immutability(tmp_path):
    async with setup(tmp_path) as (fixture, owner, pool):
        item, store = fixture[4], fixture[1].object_store
        assert (
            await store.get(item.tenant_id, item.workspace_id, item.object_id, 1)
            == item
        )
        for tenant, workspace in [
            ("tenant_unknown", "workspace_a"),
            ("tenant_a", "workspace_unknown"),
            ("tenant_a,tenant_b", "workspace_a"),
        ]:
            with pytest.raises(IdentityError):
                await store.get(tenant, workspace, item.object_id, 1)
        async with pool.acquire() as runtime:
            assert (
                await runtime.fetchval(
                    "SELECT count(*) FROM agriculture_private_objects"
                )
                == 0
            )
            async with runtime.transaction():
                await set_scope(runtime, item.tenant_id, item.workspace_id)
                with pytest.raises(asyncpg.InsufficientPrivilegeError):
                    async with runtime.transaction():
                        await runtime.execute(
                            "UPDATE agriculture_private_objects SET state='revoked'"
                        )
                assert (
                    await runtime.fetchval(
                        "SELECT count(*) FROM agriculture_private_objects"
                    )
                    >= 1
                )
                assert not await runtime.fetchval(
                    "SELECT EXISTS(SELECT 1 FROM agriculture_private_objects "
                    "WHERE tenant_id<>$1 OR workspace_id<>$2)",
                    item.tenant_id,
                    item.workspace_id,
                )
            assert (
                await runtime.fetchval(
                    "SELECT count(*) FROM agriculture_private_objects"
                )
                == 0
            )
        for sql in [
            "UPDATE agriculture_private_objects SET content_sha256=repeat('0',64) "
            "WHERE object_id=$1",
            "DELETE FROM agriculture_private_objects WHERE object_id=$1",
        ]:
            with pytest.raises(asyncpg.CheckViolationError):
                async with owner.transaction():
                    await owner.execute(sql, item.object_id)
        await owner.execute(
            "UPDATE agriculture_private_objects SET state='revoked' WHERE object_id=$1",
            item.object_id,
        )
        with pytest.raises(IdentityError):
            await store.get(item.tenant_id, item.workspace_id, item.object_id, 1)
        with pytest.raises(asyncpg.CheckViolationError):
            async with owner.transaction():
                await owner.execute(
                    "UPDATE agriculture_private_objects SET state='active' "
                    "WHERE object_id=$1",
                    item.object_id,
                )


@pytest.mark.asyncio
async def test_postgres_private_object_http_bytes_and_withdrawal(tmp_path):
    async with setup(tmp_path) as (fixture, owner, _pool):
        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        fixture[0].state.test_url = f"http://127.0.0.1:{sock.getsockname()[1]}"
        server = uvicorn.Server(
            uvicorn.Config(
                fixture[0], log_config=None, access_log=False, log_level="critical"
            )
        )
        task = asyncio.create_task(server.serve(sockets=[sock]))
        try:
            for _ in range(100):
                if server.started:
                    break
                await asyncio.sleep(0.01)
            assert server.started
            await assert_http(fixture, owner)
        finally:
            server.should_exit = True
            await asyncio.wait_for(task, timeout=5)
            sock.close()


async def assert_http(fixture, owner):
    item = fixture[4]
    path = (
        "/internal/agri/v1/tenants/tenant_a/workspaces/workspace_a/"
        f"assets/objects/{item.object_id}/versions/1"
    )
    response = await request(fixture, path=path)
    assert response.status_code == 200 and response.content == fixture[-1]
    for changed in [
        path.replace("tenant_a", "tenant_b"),
        path.replace("workspace_a", "workspace_b"),
    ]:
        response = await request(fixture, path=changed)
        assert response.status_code == 403 and fixture[-1] not in response.content
    response = await request(fixture, path=path, headers={"Range": "bytes=0-4"})
    assert response.status_code == 206 and response.content == fixture[-1][:5]
    await owner.execute("UPDATE object_test_fixture.authority SET state='revoked'")
    assert (await request(fixture, path=path)).status_code == 404
    await owner.execute("UPDATE object_test_fixture.authority SET state='active'")
    await owner.execute(
        "UPDATE agriculture_private_objects SET state='revoked' WHERE object_id=$1",
        item.object_id,
    )
    response = await request(fixture, path=path, headers={"If-None-Match": '"cached"'})
    assert response.status_code in {400, 404} and fixture[-1] not in response.content
    assert response.headers["cache-control"] == "private, no-store"
