"""Narrow current-authorized asset byte profile; ordinary delegation unchanged."""

import hashlib
import json
import re
from datetime import UTC, datetime, timedelta
from uuid import uuid4

from .asset_catalog_rules import ASSET_ID, FARM_ID
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

ASSET_OBJECT_TYP = "weavesense-agri-asset-object+jws"
ACTION = "agri.asset_download.read"
CALLBACK_PATH = "/internal/agri/v1/assets/download/authority/check"
ROUTE = (
    "/internal/agri/v1/tenants/{tenant}/workspaces/{workspace}"
    "/assets/catalog-objects/{object}/versions/{version}"
)


def asset_ownership_hash(fact):
    require(
        isinstance(fact.asset_id, str)
        and ASSET_ID.fullmatch(fact.asset_id)
        and fact.status == "ready"
        and integer(fact.revision)
        and isinstance(fact.farm_ids, tuple)
        and len(fact.farm_ids) <= 1
        and all(isinstance(x, str) and FARM_ID.fullmatch(x) for x in fact.farm_ids),
        "resource_not_found",
        404,
    )
    # Exact existing Go ownershipBindingHash field order, not a new hash recipe.
    value = {
        "Resource": {"kind": "asset", "id": fact.asset_id},
        "TenantID": fact.tenant_id,
        "WorkspaceID": fact.workspace_id,
        "FarmIDs": list(fact.farm_ids),
        "Revision": fact.revision,
        "Status": fact.status,
        "AssignedActorID": "",
    }
    return hashlib.sha256(
        json.dumps(value, separators=(",", ":"), ensure_ascii=False).encode()
    ).hexdigest()


def verify_asset_object(
    verifier, token, *, typ, method, path, query, raw_body, now, action
):
    validate_path(path, query)
    require(
        typ == ASSET_OBJECT_TYP
        and method in {"GET", "HEAD"}
        and action == ACTION
        and raw_body == b""
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
            "asset_id",
            "asset_revision",
            "asset_version",
            "ownership_hash",
            "object_id",
            "object_version",
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
        and c["action"] == ACTION
        and c["policy_version"] == POLICY_VERSION
        and integer(c["authorization_version"])
        and integer(c["asset_revision"])
        and integer(c["asset_version"])
        and c["asset_version"] <= c["asset_revision"]
        and integer(c["object_version"])
    )
    for name in (
        "tenant_id",
        "workspace_id",
        "object_id",
        "jti",
        "trace_id",
        "request_id",
    ):
        require(isinstance(c[name], str) and ID.fullmatch(c[name]))
    require(isinstance(c["asset_id"], str) and ASSET_ID.fullmatch(c["asset_id"]))
    require(
        isinstance(c["ownership_hash"], str)
        and re.fullmatch(r"[0-9a-f]{64}", c["ownership_hash"])
    )
    actor = c["actor"]
    require(
        isinstance(actor, dict)
        and set(actor) == {"kind", "id"}
        and actor["kind"] == "user"
        and isinstance(actor["id"], str)
        and OPAQUE_ID.fullmatch(actor["id"])
    )
    require(
        c["request"] == {"method": method, "path": path}
        and path
        == ROUTE.format(
            tenant=c["tenant_id"],
            workspace=c["workspace_id"],
            object=c["object_id"],
            version=c["object_version"],
        )
    )
    issued, expires = utc_second(c["issued_at"]), utc_second(c["expires_at"])
    require(
        0 < (expires - issued).total_seconds() <= 30
        and (issued - now).total_seconds() <= 5
        and now < expires
        and key.not_before <= issued < key.expires_at
        and expires <= key.expires_at
    )
    return Verified(c, expires, None)


class AssetObjectAuthority(PlatformAuthority):
    def __init__(self, base_url, key, client):
        super().__init__(base_url, key, client)
        self.url = base_url.rstrip("/") + CALLBACK_PATH

    async def check_asset_object(self, verified):
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
                "asset_id",
                "asset_revision",
                "asset_version",
                "ownership_hash",
                "object_id",
                "object_version",
            )
        }
        body["actor_user_id"] = c["actor"]["id"]
        raw = canonicalize(json.dumps(body).encode())
        claims = {
            "schema_version": "1.0",
            "issuer": "agri-gis",
            "subject": "agri-gis",
            "audience": "flow-platform",
            "capability": "asset.download.check",
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
