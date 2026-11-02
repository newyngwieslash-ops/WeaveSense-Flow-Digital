"""Actual Ed25519 profile component tests; current authority needs live HTTP tests."""

import copy
import hashlib
import json
from datetime import UTC, datetime

import pytest
from test_agriculture_identity import build_fixture, sign

from app.agriculture.asset_catalog_identity import (
    ASSET_PATHS,
    ASSET_TYP,
    verify_asset_workspace,
)
from app.agriculture.identity.canonical import canonicalize
from app.agriculture.identity.verify import BUSINESS_TYP, IdentityError


def fixture(action="asset.create"):
    _, services, key, base, _, _, _ = build_fixture()
    names = {
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
    }
    claims = {k: copy.deepcopy(base[k]) for k in names}
    identifier = "asset_" + "a" * 32
    claims.update(
        action=action,
        operation_id="aop_fixture",
        request_hash="a" * 64,
        grant_plan_hash="b" * 64,
        asset_ids=[],
    )
    if action == "asset.context":
        claims.update(request_hash="0" * 64, grant_plan_hash="0" * 64)
        body = {}
    elif action in {"asset.list", "asset.read"}:
        claims.update(
            request_hash="0" * 64, grant_plan_hash="0" * 64, asset_ids=[identifier]
        )
        body = (
            {"asset_ids": [identifier], "limit": 50, "after_id": ""}
            if action == "asset.list"
            else {"asset_id": identifier, "version": None}
        )
    else:
        body = {
            k: claims[k] for k in ("operation_id", "request_hash", "grant_plan_hash")
        }
        if action != "asset.receipt":
            version = action in {"asset.version", "asset.preview.version"}
            claims["asset_ids"] = [identifier] if version else []
            body.update(
                asset_id=identifier if version else None,
                expected_revision=1 if version else 0,
                metadata={"name": "crypto fixture only"},
            )
    raw = canonicalize(json.dumps(body).encode())
    claims["request"] = {
        "method": "POST",
        "path": ASSET_PATHS[action],
        "operation_id": claims["operation_id"],
        "canonicalization_version": "rfc8785-safe-v1",
        "payload_hash": hashlib.sha256(raw).hexdigest(),
    }
    return services.verifier, key, claims, raw


def verify(value, *, typ=ASSET_TYP, path=None, body=None):
    verifier, key, claims, raw = value
    token = sign(claims, key, {"alg": "EdDSA", "typ": typ, "kid": "fixture_key"})
    return verify_asset_workspace(
        verifier,
        token,
        typ=typ,
        method="POST",
        path=path or claims["request"]["path"],
        query=b"",
        raw_body=raw if body is None else body,
        now=datetime.now(UTC),
        action=claims["action"],
    )


@pytest.mark.parametrize("action", ASSET_PATHS)
def test_catalog_signed_profile_exact_action_body(action):
    value = fixture(action)
    assert verify(value).body == json.loads(value[3])


@pytest.mark.parametrize("action", ASSET_PATHS)
def test_catalog_profile_cannot_be_ordinary_empty_farm_business_scope(action):
    with pytest.raises(IdentityError):
        verify(fixture(action), typ=BUSINESS_TYP)


@pytest.mark.parametrize(
    "mutation",
    [
        "extra",
        "actor",
        "audience",
        "policy",
        "operation",
        "hash",
        "ids",
        "request_path",
        "expired",
        "role",
    ],
)
def test_catalog_invalid_cryptographically_signed_claims_denied(mutation):
    value = fixture()
    c = value[2]
    if mutation == "extra":
        c["unknown"] = True
    elif mutation == "actor":
        c["actor"]["kind"] = "service"
    elif mutation == "audience":
        c["audience"] = "flow-platform"
    elif mutation == "policy":
        c["policy_version"] = "unknown"
    elif mutation == "operation":
        c["operation_id"] = "different"
    elif mutation == "hash":
        c["request_hash"] = "0" * 64
    elif mutation == "ids":
        c["asset_ids"] = ["asset_" + "b" * 32]
    elif mutation == "request_path":
        c["request"]["path"] = ASSET_PATHS["asset.version"]
    elif mutation == "expired":
        c["expires_at"] = "2000-01-01T00:00:00Z"
    else:
        c["actor"]["role"] = "owner"
    with pytest.raises(IdentityError):
        verify(value)


@pytest.mark.parametrize(
    "action", ["asset.read", "asset.list", "asset.version", "asset.preview.version"]
)
def test_catalog_content_profiles_require_nonempty_exact_asset_ids(action):
    value = fixture(action)
    value[2]["asset_ids"] = []
    with pytest.raises(IdentityError):
        verify(value)


def test_catalog_profile_body_changes_and_cross_action_path_denied():
    value = fixture("asset.read")
    with pytest.raises(IdentityError):
        verify(value, body=b'{"asset_id":"different","version":null}')
    with pytest.raises(IdentityError):
        verify(value, path=ASSET_PATHS["asset.list"])
