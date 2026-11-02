"""Real signatures and fixed profile negatives; not platform grant acceptance."""

import copy
import hashlib
import json
from datetime import UTC, datetime

import httpx
import pytest
from test_agriculture_identity import build_fixture, sign

from app.agriculture.asset_catalog_store import AssetCatalogOwnershipFact
from app.agriculture.asset_object_identity import (
    ACTION,
    ASSET_OBJECT_TYP,
    CALLBACK_PATH,
    ROUTE,
    AssetObjectAuthority,
    asset_ownership_hash,
    verify_asset_object,
)
from app.agriculture.identity.callback import CallbackKey
from app.agriculture.identity.verify import BUSINESS_TYP, IdentityError


def fixture(method="GET"):
    _, services, key, base, _, _, _ = build_fixture()
    facts = AssetCatalogOwnershipFact(
        "asset_" + "a" * 32, "tenant_a", "workspace_a", (), 2, "ready"
    )
    c = {
        k: copy.deepcopy(base[k])
        for k in (
            "schema_version",
            "issuer",
            "subject",
            "audience",
            "actor",
            "tenant_id",
            "workspace_id",
            "authorization_version",
            "policy_version",
            "issued_at",
            "expires_at",
            "jti",
            "trace_id",
            "request_id",
        )
    }
    c.update(
        action=ACTION,
        asset_id=facts.asset_id,
        asset_revision=2,
        asset_version=2,
        ownership_hash=asset_ownership_hash(facts),
        object_id="object_a",
        object_version=1,
    )
    c["request"] = {
        "method": method,
        "path": ROUTE.format(
            tenant="tenant_a", workspace="workspace_a", object="object_a", version=1
        ),
    }
    return services.verifier, key, c


def verify(parts, *, typ=ASSET_OBJECT_TYP, method=None, path=None, body=b"", query=b""):
    verifier, key, c = parts
    token = sign(c, key, {"alg": "EdDSA", "kid": "fixture_key", "typ": typ})
    return verify_asset_object(
        verifier,
        token,
        typ=typ,
        method=method or c["request"]["method"],
        path=path or c["request"]["path"],
        query=query,
        raw_body=body,
        now=datetime.now(UTC),
        action=ACTION,
    )


@pytest.mark.parametrize("method", ["GET", "HEAD"])
def test_asset_object_real_signature_and_no_farm_profile(method):
    assert verify(fixture(method)).claims["asset_revision"] == 2


@pytest.mark.parametrize(
    "change",
    [
        lambda c: c.update(action="agri.asset.read"),
        lambda c: c.update(asset_id="asset_a"),
        lambda c: c.update(asset_revision=True),
        lambda c: c.update(asset_version=True),
        lambda c: c.update(asset_version=0),
        lambda c: c.update(asset_version=3),
        lambda c: c.update(object_version=0),
        lambda c: c.update(ownership_hash="wrong"),
        lambda c: c.update(tenant_id="tenant_b"),
        lambda c: c.update(workspace_id="workspace_b"),
        lambda c: c.update(object_id="object_b"),
        lambda c: c.update(farm_ids=[]),
        lambda c: c.update(actor={"kind": "service", "id": "actor_a"}),
        lambda c: c.update(authorization_version=True),
        lambda c: c.update(request={"method": "GET", "path": "/elsewhere"}),
    ],
)
def test_asset_object_rejects_signed_invalid_scope_or_profile(change):
    parts = fixture()
    change(parts[2])
    with pytest.raises(IdentityError):
        verify(parts)


@pytest.mark.parametrize(
    "kwargs",
    [
        {"typ": BUSINESS_TYP},
        {"method": "POST"},
        {"method": "HEAD"},
        {"query": b"tenant_id=tenant_b"},
        {"body": b"{}"},
        {"path": "/internal/agri/v1/assets/read"},
    ],
)
def test_asset_object_rejects_replay_on_other_transport(kwargs):
    with pytest.raises(IdentityError):
        verify(fixture(), **kwargs)


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "status,response,allowed",
    [
        (200, {"active": True, "authorization_version": 7}, True),
        (200, {"active": True, "authorization_version": 8}, False),
        (200, {"active": True, "authorization_version": True}, False),
        (200, {"active": False, "authorization_version": 7}, False),
        (204, {}, False),
        (302, {}, False),
        (403, {}, False),
        (500, {}, False),
    ],
)
async def test_asset_object_callback_signature_current_epoch_contract(
    status, response, allowed
):
    parts = fixture()
    proof = verify(parts)
    record = parts[0].keys.require("fixture_key", datetime.now(UTC))
    seen = []

    def responder(request):
        seen.append(request)
        return httpx.Response(status, json=response)

    async with httpx.AsyncClient(transport=httpx.MockTransport(responder)) as client:
        authority = AssetObjectAuthority(
            "https://platform.invalid",
            CallbackKey(record.kid, parts[1], record.not_before, record.expires_at),
            client,
        )
        if allowed:
            await authority.check_asset_object(proof)
        else:
            with pytest.raises(IdentityError):
                await authority.check_asset_object(proof)
    assert len(seen) == 1 and seen[0].url.path == CALLBACK_PATH
    claims, _ = parts[0].decode_signed(
        seen[0].headers["Authorization"][7:],
        typ="weavesense-agri-service+jws",
        now=datetime.now(UTC),
    )
    assert claims["issuer"] == "agri-gis" and claims["audience"] == "flow-platform"
    assert claims["capability"] == "asset.download.check"
    assert (
        claims["request"]["payload_hash"] == hashlib.sha256(seen[0].content).hexdigest()
    )
    body = json.loads(seen[0].content)
    assert body["asset_id"] == proof.claims["asset_id"]
    assert body["ownership_hash"] == proof.claims["ownership_hash"]
    assert body["actor_user_id"] == proof.claims["actor"]["id"]
    assert "operation_id" not in body
