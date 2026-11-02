"""Receipt-only parcel operation assertion, distinct from business access."""

import hashlib
import json
import re
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

PARCEL_OPERATION_TYP = "weavesense-agri-parcel-operation+jws"
RECEIPT_PATH = "/internal/agri/v1/parcels/commands/receipt"
CALLBACK_PATH = "/internal/agri/v1/parcels/authority/check"


def verify_parcel_operation(
    verifier, token, *, typ, method, path, query, raw_body, now, action
):
    validate_path(path, query)
    require(
        typ == PARCEL_OPERATION_TYP
        and method == "POST"
        and path == RECEIPT_PATH
        and action == "parcel.receipt"
    )
    c, key = verifier.decode_signed(token, typ=typ, now=now)
    require(
        isinstance(c, dict)
        and set(c)
        == {
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
            "issued_at",
            "expires_at",
            "jti",
            "trace_id",
            "request_id",
            "request",
        }
    )
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
        require(isinstance(c[name], str) and ID.fullmatch(c[name]))
    actor = c["actor"]
    require(
        isinstance(actor, dict)
        and set(actor) == {"kind", "id"}
        and actor["kind"] == "user"
        and isinstance(actor["id"], str)
        and OPAQUE_ID.fullmatch(actor["id"])
    )
    require(
        isinstance(c["request_hash"], str)
        and re.fullmatch(r"[0-9a-f]{64}", c["request_hash"])
    )
    issued, expires = utc_second(c["issued_at"]), utc_second(c["expires_at"])
    require(
        0 < (expires - issued).total_seconds() <= 30
        and (issued - now).total_seconds() <= 5
        and now < expires
    )
    require(key.not_before <= issued < key.expires_at and expires <= key.expires_at)
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
    require(
        isinstance(body, dict)
        and set(body) == {"operation_id", "request_hash"}
        and all(body[k] == c[k] for k in body),
        "request_scope_mismatch",
        403,
    )
    return Verified(c, expires, body)


class ParcelAuthority(PlatformAuthority):
    def __init__(self, base_url, key, client):
        super().__init__(base_url, key, client)
        self.url = base_url.rstrip("/") + CALLBACK_PATH

    async def check_parcel(self, verified):
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
                "authorization_version",
                "operation_id",
                "request_hash",
            )
        }
        body.update(
            actor_user_id=c["actor"]["id"],
            request_payload_hash=c["request"]["payload_hash"],
        )
        raw = canonicalize(json.dumps(body).encode())
        claims = {
            "schema_version": "1.0",
            "issuer": "agri-gis",
            "subject": "agri-gis",
            "audience": "flow-platform",
            "capability": "parcel.operation.check",
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
