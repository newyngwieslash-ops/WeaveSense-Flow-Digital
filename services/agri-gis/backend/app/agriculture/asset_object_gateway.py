"""Exact asset byte route with current typed ownership and rights at both edges."""

import asyncio
from datetime import UTC, datetime

from fastapi import Request
from fastapi.responses import Response

from .asset_catalog_objects import CatalogObjectStore
from .asset_object_identity import ACTION, ASSET_OBJECT_TYP, ROUTE, asset_ownership_hash
from .identity.dependency import verify_request
from .identity.verify import IdentityError, require
from .object_gateway import CACHE
from .object_storage import byte_range


async def current_reference(request, proof):
    services, c = request.app.state.identity, proof.claims
    ownership = services.asset_catalog_ownership
    authority = getattr(services, "asset_object_authority", None)
    require(
        ownership is not None
        and authority is not None
        and isinstance(services.object_store, CatalogObjectStore),
        "object_dependency_unavailable",
        503,
    )
    try:
        facts = await asyncio.wait_for(
            ownership.resolve_asset(c["tenant_id"], c["workspace_id"], [c["asset_id"]]),
            timeout=2,
        )
        require(len(facts) == 1, "resource_not_found", 404)
        fact = facts[0]
        require(
            fact.asset_id == c["asset_id"]
            and fact.tenant_id == c["tenant_id"]
            and fact.workspace_id == c["workspace_id"]
            and fact.revision == c["asset_revision"]
            and asset_ownership_hash(fact) == c["ownership_hash"],
            "resource_not_found",
            404,
        )
        await asyncio.wait_for(authority.check_asset_object(proof), timeout=2)
        item = await services.object_store.get(
            c["tenant_id"], c["workspace_id"], c["object_id"], c["object_version"]
        )
        require(
            item.resource_kind == "asset"
            and item.resource_id == c["asset_id"]
            and item.resource_revision == c["asset_version"],
            "object_not_found",
            404,
        )
        require(datetime.now(UTC) < proof.expires_at, "delegation_expired", 401)
        return item
    except IdentityError:
        raise
    except Exception:
        raise IdentityError("object_dependency_unavailable", 503) from None


def install_asset_object_routes(app):
    async def content(request: Request):
        proof = await verify_request(request, ASSET_OBJECT_TYP, ACTION)
        item = await current_reference(request, proof)
        ranges = [
            value for key, value in request.scope["headers"] if key.lower() == b"range"
        ]
        require(len(ranges) <= 1, "invalid_object_range", 416)
        try:
            range_value = ranges[0].decode("ascii") if ranges else None
        except UnicodeError:
            raise IdentityError("invalid_object_range", 416) from None
        start, end = byte_range(range_value, item.size_bytes)
        provider = request.app.state.identity.object_storage
        require(provider is not None, "object_dependency_unavailable", 503)
        try:
            body = await asyncio.to_thread(
                provider.read,
                tenant=item.tenant_id,
                workspace=item.workspace_id,
                object_id=item.object_id,
                version=item.version,
                key=item.object_key,
                size=item.size_bytes,
                sha256=item.content_sha256,
            )
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("object_dependency_unavailable", 503) from None
        require(
            await current_reference(request, proof) == item, "object_not_found", 404
        )
        headers = CACHE | {
            "Content-Length": str(end - start),
            "Accept-Ranges": "bytes",
            "Content-Disposition": 'attachment; filename="object.bin"',
        }
        if range_value is not None:
            headers["Content-Range"] = f"bytes {start}-{end-1}/{item.size_bytes}"
        return Response(
            b"" if request.method == "HEAD" else body[start:end],
            status_code=206 if range_value is not None else 200,
            media_type=item.media_type,
            headers=headers,
        )

    app.add_api_route(ROUTE, content, methods=["GET", "HEAD"])
