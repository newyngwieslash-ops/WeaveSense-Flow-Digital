"""Signed HTTP component fixtures with real local bytes; not live asset facts."""

import copy
import hashlib
import json
from dataclasses import replace

import httpx
import pytest
from test_agriculture_identity import build_fixture, sign

from app.agriculture.identity.canonical import canonicalize
from app.agriculture.identity.providers import Ownership
from app.agriculture.identity.verify import SERVICE_TYP, IdentityError
from app.agriculture.object_storage import PrivateLocalStorage, object_key
from app.agriculture.object_store import ObjectReference

PATH = (
    "/internal/agri/v1/tenants/tenant_a/workspaces/workspace_a/"
    "assets/objects/object_a/versions/1"
)


class FixtureObjectStore:
    def __init__(self, item):
        self.item = item
        self.calls = 0

    async def get(self, tenant, workspace, object_id, version):
        self.calls += 1
        item = self.item
        if item is None or (tenant, workspace, object_id, version) != (
            item.tenant_id,
            item.workspace_id,
            item.object_id,
            item.version,
        ):
            raise IdentityError("object_not_found", 404)
        return item


def object_fixture(tmp_path):
    app, services, private, claims, _, _, _ = build_fixture()
    body = b"actual synthetic private file\x00\xff"
    item = ObjectReference(
        "tenant_a",
        "workspace_a",
        "object_a",
        1,
        "asset",
        "asset_a",
        1,
        "thumbnail",
        object_key("tenant_a", "workspace_a", "object_a", 1),
        hashlib.sha256(body).hexdigest(),
        len(body),
        "application/octet-stream",
        "demo",
    )
    file = tmp_path / item.object_key
    file.parent.mkdir(parents=True)
    file.write_bytes(body)
    services.object_store = FixtureObjectStore(item)
    services.object_storage = PrivateLocalStorage(tmp_path)
    services.ownership.facts = [
        Ownership("asset", "asset_a", "tenant_a", "workspace_a", ("farm_a",), 1)
    ]
    claims["resource_scope"] = [{"kind": "asset", "id": "asset_a"}]
    claims["resource_versions"] = [
        {"resource": claims["resource_scope"][0], "revision": 1}
    ]
    claims["scopes"] = ["agri.asset_download.read"]
    claims["request"] = {"method": "GET", "path": PATH}
    return app, services, private, claims, item, body


async def request(fixture, *, method="GET", path=PATH, changes=None, headers=None):
    app, _, key, claims, _, _ = fixture
    claims = copy.deepcopy(claims)
    claims["request"] = {"method": method, "path": path}
    claims.update(changes or {})
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app), base_url="http://test"
    ) as client:
        return await client.request(
            method,
            path,
            headers={"Authorization": "Bearer " + sign(claims, key), **(headers or {})},
        )


@pytest.mark.asyncio
async def test_actual_bytes_range_head_no_public_cache(tmp_path):
    fixture = object_fixture(tmp_path)
    response = await request(fixture)
    assert response.status_code == 200 and response.content == fixture[-1]
    assert response.headers["cache-control"] == "private, no-store"
    assert "etag" not in response.headers
    response = await request(fixture, headers={"Range": "bytes=2-6"})
    assert response.status_code == 206 and response.content == fixture[-1][2:7]
    response = await request(fixture, method="HEAD")
    assert response.status_code == 200 and not response.content
    assert int(response.headers["content-length"]) == len(fixture[-1])
    response = await request(fixture, headers={"If-None-Match": '"old"'})
    assert response.status_code == 200


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "path",
    [
        PATH.replace("object_a", "object_b"),
        PATH.replace("tenant_a", "tenant_b"),
        PATH.replace("workspace_a", "workspace_b"),
        PATH.replace("/1", "/2"),
        PATH.replace("/1", "/01"),
        PATH.replace("assets", "reports"),
        PATH.replace("object_a", "object%5fa"),
        PATH + "?key=other",
    ],
)
async def test_signed_path_alias_and_object_swap_denied(tmp_path, path):
    response = await request(object_fixture(tmp_path), path=path)
    assert response.status_code in {400, 401, 403, 404}
    assert response.headers["cache-control"] == "private, no-store"


@pytest.mark.asyncio
async def test_current_owner_epoch_withdrawal_and_missing_provider(tmp_path):
    fixture = object_fixture(tmp_path)
    services = fixture[1]
    services.authority.denied = True
    assert (await request(fixture)).status_code == 403
    services.authority.denied = False
    services.ownership.facts[0] = replace(services.ownership.facts[0], revision=2)
    assert (await request(fixture)).status_code == 404
    services.ownership.facts[0] = replace(services.ownership.facts[0], revision=1)
    services.object_store.item = None
    assert (await request(fixture)).status_code in {400, 404}
    services.object_store.item = fixture[4]
    services.ownership = None
    assert (await request(fixture)).status_code == 503


@pytest.mark.asyncio
async def test_revoked_during_actual_storage_read_sends_no_bytes(tmp_path):
    fixture = object_fixture(tmp_path)
    services = fixture[1]
    original = services.object_storage.read

    def withdraw(**kwargs):
        data = original(**kwargs)
        services.object_store.item = None
        return data

    services.object_storage.read = withdraw
    response = await request(fixture)
    assert response.status_code == 404 and fixture[-1] not in response.content


@pytest.mark.asyncio
async def test_service_metadata_minimal_and_scope_bound(tmp_path):
    app, _, key, business, item, _ = object_fixture(tmp_path)
    body = {
        "tenant_id": item.tenant_id,
        "workspace_id": item.workspace_id,
        "object_id": item.object_id,
        "version": item.version,
    }
    claims = {
        k: v
        for k, v in business.items()
        if k
        not in {
            "actor",
            "resource_scope",
            "scopes",
            "authorization_version",
            "policy_version",
            "farm_ids",
            "resource_versions",
        }
    }
    claims["capability"] = "object.resolve"
    path = "/internal/agri/v1/objects/resolve"
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app), base_url="http://test"
    ) as client:
        for override, status in [
            ({}, 200),
            ({"tenant_id": "tenant_other"}, 403),
            ({"object_key": "secret"}, 400),
        ]:
            raw = json.dumps(body | override).encode()
            claims["request"] = {
                "method": "POST",
                "path": path,
                "canonicalization_version": "rfc8785-safe-v1",
                "payload_hash": hashlib.sha256(canonicalize(raw)).hexdigest(),
            }
            token = sign(
                claims, key, {"alg": "EdDSA", "typ": SERVICE_TYP, "kid": "fixture_key"}
            )
            response = await client.post(
                path, content=raw, headers={"Authorization": "Bearer " + token}
            )
            assert response.status_code == status
            if status == 200:
                assert response.json() == item.metadata()
                assert set(response.json()) == {
                    "tenant_id",
                    "workspace_id",
                    "object_id",
                    "version",
                    "resource",
                    "resource_revision",
                }


@pytest.mark.asyncio
async def test_unconfigured_object_store_is_503_not_fake_missing(tmp_path):
    fixture = object_fixture(tmp_path)
    fixture[1].object_store = None
    response = await request(fixture)
    assert response.status_code == 503
    assert response.headers["cache-control"] == "private, no-store"


@pytest.mark.asyncio
async def test_old_signed_request_cannot_replay_at_other_version_or_tenant(tmp_path):
    app, _, private, claims, _, body = object_fixture(tmp_path)
    token = sign(claims, private)
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app), base_url="http://test"
    ) as client:
        for path in [
            PATH.replace("tenant_a", "tenant_b"),
            PATH.replace("/versions/1", "/versions/2"),
        ]:
            response = await client.get(
                path, headers={"Authorization": "Bearer " + token}
            )
            assert response.status_code == 401 and body not in response.content
        response = await client.get(PATH)
        assert response.status_code == 401 and body not in response.content


@pytest.mark.asyncio
async def test_report_route_has_independent_fixed_action(tmp_path):
    fixture = object_fixture(tmp_path)
    services, claims = fixture[1], fixture[3]
    item = replace(
        fixture[4], resource_kind="report", resource_id="report_a", category="report"
    )
    services.object_store.item = item
    services.ownership.facts = [
        Ownership("report", "report_a", "tenant_a", "workspace_a", ("farm_a",), 1)
    ]
    claims["resource_scope"] = [{"kind": "report", "id": "report_a"}]
    claims["resource_versions"] = [
        {"resource": claims["resource_scope"][0], "revision": 1}
    ]
    path = PATH.replace("assets/", "reports/")
    assert (await request(fixture, path=path)).status_code == 401
    claims["scopes"] = ["agri.report.read"]
    response = await request(fixture, path=path)
    assert response.status_code == 200 and response.content == fixture[-1]


@pytest.mark.asyncio
async def test_signed_foreign_path_scope_denied_before_any_metadata_read(tmp_path):
    fixture = object_fixture(tmp_path)
    store = fixture[1].object_store
    path = PATH.replace("tenant_a", "tenant_foreign")
    for item in [None, replace(fixture[4], tenant_id="tenant_foreign")]:
        store.item = item
        response = await request(fixture, path=path)
        assert response.status_code == 403
        assert response.json() == {"code": "request_scope_mismatch"}
    assert store.calls == 0


def test_cross_language_fixture_configuration_requires_private_file(tmp_path):
    from object_http_fixture import private_config

    path = tmp_path / "config.json"
    value = dict.fromkeys(
        [
            "owner_dsn",
            "app_dsn",
            "tenant",
            "workspace",
            "platform",
            "platform_keys",
            "callback_key",
        ],
        None,
    )
    path.write_text(json.dumps(value))
    path.chmod(0o600)
    assert private_config(path) == value
    path.chmod(0o644)
    with pytest.raises(ValueError, match="private fixture"):
        private_config(path)
    path.chmod(0o600)
    link = tmp_path / "link.json"
    link.symlink_to(path)
    with pytest.raises(ValueError, match="private fixture"):
        private_config(link)
    path.write_text(json.dumps(value | {"object_key": "not_allowed"}))
    with pytest.raises(ValueError, match="invalid fixture"):
        private_config(path)
