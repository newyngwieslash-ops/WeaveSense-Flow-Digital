"""Real signed HTTP/PG/bytes components; fixture authority is not Go acceptance."""

import asyncio
import copy
import socket
from contextlib import asynccontextmanager

import httpx
import pytest
import uvicorn
from test_agriculture_asset_catalog_postgres import (
    command,
    setup,
    trusted_fixture_entity,
)
from test_agriculture_asset_catalog_rules import metadata
from test_agriculture_asset_object_identity import fixture as crypto_fixture
from test_agriculture_identity import build_fixture, sign

from app.agriculture.asset_catalog_objects import CatalogObjectStore
from app.agriculture.asset_catalog_store import AssetCatalogOwnership
from app.agriculture.asset_object_identity import (
    ASSET_OBJECT_TYP,
    ROUTE,
    asset_ownership_hash,
)
from app.agriculture.identity.verify import BUSINESS_TYP, require
from app.agriculture.object_storage import PrivateLocalStorage
from app.agriculture.object_store import PostgresObjectStore


class FixtureDatabaseDownloadAuthority:
    """Explicit component substitute; production uses the Go signed callback."""

    def __init__(self, owner):
        self.owner = owner

    async def check_asset_object(self, proof):
        c = proof.claims
        allowed = await self.owner.fetchval(
            "SELECT EXISTS(SELECT 1 FROM asset_test_fixture.authority "
            "WHERE tenant=$1 AND workspace=$2 AND actor=$3 AND asset_id=$4 "
            "AND epoch=$5 AND revision=$6 AND binding_hash=$7 AND active)",
            c["tenant_id"],
            c["workspace_id"],
            c["actor"]["id"],
            c["asset_id"],
            c["authorization_version"],
            c["asset_revision"],
            c["ownership_hash"],
        )
        require(allowed, "authority_denied", 403)


@asynccontextmanager
async def http_fixture(tmp_path):
    async with setup() as (catalog, owner, pool, source):
        value = metadata()
        value["rights_declaration"]["permitted_uses"].append("export")
        source["sources"][0]["rights"]["permitted_uses"].append("export")
        source["sources"][0]["rights"]["redistribution"] = "allowed"
        verified, body = command(value=value)
        receipt = await catalog.execute(verified, "asset.create", body)
        obj, key, content = await trusted_fixture_entity(
            owner, catalog, receipt["asset_id"], value, 2
        )
        location = tmp_path / key
        location.parent.mkdir(parents=True)
        location.write_bytes(content)
        app, services, private, _, _, _, _ = build_fixture()
        services.asset_catalog_store = catalog
        services.asset_catalog_ownership = AssetCatalogOwnership(pool)
        services.object_store = CatalogObjectStore(PostgresObjectStore(pool), catalog)
        services.object_storage = PrivateLocalStorage(tmp_path.resolve())
        services.asset_object_authority = FixtureDatabaseDownloadAuthority(owner)
        _, _, claims = crypto_fixture()
        fact = (
            await services.asset_catalog_ownership.resolve_asset(
                "tenant_a", "workspace_a", [receipt["asset_id"]]
            )
        )[0]
        claims.update(
            asset_id=receipt["asset_id"],
            ownership_hash=asset_ownership_hash(fact),
            object_id=obj,
        )
        path = ROUTE.format(
            tenant="tenant_a", workspace="workspace_a", object=obj, version=1
        )
        claims["request"] = {"method": "GET", "path": path}
        await owner.execute("""
CREATE SCHEMA IF NOT EXISTS asset_test_fixture;
REVOKE ALL ON SCHEMA asset_test_fixture FROM PUBLIC;
CREATE TABLE IF NOT EXISTS asset_test_fixture.authority (
 tenant text,workspace text,actor text,asset_id text PRIMARY KEY,
 epoch bigint,revision bigint,binding_hash text,active boolean);
REVOKE ALL ON asset_test_fixture.authority FROM PUBLIC;
""")
        await owner.execute(
            "INSERT INTO asset_test_fixture.authority "
            "VALUES($1,$2,$3,$4,$5,$6,$7,true)",
            claims["tenant_id"],
            claims["workspace_id"],
            claims["actor"]["id"],
            claims["asset_id"],
            claims["authorization_version"],
            2,
            claims["ownership_hash"],
        )
        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        server = uvicorn.Server(
            uvicorn.Config(app, log_level="critical", access_log=False)
        )
        task = asyncio.create_task(server.serve(sockets=[sock]))
        try:
            for _ in range(200):
                if server.started:
                    break
                if task.done():
                    await task
                await asyncio.sleep(0.01)
            assert server.started
            async with httpx.AsyncClient(
                base_url=f"http://127.0.0.1:{sock.getsockname()[1]}", trust_env=False
            ) as client:
                yield client, services, private, claims, content, owner, source
        finally:
            server.should_exit = True
            await asyncio.wait_for(task, timeout=5)
            sock.close()


async def request(
    parts, *, method="GET", changes=None, headers=None, typ=ASSET_OBJECT_TYP
):
    client, _, private, original, *_ = parts
    c = copy.deepcopy(original)
    c.update(changes or {})
    c["request"]["method"] = method
    return await client.request(
        method,
        c["request"]["path"],
        headers={
            "Authorization": "Bearer "
            + sign(c, private, {"alg": "EdDSA", "kid": "fixture_key", "typ": typ}),
            **(headers or {}),
        },
    )


@pytest.mark.asyncio
async def test_asset_object_real_http_scope_range_head_and_profile(tmp_path):
    async with http_fixture(tmp_path) as parts:
        response = await request(parts)
        assert response.status_code == 200 and response.content == parts[4]
        assert (
            response.headers["cache-control"] == "private, no-store"
            and "etag" not in response.headers
        )
        response = await request(parts, method="HEAD")
        assert response.status_code == 200 and response.content == b""
        response = await request(parts, headers={"Range": "bytes=2-6"})
        assert response.status_code == 206 and response.content == parts[4][2:7]
        response = await request(parts, headers={"Range": "bytes=9999-"})
        assert response.status_code == 416
        for change in (
            {"tenant_id": "tenant_b"},
            {"workspace_id": "workspace_b"},
            {"asset_revision": 1},
            {"ownership_hash": "0" * 64},
            {"actor": {"kind": "user", "id": "other_actor"}},
            {"authorization_version": 8},
        ):
            response = await request(parts, changes=change)
            assert (
                response.status_code in {401, 403, 404}
                and parts[4] not in response.content
            )
        assert (await request(parts, typ=BUSINESS_TYP)).status_code == 401


@pytest.mark.asyncio
@pytest.mark.parametrize("withdraw", ["rights", "actor"])
async def test_asset_object_real_http_withdraw_during_storage_read(tmp_path, withdraw):
    async with http_fixture(tmp_path) as parts:
        _, services, _, claims, content, owner, source = parts
        original = services.object_storage
        loop = asyncio.get_running_loop()

        class WithdrawingStorage:
            def read(self, **kwargs):
                body = original.read(**kwargs)
                if withdraw == "rights":
                    source["sources"][0]["rights"]["status"] = "revoked"
                else:
                    asyncio.run_coroutine_threadsafe(
                        owner.execute(
                            "UPDATE asset_test_fixture.authority SET active=false "
                            "WHERE asset_id=$1",
                            claims["asset_id"],
                        ),
                        loop,
                    ).result(timeout=3)
                return body

        services.object_storage = WithdrawingStorage()
        response = await request(parts)
        assert response.status_code == 403 and content not in response.content
        services.object_storage = original
        assert (await request(parts)).status_code == 403


@pytest.mark.asyncio
async def test_asset_object_real_http_history_needs_fresh_current_authority(tmp_path):
    async with http_fixture(tmp_path) as parts:
        _, services, _, claims, content, owner, _source = parts
        catalog = services.asset_catalog_store
        value = (await catalog.get("tenant_a", "workspace_a", claims["asset_id"], 2))[
            "metadata"
        ]
        await trusted_fixture_entity(owner, catalog, claims["asset_id"], value, 3)
        assert (await request(parts)).status_code == 404
        fact = (
            await services.asset_catalog_ownership.resolve_asset(
                "tenant_a", "workspace_a", [claims["asset_id"]]
            )
        )[0]
        fresh_hash = asset_ownership_hash(fact)
        await owner.execute(
            "UPDATE asset_test_fixture.authority SET revision=3,binding_hash=$2 "
            "WHERE asset_id=$1",
            claims["asset_id"],
            fresh_hash,
        )
        response = await request(
            parts, changes={"asset_revision": 3, "ownership_hash": fresh_hash}
        )
        assert response.status_code == 200 and response.content == content
        # Selecting a different historical entity cannot reuse this object path.
        response = await request(
            parts,
            changes={
                "asset_revision": 3,
                "asset_version": 1,
                "ownership_hash": fresh_hash,
            },
        )
        assert response.status_code == 404 and content not in response.content
