"""Cryptographic farm profile components; not dual-PG authorization evidence."""

import copy
import hashlib
import json
from datetime import UTC, datetime, timedelta

import httpx
import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from test_agriculture_identity import build_fixture, sign

from app.agriculture.farms_identity import (
    CALLBACK_PATH,
    WORKSPACE_PATHS,
    WORKSPACE_TYP,
    FarmAuthority,
)
from app.agriculture.identity.callback import CallbackKey
from app.agriculture.identity.canonical import canonicalize
from app.agriculture.identity.verify import BUSINESS_TYP, IdentityError
from app.agriculture.main import create_app


def workspace_fixture(action="farm.create"):
    _, services, key, base, _, _, verification_key = build_fixture()
    claims = {
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
    zero = "0" * 64
    farm = "farm_" + "a" * 32
    claims.update(
        action=action,
        operation_id="fop_fixture",
        request_hash="a" * 64,
        grant_plan_hash="b" * 64,
        farm_ids=[],
    )
    if action in {"farm.create", "farm.preview.create", "farm.receipt"}:
        body = {
            k: claims[k] for k in ("operation_id", "request_hash", "grant_plan_hash")
        }
        if action != "farm.receipt":
            body["farm"] = {"name": "crypto-only fixture"}
    else:
        claims.update(request_hash=zero, grant_plan_hash=zero)
        body = {}
        if action == "farm.list":
            claims["farm_ids"] = [farm]
            body = {
                "farm_ids": [farm],
                "status": "active",
                "limit": 50,
                "after_id": "",
                "search": "",
            }
        elif action == "farm.history.read":
            claims["farm_ids"] = [farm]
            body = {"farm_id": farm}
    raw = canonicalize(json.dumps(body).encode())
    claims["request"] = {
        "method": "POST",
        "path": WORKSPACE_PATHS[action],
        "operation_id": claims["operation_id"],
        "canonicalization_version": "rfc8785-safe-v1",
        "payload_hash": hashlib.sha256(raw).hexdigest(),
    }
    return services, key, claims, raw, verification_key


def verify_fixture(
    fixture, *, signer=None, header=None, typ=WORKSPACE_TYP, path=None, raw=None
):
    services, key, claims, body, _ = fixture
    token = sign(
        claims,
        signer or key,
        header or {"alg": "EdDSA", "typ": WORKSPACE_TYP, "kid": "fixture_key"},
    )
    return services.verifier.verify(
        token,
        typ=typ,
        method="POST",
        path=path or claims["request"]["path"],
        query=b"",
        raw_body=body if raw is None else raw,
        now=datetime.now(UTC),
        action=claims["action"],
    )


@pytest.mark.parametrize("action", WORKSPACE_PATHS)
def test_farm_workspace_profile_exact_fixed_action_control(action):
    fixture = workspace_fixture(action)
    proof = verify_fixture(fixture)
    assert proof.body == json.loads(fixture[3])


@pytest.mark.parametrize(
    "mutation",
    (
        "extra",
        "actor",
        "tenant",
        "audience",
        "policy",
        "expired",
        "operation",
        "request_hash",
        "grant_hash",
        "body_hash",
        "path",
        "method",
        "farm_ids",
        "version_bool",
    ),
)
def test_farm_workspace_profile_rejects_mutations(mutation):
    fixture = workspace_fixture()
    c = fixture[2]
    if mutation == "extra":
        c["roles"] = ["owner"]
    elif mutation == "actor":
        c["actor"] = {"kind": "service", "id": "root"}
    elif mutation == "tenant":
        c["tenant_id"] = "tenant_a,tenant_b"
    elif mutation == "audience":
        c["audience"] = "flow-platform"
    elif mutation == "policy":
        c["policy_version"] = "unknown"
    elif mutation == "expired":
        c["expires_at"] = (datetime.now(UTC) - timedelta(seconds=1)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
    elif mutation == "operation":
        c["operation_id"] = "other"
    elif mutation == "request_hash":
        c["request_hash"] = "c" * 64
    elif mutation == "grant_hash":
        c["grant_plan_hash"] = "c" * 64
    elif mutation == "body_hash":
        c["request"]["payload_hash"] = "c" * 64
    elif mutation == "path":
        c["request"]["path"] = "/internal/agri/v1/farms/list"
    elif mutation == "method":
        c["request"]["method"] = "PATCH"
    elif mutation == "farm_ids":
        c["farm_ids"] = ["farm_" + "a" * 32]
    elif mutation == "version_bool":
        c["authorization_version"] = True
    with pytest.raises(IdentityError):
        verify_fixture(fixture)


@pytest.mark.parametrize("action", ("farm.list", "farm.history.read"))
def test_farm_read_profile_never_accepts_empty_or_extra_farm_set(action):
    fixture = workspace_fixture(action)
    for ids in ([], ["farm_" + "b" * 32], ["farm_" + "a" * 32] * 2):
        fixture[2]["farm_ids"] = ids
        with pytest.raises(IdentityError):
            verify_fixture(fixture)


def test_farm_workspace_and_business_profiles_cannot_be_substituted():
    fixture = workspace_fixture()
    for kwargs in (
        {"typ": BUSINESS_TYP},
        {"header": {"alg": "EdDSA", "typ": BUSINESS_TYP, "kid": "fixture_key"}},
        {"signer": Ed25519PrivateKey.generate()},
        {"path": "/internal/agri/v1/ownership/resolve"},
        {"raw": b'{"operator_code":"admin"}'},
    ):
        with pytest.raises(IdentityError):
            verify_fixture(fixture, **kwargs)


@pytest.mark.asyncio
async def test_farm_callback_signed_body_and_current_epoch_component():
    fixture = workspace_fixture()
    proof = verify_fixture(fixture)
    record = fixture[-1]
    requests = []

    def responder(request):
        requests.append(request)
        return httpx.Response(200, json={"active": True, "authorization_version": 7})

    async with httpx.AsyncClient(transport=httpx.MockTransport(responder)) as client:
        authority = FarmAuthority(
            "https://platform.invalid",
            CallbackKey(record.kid, fixture[1], record.not_before, record.expires_at),
            client,
        )
        await authority.check_farm(proof)
    assert len(requests) == 1 and requests[0].url.path == CALLBACK_PATH
    body = json.loads(requests[0].content)
    assert body["request_payload_hash"] == proof.claims["request"]["payload_hash"]
    assert body["actor_user_id"] == proof.claims["actor"]["id"]
    assert "farm" not in body and "contact" not in body
    token = requests[0].headers["Authorization"].removeprefix("Bearer ")
    claims, _ = fixture[0].verifier.decode_signed(
        token, typ="weavesense-agri-service+jws", now=datetime.now(UTC)
    )
    assert claims["issuer"] == "agri-gis" and claims["audience"] == "flow-platform"
    assert claims["capability"] == "farm.operation.check"
    assert (
        claims["request"]["payload_hash"]
        == hashlib.sha256(requests[0].content).hexdigest()
    )


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "status,body",
    [
        (403, {}),
        (302, {}),
        (200, {"active": True, "authorization_version": 8}),
        (200, {"active": True, "authorization_version": True}),
        (200, {"active": True, "authorization_version": 7, "secret": "extra"}),
    ],
)
async def test_farm_callback_fail_closed_component(status, body):
    fixture = workspace_fixture()
    record = fixture[-1]
    async with httpx.AsyncClient(
        transport=httpx.MockTransport(lambda _: httpx.Response(status, json=body))
    ) as client:
        authority = FarmAuthority(
            "https://platform.invalid",
            CallbackKey(record.kid, fixture[1], record.not_before, record.expires_at),
            client,
        )
        with pytest.raises(IdentityError):
            await authority.check_farm(verify_fixture(fixture))


@pytest.mark.asyncio
async def test_farm_all_new_routes_unsigned_fail_closed_without_providers():
    app = create_app()
    paths = [("POST", p) for p in WORKSPACE_PATHS.values()]
    farm = "/internal/agri/v1/farms/farm_" + "a" * 32
    paths += [
        ("GET", farm),
        ("PATCH", farm),
        ("POST", farm + "/preview-update"),
        ("POST", farm + "/preview-archive"),
        ("POST", farm + "/archive"),
    ]
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app), base_url="http://fixture"
    ) as client:
        for method, path in paths:
            result = await client.request(method, path, json={})
            assert result.status_code == 401
            assert result.json() == {"error": {"code": "invalid_delegation"}}
            assert "no-store" in result.headers["cache-control"]
