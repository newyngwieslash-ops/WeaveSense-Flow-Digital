"""Closed workspace-operation profile, never an empty business delegation."""

import hashlib
import json
from datetime import UTC, datetime, timedelta
from uuid import uuid4

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

WORKSPACE_TYP = "weavesense-agri-workspace-operation+jws"
WORKSPACE_PATHS = {
    "farm.preview.create": "/internal/agri/v1/farms/preview-create",
    "farm.create": "/internal/agri/v1/farms/commands/create",
    "farm.receipt": "/internal/agri/v1/farms/commands/receipt",
    "farm.context": "/internal/agri/v1/farms/context",
    "farm.list": "/internal/agri/v1/farms/list",
    "farm.history.read": "/internal/agri/v1/farms/history",
}
CALLBACK_PATH = "/internal/agri/v1/farms/authority/check"
ZERO_HASH = "0" * 64


def verify_workspace(
    verifier, token, *, typ, method, path, query, raw_body, now, action
):
    import re

    validate_path(path, query)
    require(
        typ == WORKSPACE_TYP
        and method == "POST"
        and WORKSPACE_PATHS.get(action) == path
    )
    claims, key = verifier.decode_signed(token, typ=typ, now=now)
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
        "farm_ids",
        "issued_at",
        "expires_at",
        "jti",
        "trace_id",
        "request_id",
        "request",
    }
    require(isinstance(claims, dict) and set(claims) == fields)
    require(
        claims["schema_version"] == "1.0"
        and claims["issuer"] == claims["subject"] == "flow-platform"
        and claims["audience"] == "agri-gis"
        and claims["action"] == action
        and claims["policy_version"] == POLICY_VERSION
        and integer(claims["authorization_version"])
    )
    for name in (
        "tenant_id",
        "workspace_id",
        "operation_id",
        "jti",
        "trace_id",
        "request_id",
    ):
        require(
            isinstance(claims[name], str) and ID.fullmatch(claims[name]) is not None
        )
    actor = claims["actor"]
    require(
        isinstance(actor, dict)
        and set(actor) == {"kind", "id"}
        and actor["kind"] == "user"
        and isinstance(actor["id"], str)
        and OPAQUE_ID.fullmatch(actor["id"]) is not None
    )
    for name in ("request_hash", "grant_plan_hash"):
        require(
            isinstance(claims[name], str)
            and re.fullmatch(r"[0-9a-f]{64}", claims[name])
        )
    ids = claims["farm_ids"]
    require(
        isinstance(ids, list)
        and len(ids) <= 200
        and all(
            isinstance(x, str) and re.fullmatch(r"farm_[0-9a-f]{32}", x) for x in ids
        )
        and ids == sorted(set(ids))
    )
    if action in {"farm.list", "farm.history.read"}:
        require(len(ids) >= 1 and (action != "farm.history.read" or len(ids) == 1))
    else:
        require(ids == [])
    if action in {"farm.context", "farm.list", "farm.history.read"}:
        require(claims["request_hash"] == claims["grant_plan_hash"] == ZERO_HASH)
    issued, expires = utc_second(claims["issued_at"]), utc_second(claims["expires_at"])
    require(
        0 < (expires - issued).total_seconds() <= 30
        and (issued - now).total_seconds() <= 5
        and now < expires
    )
    require(key.not_before <= issued < key.expires_at and expires <= key.expires_at)
    request = claims["request"]
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
        and request["operation_id"] == claims["operation_id"]
        and request["canonicalization_version"] == "rfc8785-safe-v1"
    )
    raw = canonicalize(raw_body)
    require(request["payload_hash"] == hashlib.sha256(raw).hexdigest())
    body = json.loads(raw)
    require(isinstance(body, dict))
    if action in {"farm.create", "farm.preview.create", "farm.receipt"}:
        require(
            set(body)
            == (
                {"operation_id", "request_hash", "grant_plan_hash"}
                | ({"farm"} if action != "farm.receipt" else set())
            )
        )
        require(
            all(
                body[x] == claims[x]
                for x in ("operation_id", "request_hash", "grant_plan_hash")
            ),
            "request_scope_mismatch",
            403,
        )
    elif action == "farm.context":
        require(body == {})
    elif action == "farm.list":
        require(
            set(body) == {"farm_ids", "status", "limit", "after_id", "search"}
            and body["farm_ids"] == ids,
            "request_scope_mismatch",
            403,
        )
    else:
        require(
            set(body) == {"farm_id"} and [body["farm_id"]] == ids,
            "request_scope_mismatch",
            403,
        )
    return Verified(claims, expires, body)


class FarmAuthority(PlatformAuthority):
    """Current platform operation/explicit read grants, no ownership recursion."""

    def __init__(self, base_url, key, client):
        super().__init__(base_url, key, client)
        self.url = base_url.rstrip("/") + CALLBACK_PATH

    async def check_farm(self, verified):
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
                "farm_ids",
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
            "capability": "farm.operation.check",
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
