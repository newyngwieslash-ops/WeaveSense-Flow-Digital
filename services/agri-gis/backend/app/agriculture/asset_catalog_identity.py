"""Separate asset catalog profile; never relax ordinary farm-bound delegation."""

import hashlib
import json
import re
from datetime import UTC, datetime, timedelta
from uuid import uuid4

from .asset_catalog_rules import ASSET_ID
from .identity.callback import PlatformAuthority, encode
from .identity.canonical import canonicalize
from .identity.keys import utc_second
from .identity.verify import (
    ID,
    OPAQUE_ID,
    POLICY_VERSION,
    SERVICE_TYP,
    IdentityError,
    Verified,
    integer,
    require,
    validate_path,
)

ASSET_TYP = "weavesense-agri-asset-operation+jws"
CALLBACK_PATH = "/internal/agri/v1/assets/authority/check"
ZERO_HASH = "0" * 64
ASSET_PATHS = {
    "asset.context": "/internal/agri/v1/assets/context",
    "asset.preview.create": "/internal/agri/v1/assets/preview-create",
    "asset.preview.version": "/internal/agri/v1/assets/preview-version",
    "asset.create": "/internal/agri/v1/assets/commands/create",
    "asset.version": "/internal/agri/v1/assets/commands/version",
    "asset.receipt": "/internal/agri/v1/assets/commands/receipt",
    "asset.list": "/internal/agri/v1/assets/list",
    "asset.read": "/internal/agri/v1/assets/read",
}


def verify_asset_workspace(
    verifier, token, *, typ, method, path, query, raw_body, now, action
):
    validate_path(path, query)
    require(typ == ASSET_TYP and method == "POST" and ASSET_PATHS.get(action) == path)
    c, key = verifier.decode_signed(token, typ=typ, now=now)
    fields = {
        "schema_version",
        "issuer",
        "subject",
        "audience",
        "actor",
        "tenant_id",
        "workspace_id",
        "action",
        "authorization_version",
        "policy_version",
        "operation_id",
        "request_hash",
        "grant_plan_hash",
        "asset_ids",
        "issued_at",
        "expires_at",
        "jti",
        "trace_id",
        "request_id",
        "request",
    }
    require(isinstance(c, dict) and set(c) == fields)
    require(
        c["schema_version"] == "1.0"
        and c["issuer"] == c["subject"] == "flow-platform"
        and c["audience"] == "agri-gis"
        and c["action"] == action
        and c["policy_version"] == POLICY_VERSION
        and integer(c["authorization_version"])
    )
    for name in (
        "tenant_id",
        "workspace_id",
        "operation_id",
        "jti",
        "trace_id",
        "request_id",
    ):
        require(isinstance(c[name], str) and ID.fullmatch(c[name]) is not None)
    require(
        isinstance(c["actor"], dict)
        and set(c["actor"]) == {"kind", "id"}
        and c["actor"]["kind"] == "user"
        and isinstance(c["actor"]["id"], str)
        and OPAQUE_ID.fullmatch(c["actor"]["id"]) is not None
    )
    for name in ("request_hash", "grant_plan_hash"):
        require(isinstance(c[name], str) and re.fullmatch(r"[0-9a-f]{64}", c[name]))
    ids = c["asset_ids"]
    require(
        isinstance(ids, list)
        and len(ids) <= 200
        and all(isinstance(x, str) and ASSET_ID.fullmatch(x) for x in ids)
        and ids == sorted(set(ids))
    )
    if action in {
        "asset.context",
        "asset.create",
        "asset.preview.create",
        "asset.receipt",
    }:
        require(ids == [])
    else:
        require(len(ids) >= 1 and (action == "asset.list" or len(ids) == 1))
    if action in {"asset.context", "asset.list", "asset.read"}:
        require(c["request_hash"] == c["grant_plan_hash"] == ZERO_HASH)
    issued, expires = utc_second(c["issued_at"]), utc_second(c["expires_at"])
    require(
        0 < (expires - issued).total_seconds() <= 30
        and (issued - now).total_seconds() <= 5
        and now < expires
        and key.not_before <= issued < key.expires_at
        and expires <= key.expires_at
    )
    request = c["request"]
    require(
        isinstance(request, dict)
        and set(request)
        == {
            "method",
            "path",
            "canonicalization_version",
            "payload_hash",
            "operation_id",
        }
        and request["method"] == method
        and request["path"] == path
        and request["operation_id"] == c["operation_id"]
        and request["canonicalization_version"] == "rfc8785-safe-v1"
    )
    raw = canonicalize(raw_body)
    require(request["payload_hash"] == hashlib.sha256(raw).hexdigest())
    body = json.loads(raw)
    require(isinstance(body, dict))
    if action == "asset.context":
        require(body == {}, "request_scope_mismatch", 403)
    elif action == "asset.list":
        require(
            set(body) == {"asset_ids", "limit", "after_id"}
            and body["asset_ids"] == ids,
            "request_scope_mismatch",
            403,
        )
    elif action == "asset.read":
        require(
            set(body) == {"asset_id", "version"} and [body["asset_id"]] == ids,
            "request_scope_mismatch",
            403,
        )
    else:
        expected = {"operation_id", "request_hash", "grant_plan_hash"}
        if action != "asset.receipt":
            expected |= {"asset_id", "expected_revision", "metadata"}
        require(set(body) == expected)
        require(
            all(
                body[x] == c[x]
                for x in ("operation_id", "request_hash", "grant_plan_hash")
            ),
            "request_scope_mismatch",
            403,
        )
        if action in {"asset.version", "asset.preview.version"}:
            require([body["asset_id"]] == ids, "request_scope_mismatch", 403)
        elif action != "asset.receipt":
            require(body["asset_id"] is None, "request_scope_mismatch", 403)
    return Verified(c, expires, body)


class AssetCatalogAuthority(PlatformAuthority):
    """Current platform operation/explicit read grants, no ownership recursion."""

    def __init__(self, base_url, key, client):
        super().__init__(base_url, key, client)
        self.url = base_url.rstrip("/") + CALLBACK_PATH

    async def check_asset(self, verified):
        c = verified.claims
        now = datetime.now(UTC).replace(microsecond=0)
        expires = min(
            now + timedelta(seconds=30), self.key.not_after, verified.expires_at
        )
        require(
            not self.key.revoked and self.key.not_before <= now < expires,
            "identity_dependency_unavailable",
            503,
        )
        body = {
            k: c[k]
            for k in (
                "tenant_id",
                "workspace_id",
                "action",
                "authorization_version",
                "operation_id",
                "request_hash",
                "grant_plan_hash",
                "asset_ids",
            )
        }
        body["actor_user_id"] = c["actor"]["id"]
        body["request_payload_hash"] = c["request"]["payload_hash"]
        raw = canonicalize(json.dumps(body).encode())
        claims = {
            "schema_version": "1.0",
            "issuer": "agri-gis",
            "subject": "agri-gis",
            "audience": "flow-platform",
            "capability": "asset.operation.check",
            "tenant_id": c["tenant_id"],
            "workspace_id": c["workspace_id"],
            "issued_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "expires_at": expires.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "jti": "jti_" + uuid4().hex,
            "request_id": "req_" + uuid4().hex,
            "trace_id": c["trace_id"],
            "request": {
                "method": "POST",
                "path": CALLBACK_PATH,
                "canonicalization_version": "rfc8785-safe-v1",
                "payload_hash": hashlib.sha256(raw).hexdigest(),
            },
        }
        header = {"alg": "EdDSA", "typ": SERVICE_TYP, "kid": self.key.kid}
        signing = (
            encode(canonicalize(json.dumps(header).encode()))
            + "."
            + encode(canonicalize(json.dumps(claims).encode()))
        )
        token = signing + "." + encode(self.key.private_key.sign(signing.encode()))
        try:
            async with self.client.stream(
                "POST",
                self.url,
                content=raw,
                headers={
                    "Authorization": "Bearer " + token,
                    "Content-Type": "application/json",
                },
                follow_redirects=False,
                timeout=2,
            ) as response:
                if response.status_code in {401, 403, 404, 409}:
                    raise IdentityError("authority_denied", 403)
                require(
                    response.status_code == 200, "identity_dependency_unavailable", 503
                )
                result = bytearray()
                async for chunk in response.aiter_bytes():
                    result.extend(chunk)
                    require(len(result) <= 4096, "identity_dependency_unavailable", 503)
                value = json.loads(canonicalize(bytes(result)))
                require(
                    isinstance(value, dict)
                    and set(value) == {"active", "authorization_version"}
                    and value["active"] is True
                    and type(value["authorization_version"]) is int
                    and value["authorization_version"] == c["authorization_version"],
                    "authority_denied",
                    403,
                )
                require(
                    datetime.now(UTC) < verified.expires_at, "delegation_expired", 401
                )
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("identity_dependency_unavailable", 503) from None
