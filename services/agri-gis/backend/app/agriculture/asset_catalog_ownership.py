"""Narrow metadata proof for no-farm assets; not ordinary content delegation."""

import asyncio
from datetime import UTC, datetime, timedelta
from uuid import uuid4

from fastapi import Request
from fastapi.responses import JSONResponse

from .asset_catalog_rules import ASSET_ID, FARM_ID
from .identity.dependency import verify_request
from .identity.verify import SERVICE_TYP, IdentityError, integer, require

ASSET_OWNERSHIP_PATH = "/internal/agri/v1/assets/ownership/resolve"


def install_asset_ownership_route(app):
    @app.post(ASSET_OWNERSHIP_PATH)
    async def ownership(request: Request):
        proof = await verify_request(request, SERVICE_TYP, "asset.ownership.read")
        body, claims = proof.body, proof.claims
        require(
            isinstance(body, dict)
            and set(body) == {"tenant_id", "workspace_id", "asset_ids"},
            "invalid_asset_request",
            400,
        )
        require(
            body["tenant_id"] == claims["tenant_id"]
            and body["workspace_id"] == claims["workspace_id"],
            "request_scope_mismatch",
            403,
        )
        ids = body["asset_ids"]
        require(
            isinstance(ids, list)
            and 1 <= len(ids) <= 200
            and all(
                isinstance(value, str) and ASSET_ID.fullmatch(value) for value in ids
            )
            and ids == sorted(set(ids)),
            "invalid_asset_request",
            400,
        )
        provider = request.app.state.identity.asset_catalog_ownership
        require(provider is not None, "identity_dependency_unavailable", 503)
        try:
            facts = await asyncio.wait_for(
                provider.resolve_asset(
                    claims["tenant_id"], claims["workspace_id"], ids
                ),
                timeout=2,
            )
            require(len(facts) == len(ids), "resource_not_found", 404)
            indexed = {fact.asset_id: fact for fact in facts}
            require(
                set(indexed) == set(ids) and len(indexed) == len(facts),
                "resource_not_found",
                404,
            )
            now = datetime.now(UTC).replace(microsecond=0)
            result = []
            for asset_id in ids:
                fact = indexed[asset_id]
                require(
                    fact.tenant_id == claims["tenant_id"]
                    and fact.workspace_id == claims["workspace_id"]
                    and fact.status
                    in {"validating", "ready", "quarantined", "rejected"},
                    "resource_not_found",
                    404,
                )
                require(
                    integer(fact.revision)
                    and isinstance(fact.farm_ids, tuple)
                    and len(fact.farm_ids) <= 1
                    and all(
                        isinstance(value, str) and FARM_ID.fullmatch(value)
                        for value in fact.farm_ids
                    ),
                    "identity_dependency_unavailable",
                    503,
                )
                result.append(
                    {
                        "resource": {"kind": "asset", "id": asset_id},
                        "tenant_id": fact.tenant_id,
                        "workspace_id": fact.workspace_id,
                        "farm_ids": list(fact.farm_ids),
                        "revision": fact.revision,
                        "status": fact.status,
                        "receipt_id": "receipt_" + uuid4().hex,
                        "issued_at": now.isoformat().replace("+00:00", "Z"),
                        "expires_at": (now + timedelta(seconds=30))
                        .isoformat()
                        .replace("+00:00", "Z"),
                    }
                )
            require(datetime.now(UTC) < proof.expires_at, "delegation_expired", 401)
            return JSONResponse(
                {"resources": result}, headers={"Cache-Control": "private, no-store"}
            )
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("identity_dependency_unavailable", 503) from None
