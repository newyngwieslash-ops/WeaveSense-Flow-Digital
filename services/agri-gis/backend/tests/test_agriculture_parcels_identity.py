"""Cryptographic parcel receipt profile components, not live authorization QA."""

import hashlib
import json
from datetime import UTC, datetime, timedelta

import httpx
import pytest
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from test_agriculture_identity import build_fixture, sign

from app.agriculture.identity.callback import CallbackKey
from app.agriculture.identity.canonical import canonicalize
from app.agriculture.identity.verify import BUSINESS_TYP, IdentityError
from app.agriculture.parcels_identity import (
    CALLBACK_PATH,
    PARCEL_OPERATION_TYP,
    RECEIPT_PATH,
    ParcelAuthority,
)


def fixture():
    _, services, key, base, _, _, _ = build_fixture()
    claims = {
        k: base[k]
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
    claims.update(
        action="parcel.receipt", operation_id="pop_fixture", request_hash="a" * 64
    )
    body = canonicalize(
        json.dumps({k: claims[k] for k in ("operation_id", "request_hash")}).encode()
    )
    claims["request"] = {
        "method": "POST",
        "path": RECEIPT_PATH,
        "operation_id": claims["operation_id"],
        "payload_hash": hashlib.sha256(body).hexdigest(),
        "canonicalization_version": "rfc8785-safe-v1",
    }
    return services.verifier, key, claims, body


def verify(
    parts,
    *,
    typ=PARCEL_OPERATION_TYP,
    path=RECEIPT_PATH,
    action="parcel.receipt",
    signer=None,
    body=None,
):
    verifier, key, claims, raw = parts
    token = sign(
        claims,
        signer or key,
        {"alg": "EdDSA", "typ": PARCEL_OPERATION_TYP, "kid": "fixture_key"},
    )
    return verifier.verify(
        token,
        typ=typ,
        method="POST",
        path=path,
        query=b"",
        raw_body=raw if body is None else body,
        now=datetime.now(UTC),
        action=action,
    )


def test_parcel_receipt_profile_valid_and_exactly_metadata_only():
    parts = fixture()
    proof = verify(parts)
    assert proof.body == {"operation_id": "pop_fixture", "request_hash": "a" * 64}
    assert "resource_scope" not in proof.claims and "farm_ids" not in proof.claims


@pytest.mark.parametrize(
    "mutation",
    (
        "extra",
        "empty_business_scope",
        "actor",
        "tenant",
        "workspace",
        "audience",
        "issuer",
        "policy",
        "expired",
        "long_lifetime",
        "operation",
        "hash",
        "body_hash",
        "path",
        "method",
        "epoch_boolean",
        "bad_signature",
        "other_typ",
        "content_path",
        "write_action",
        "altered_body",
    ),
)
def test_parcel_receipt_profile_rejects_mutations(mutation):
    parts = fixture()
    c, options = parts[2], {}
    if mutation == "extra":
        c["roles"] = ["owner"]
    elif mutation == "empty_business_scope":
        c["resource_scope"] = []
    elif mutation == "actor":
        c["actor"] = {"kind": "service", "id": "root"}
    elif mutation in {"tenant", "workspace"}:
        c[mutation + "_id"] = "one,two"
    elif mutation == "audience":
        c["audience"] = "flow-platform"
    elif mutation == "issuer":
        c["issuer"] = "agri-gis"
    elif mutation == "policy":
        c["policy_version"] = "unknown"
    elif mutation == "expired":
        c["expires_at"] = (datetime.now(UTC) - timedelta(seconds=1)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
    elif mutation == "long_lifetime":
        c["expires_at"] = (datetime.now(UTC) + timedelta(seconds=60)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
    elif mutation == "operation":
        c["operation_id"] = "pop_other"
    elif mutation == "hash":
        c["request_hash"] = "b" * 64
    elif mutation == "body_hash":
        c["request"]["payload_hash"] = "b" * 64
    elif mutation == "path":
        c["request"]["path"] += "/unexpected"
    elif mutation == "method":
        c["request"]["method"] = "GET"
    elif mutation == "epoch_boolean":
        c["authorization_version"] = True
    elif mutation == "bad_signature":
        options["signer"] = Ed25519PrivateKey.generate()
    elif mutation == "other_typ":
        options["typ"] = BUSINESS_TYP
    elif mutation == "content_path":
        options["path"] = "/internal/agri/v1/parcels/parcel_fixture/read"
    elif mutation == "write_action":
        options["action"] = "agri.parcel.update"
    elif mutation == "altered_body":
        options["body"] = b"{}"
    with pytest.raises(IdentityError):
        verify(parts, **options)


@pytest.mark.asyncio
async def test_parcel_receipt_callback_actual_signature_component():
    parts = fixture()
    proof = verify(parts)
    record = parts[0].keys.require("fixture_key", datetime.now(UTC))
    requests = []

    def handler(request):
        requests.append(request)
        return httpx.Response(
            200,
            json={
                "active": True,
                "authorization_version": proof.claims["authorization_version"],
            },
        )

    async with httpx.AsyncClient(transport=httpx.MockTransport(handler)) as client:
        authority = ParcelAuthority(
            "https://platform.invalid",
            CallbackKey(record.kid, parts[1], record.not_before, record.expires_at),
            client,
        )
        await authority.check_parcel(proof)
    assert len(requests) == 1 and requests[0].url.path == CALLBACK_PATH
    body = json.loads(requests[0].content)
    assert set(body) == {
        "actor_user_id",
        "tenant_id",
        "workspace_id",
        "authorization_version",
        "operation_id",
        "request_hash",
        "request_payload_hash",
    }
    assert body["request_payload_hash"] == proof.claims["request"]["payload_hash"]
    token = requests[0].headers["Authorization"].removeprefix("Bearer ")
    claims, _ = parts[0].decode_signed(
        token, typ="weavesense-agri-service+jws", now=datetime.now(UTC)
    )
    assert claims["issuer"] == "agri-gis" and claims["audience"] == "flow-platform"
    assert claims["capability"] == "parcel.operation.check"
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
        (200, {"active": True, "authorization_version": 7, "extra": "forbidden"}),
    ],
)
async def test_parcel_receipt_callback_failclosed_component(status, body):
    parts = fixture()
    record = parts[0].keys.require("fixture_key", datetime.now(UTC))
    async with httpx.AsyncClient(
        transport=httpx.MockTransport(lambda _: httpx.Response(status, json=body))
    ) as client:
        authority = ParcelAuthority(
            "https://platform.invalid",
            CallbackKey(record.kid, parts[1], record.not_before, record.expires_at),
            client,
        )
        with pytest.raises(IdentityError):
            await authority.check_parcel(verify(parts))
