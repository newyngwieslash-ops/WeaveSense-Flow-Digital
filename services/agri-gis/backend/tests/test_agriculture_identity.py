"""Cryptographic and HTTP fixtures, explicitly not live farm/GIS evidence."""

import asyncio
import base64
import hashlib
import json
from datetime import UTC, datetime, timedelta

import httpx
import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from fastapi import Depends

from app.agriculture.identity.callback import CallbackKey, PlatformAuthority
from app.agriculture.identity.canonical import canonicalize
from app.agriculture.identity.dependency import IdentityServices, delegation_dependency
from app.agriculture.identity.keys import KeyRing, VerificationKey
from app.agriculture.identity.providers import Ownership
from app.agriculture.identity.verify import (
    BUSINESS_TYP,
    SERVICE_TYP,
    IdentityError,
    Verifier,
)
from app.agriculture.main import create_app


def enc(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def sign(claims, private, header=None):
    header = header or {"alg": "EdDSA", "typ": BUSINESS_TYP, "kid": "fixture_key"}
    value = enc(json.dumps(header).encode()) + "." + enc(json.dumps(claims).encode())
    return value + "." + enc(private.sign(value.encode()))


class FixtureOwnership:
    def __init__(self):
        self.facts = [
            Ownership("farm", "farm_a", "tenant_a", "workspace_a", ("farm_a",), 1)
        ]

    async def resolve(self, tenant_id, workspace_id, resources):
        return self.facts


class FixtureAuthority:
    denied = False

    async def check(
        self, actor_user_id, tenant_id, workspace_id, authorization_version
    ):
        if self.denied or actor_user_id != "123opaque" or authorization_version != 7:
            raise IdentityError("authority_denied", 403)


class FixtureReplay:
    def __init__(self):
        self.seen = set()

    async def claim(self, *, jti, **_kwargs):
        if jti in self.seen:
            raise IdentityError("delegation_replayed", 409)
        self.seen.add(jti)


def build_fixture():
    # This deterministic private seed exists only in tests, never in app config.
    private = Ed25519PrivateKey.from_private_bytes(bytes(range(32)))
    now = datetime.now(UTC).replace(microsecond=0)
    key = VerificationKey(
        "fixture_key",
        private.public_key(),
        now - timedelta(days=1),
        now + timedelta(days=1),
    )
    services = IdentityServices(
        Verifier(KeyRing([key])),
        FixtureOwnership(),
        FixtureAuthority(),
        FixtureReplay(),
    )
    app = create_app(services)
    state = {"crash": False}

    def extract_resources(request, body):
        if request.method in {"GET", "HEAD"}:
            return [{"kind": "farm", "id": "farm_a"}]
        return body["resources"]

    verify = Depends(delegation_dependency("agri.farm.read", extract_resources))

    @app.post("/internal/agri/v1/delegation/check")
    async def probe(_verified=verify):
        if state["crash"]:
            raise RuntimeError("fixture post-claim failure")
        return {"status": "verified"}

    @app.get("/internal/agri/v1/delegation/check")
    async def read_probe(_verified=verify):
        return {"status": "verified"}

    body = {"resources": [{"kind": "farm", "id": "farm_a"}]}
    claims = {
        "schema_version": "1.0",
        "issuer": "flow-platform",
        "subject": "flow-platform",
        "audience": "agri-gis",
        "actor": {"kind": "user", "id": "123opaque"},
        "tenant_id": "tenant_a",
        "workspace_id": "workspace_a",
        "resource_scope": body["resources"],
        "scopes": ["agri.farm.read"],
        "authorization_version": 7,
        "policy_version": "agriculture-policy/1.0.0",
        "farm_ids": ["farm_a"],
        "resource_versions": [{"resource": body["resources"][0], "revision": 1}],
        "issued_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "expires_at": (now + timedelta(seconds=30)).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "jti": "nonce_fixture",
        "trace_id": "trace_fixture",
        "request_id": "req_fixture",
        "request": {
            "method": "POST",
            "path": "/internal/agri/v1/delegation/check",
            "canonicalization_version": "rfc8785-safe-v1",
            "payload_hash": hashlib.sha256(
                canonicalize(json.dumps(body).encode())
            ).hexdigest(),
            "operation_id": "operation_fixture",
        },
    }
    return app, services, private, claims, body, state, key


@pytest.fixture
def fixture():
    return build_fixture()


async def send(
    fixture,
    token=None,
    body=None,
    path="/internal/agri/v1/delegation/check",
    method="POST",
):
    app, _, private, claims, default_body, _, _ = fixture
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app, raise_app_exceptions=False),
        base_url="http://fixture",
    ) as client:
        return await client.request(
            method,
            path,
            content=json.dumps(default_body if body is None else body).encode()
            if method not in {"GET", "HEAD"}
            else b"",
            headers={
                "Authorization": "Bearer " + (token or sign(claims, private)),
                "Content-Type": "application/json",
            },
        )


@pytest.mark.asyncio
async def test_valid_signature_raw_numeric_first_actor_and_replay(fixture):
    assert (await send(fixture)).status_code == 200
    response = await send(fixture)
    assert response.status_code == 409 and response.json() == {
        "code": "delegation_replayed"
    }


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "field,value",
    [
        ("issuer", "client"),
        ("subject", "browser"),
        ("audience", "flow-platform"),
        ("authorization_version", True),
        ("policy_version", "old/1.0.0"),
        ("scopes", ["agri.farm.update"]),
        ("actor", {"kind": "service", "id": "123opaque"}),
        ("farm_ids", ["farm_a", "farm_other"]),
        (
            "resource_versions",
            [{"resource": {"kind": "farm", "id": "farm_a"}, "revision": 2}],
        ),
    ],
)
async def test_signed_but_invalid_or_stale_claims(fixture, field, value):
    fixture[3][field] = value
    assert (await send(fixture)).status_code in {401, 403, 404}
    assert not fixture[1].replay.seen


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "header",
    [
        {"alg": "none", "typ": BUSINESS_TYP, "kid": "fixture_key"},
        {"alg": "HS256", "typ": BUSINESS_TYP, "kid": "fixture_key"},
        {"alg": "EdDSA", "typ": SERVICE_TYP, "kid": "fixture_key"},
        {"alg": "EdDSA", "typ": BUSINESS_TYP, "kid": "unknown"},
        {
            "alg": "EdDSA",
            "typ": BUSINESS_TYP,
            "kid": "fixture_key",
            "jku": "https://invalid.example/keys",
        },
    ],
)
async def test_algorithm_type_and_untrusted_key_headers(fixture, header):
    assert (
        await send(fixture, sign(fixture[3], fixture[2], header))
    ).status_code == 401


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "change",
    ["expired", "future", "long_ttl", "revoked", "wrong_signature", "query", "body"],
)
async def test_request_time_key_binding_failures(fixture, change):
    app, services, private, claims, body, _, key = fixture
    now = datetime.now(UTC).replace(microsecond=0)
    if change == "expired":
        claims["expires_at"] = (now - timedelta(seconds=1)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
    if change == "future":
        claims["issued_at"] = (now + timedelta(seconds=6)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
    if change == "long_ttl":
        claims["expires_at"] = (now + timedelta(seconds=31)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
    if change == "revoked":
        services.verifier = Verifier(
            KeyRing(
                [
                    VerificationKey(
                        key.kid, key.public_key, key.not_before, key.expires_at, True
                    )
                ]
            )
        )
    token = sign(
        claims, Ed25519PrivateKey.generate() if change == "wrong_signature" else private
    )
    response = await send(
        fixture,
        token,
        body={"resources": []} if change == "body" else body,
        path="/internal/agri/v1/delegation/check?tenant_id=tenant_a"
        if change == "query"
        else "/internal/agri/v1/delegation/check",
    )
    assert response.status_code == 401
    assert not services.replay.seen


@pytest.mark.asyncio
async def test_authority_and_domain_revalidation_precede_nonce(fixture):
    services = fixture[1]
    services.authority.denied = True
    assert (await send(fixture)).status_code == 403
    services.authority.denied = False
    services.ownership.facts = [
        Ownership("farm", "farm_a", "foreign", "workspace_a", ("farm_a",), 1)
    ]
    assert (await send(fixture)).status_code == 404
    assert not services.replay.seen
    services.ownership.facts = [
        Ownership("farm", "farm_a", "tenant_a", "workspace_a", ("farm_a",), 1)
    ]
    assert (await send(fixture)).status_code == 200


@pytest.mark.asyncio
@pytest.mark.parametrize("missing", ["ownership", "authority", "replay"])
async def test_missing_dependency_fails_closed(fixture, missing):
    setattr(fixture[1], missing, None)
    assert (await send(fixture)).status_code == 503


@pytest.mark.asyncio
async def test_claim_before_handler_failure_is_not_rolled_back(fixture):
    fixture[5]["crash"] = True
    assert (await send(fixture)).status_code == 500
    fixture[5]["crash"] = False
    assert (await send(fixture)).status_code == 409


@pytest.mark.asyncio
async def test_get_empty_body_does_not_claim_write_nonce(fixture):
    fixture[3]["request"] = {
        "method": "GET",
        "path": "/internal/agri/v1/delegation/check",
    }
    assert (await send(fixture, method="GET")).status_code == 200
    assert (await send(fixture, method="GET")).status_code == 200
    assert not fixture[1].replay.seen


@pytest.mark.asyncio
async def test_production_profile_has_no_probe_or_legacy_routes():
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=create_app()), base_url="http://fixture"
    ) as client:
        assert (
            await client.get("/internal/agri/v1/delegation/check")
        ).status_code == 404
        assert (await client.get("/api/v1/plots")).status_code == 404
        assert (await client.get("/health")).json()[
            "agriculture_content_ready"
        ] is False


@pytest.mark.asyncio
async def test_service_metadata_credential_cannot_be_business_ticket(fixture):
    c = fixture[3]
    for name in (
        "actor",
        "resource_scope",
        "scopes",
        "authorization_version",
        "policy_version",
        "farm_ids",
        "resource_versions",
    ):
        del c[name]
    c["capability"] = "ownership.resolve"
    body = {
        "tenant_id": "tenant_a",
        "workspace_id": "workspace_a",
        "resources": fixture[4]["resources"],
    }
    c["request"] = {
        "method": "POST",
        "path": "/internal/agri/v1/ownership/resolve",
        "canonicalization_version": "rfc8785-safe-v1",
        "payload_hash": hashlib.sha256(
            canonicalize(json.dumps(body).encode())
        ).hexdigest(),
    }
    token = sign(
        c, fixture[2], {"alg": "EdDSA", "typ": SERVICE_TYP, "kid": "fixture_key"}
    )
    response = await send(fixture, token, body, path=c["request"]["path"])
    assert response.status_code == 200 and set(response.json()) == {"resources"}
    assert not fixture[1].replay.seen
    assert (await send(fixture, token)).status_code == 401


@pytest.mark.asyncio
async def test_key_rotation_overlap_and_explicit_revocation(fixture):
    _, services, old_private, claims, _, _, old = fixture
    new_private = Ed25519PrivateKey.generate()
    new = VerificationKey(
        "rotation_new", new_private.public_key(), old.not_before, old.expires_at
    )
    services.verifier = Verifier(KeyRing([old, new]))
    claims["request"] = {"method": "GET", "path": "/internal/agri/v1/delegation/check"}
    assert (
        await send(fixture, sign(claims, old_private), method="GET")
    ).status_code == 200
    header = {"alg": "EdDSA", "typ": BUSINESS_TYP, "kid": "rotation_new"}
    assert (
        await send(fixture, sign(claims, new_private, header), method="GET")
    ).status_code == 200
    services.verifier = Verifier(
        KeyRing(
            [
                VerificationKey(
                    old.kid, old.public_key, old.not_before, old.expires_at, True
                ),
                new,
            ]
        )
    )
    assert (
        await send(fixture, sign(claims, old_private), method="GET")
    ).status_code == 401
    assert (
        await send(fixture, sign(claims, new_private, header), method="GET")
    ).status_code == 200


@pytest.mark.asyncio
@pytest.mark.parametrize("part", ["header", "claims"])
async def test_duplicate_json_fields_even_with_valid_signature_rejected(fixture, part):
    raw_header = json.dumps({"alg": "EdDSA", "typ": BUSINESS_TYP, "kid": "fixture_key"})
    raw_claims = json.dumps(fixture[3])
    if part == "header":
        raw_header = raw_header[:-1] + ',"alg":"EdDSA"}'
    else:
        raw_claims = raw_claims[:-1] + ',"issuer":"flow-platform"}'
    content = enc(raw_header.encode()) + "." + enc(raw_claims.encode())
    token = content + "." + enc(fixture[2].sign(content.encode()))
    assert (await send(fixture, token)).status_code == 401


@pytest.mark.asyncio
async def test_decoded_claim_limit_is_enforced_without_truncation(fixture):
    fixture[3]["padding"] = "x" * 131072
    assert (await send(fixture)).status_code == 401
    assert not fixture[1].replay.seen


@pytest.mark.asyncio
async def test_expiry_during_claim_never_enters_handler_or_releases_nonce(fixture):
    original = fixture[1].replay
    entered = []

    class DelayedClaim:
        async def claim(self, **kwargs):
            await original.claim(**kwargs)
            entered.append(True)
            await asyncio.sleep(2.1)

    fixture[1].replay = DelayedClaim()
    fixture[3]["expires_at"] = (
        datetime.now(UTC).replace(microsecond=0) + timedelta(seconds=2)
    ).strftime("%Y-%m-%dT%H:%M:%SZ")
    response = await send(fixture)
    assert response.status_code == 401 and response.json() == {
        "code": "delegation_expired"
    }
    assert entered and fixture[3]["jti"] in original.seen


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "origin,allowed",
    [
        ("http://127.0.0.1:1234", True),
        ("http://[::1]:1234", True),
        ("http://localhost:1234", False),
        ("http://10.0.0.1:1234", False),
        ("http://[::1%25eth0]:1234", False),
        ("https://platform.example", True),
    ],
)
async def test_callback_origin_requires_tls_or_literal_loopback(
    fixture, origin, allowed
):
    key = CallbackKey(
        "fixture_key", fixture[2], fixture[6].not_before, fixture[6].expires_at
    )
    async with httpx.AsyncClient(trust_env=False) as client:
        if allowed:
            PlatformAuthority(origin, key, client)
        else:
            with pytest.raises(ValueError):
                PlatformAuthority(origin, key, client)


def test_route_extractor_is_mandatory_configuration():
    with pytest.raises(TypeError):
        delegation_dependency("agri.farm.read")
    with pytest.raises(ValueError):
        delegation_dependency("agri.farm.read", None)


@pytest.mark.asyncio
async def test_signed_actual_path_cannot_authorize_a_different_resource(fixture):
    dependency = Depends(
        delegation_dependency(
            "agri.parcel.read",
            lambda request, _body: [
                {"kind": "parcel", "id": request.path_params["parcel_id"]}
            ],
        )
    )

    @fixture[0].get("/internal/agri/v1/parcels/{parcel_id}")
    async def read_parcel(_verified=dependency):
        raise AssertionError("scope mismatch must never enter handler")

    claims = fixture[3]
    claims["request"] = {"method": "GET", "path": "/internal/agri/v1/parcels/parcel_b"}
    claims["scopes"] = ["agri.parcel.read"]
    claims["resource_scope"] = [{"kind": "parcel", "id": "parcel_a"}]
    claims["resource_versions"] = [
        {"resource": claims["resource_scope"][0], "revision": 1}
    ]
    response = await send(fixture, path=claims["request"]["path"], method="GET")
    assert response.status_code == 403
    assert response.json() == {"code": "request_scope_mismatch"}
    assert not fixture[1].replay.seen


@pytest.mark.asyncio
async def test_custom_payload_extractor_checks_complete_batch(fixture):
    async def extract_batch(_request, body):
        return [body["primary"], *body["related"]]

    dependency = Depends(delegation_dependency("agri.farm.read", extract_batch))

    @fixture[0].post("/internal/agri/v1/batch/check")
    async def batch_probe(_verified=dependency):
        return {"status": "verified"}

    refs = [{"kind": "farm", "id": "farm_a"}, {"kind": "farm", "id": "farm_b"}]
    body = {"primary": refs[1], "related": [refs[0]]}
    claims = fixture[3]
    claims["request"]["path"] = "/internal/agri/v1/batch/check"
    claims["request"]["payload_hash"] = hashlib.sha256(
        canonicalize(json.dumps(body).encode())
    ).hexdigest()
    claims["resource_scope"] = refs
    claims["farm_ids"] = ["farm_a", "farm_b"]
    claims["resource_versions"] = [{"resource": ref, "revision": 1} for ref in refs]
    fixture[1].ownership.facts = [
        Ownership("farm", ref["id"], "tenant_a", "workspace_a", (ref["id"],), 1)
        for ref in refs
    ]
    assert (
        await send(fixture, body=body, path=claims["request"]["path"])
    ).status_code == 200
    claims["jti"] = "nonce_partial_batch"
    claims["resource_scope"] = refs[:1]
    claims["resource_versions"] = claims["resource_versions"][:1]
    claims["farm_ids"] = ["farm_a"]
    response = await send(fixture, body=body, path=claims["request"]["path"])
    assert response.status_code == 403 and response.json() == {
        "code": "request_scope_mismatch"
    }
    assert "nonce_partial_batch" not in fixture[1].replay.seen
