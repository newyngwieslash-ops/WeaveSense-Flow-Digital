"""Authenticated byte gateway; no bearer URLs, public cache or storage-key API."""

import asyncio
from datetime import UTC, datetime

from fastapi import Request
from fastapi.responses import JSONResponse, Response

from .identity.dependency import delegation_dependency, verify_request
from .identity.providers import resolve_owned
from .identity.verify import SERVICE_TYP, IdentityError, require
from .object_storage import byte_range

CACHE = {"Cache-Control": "private, no-store", "X-Content-Type-Options": "nosniff"}
PREFIX = "/internal/agri/v1/tenants/{tenant}/workspaces/{workspace}"


def store(request):
    value = request.app.state.identity.object_store
    if value is None:
        raise IdentityError("object_dependency_unavailable", 503)
    return value


def version_value(value):
    if not isinstance(value, str) or not value.isascii() or not value.isdecimal():
        raise IdentityError("object_not_found", 404)
    number = int(value) if len(value) <= 16 else 0
    if not 1 <= number <= 9007199254740991 or str(number) != value:
        raise IdentityError("object_not_found", 404)
    return number


async def reference(request):
    p = request.path_params
    return await store(request).get(
        p["tenant"], p["workspace"], p["object"], version_value(p["version"])
    )


def register_object_routes(app):
    @app.middleware("http")
    async def private_object_cache(request, call_next):
        response = await call_next(request)
        if "/objects/" in request.url.path:
            for name, value in CACHE.items():
                response.headers[name] = value
            if "etag" in response.headers:
                del response.headers["etag"]
        return response

    @app.post("/internal/agri/v1/objects/resolve")
    async def metadata(request: Request):
        proof = await verify_request(request, SERVICE_TYP, "object.resolve")
        body, claims = proof.body, proof.claims
        require(
            isinstance(body, dict)
            and set(body) == {"tenant_id", "workspace_id", "object_id", "version"},
            "invalid_object_request",
            400,
        )
        require(
            body["tenant_id"] == claims["tenant_id"]
            and body["workspace_id"] == claims["workspace_id"],
            "request_scope_mismatch",
            403,
        )
        item = await store(request).get(
            body["tenant_id"], body["workspace_id"], body["object_id"], body["version"]
        )
        require(datetime.now(UTC) < proof.expires_at, "delegation_expired")
        return JSONResponse(item.metadata(), headers=CACHE)

    def register(kind, action):
        async def extract(request, _body):
            item = await reference(request)
            require(item.resource_kind == kind, "object_not_found", 404)
            return [{"kind": kind, "id": item.resource_id}]

        check = delegation_dependency(action, extract)

        async def content(request: Request):
            proof = await check(request)
            p, claims = request.path_params, proof.claims
            require(
                p["tenant"] == claims["tenant_id"]
                and p["workspace"] == claims["workspace_id"],
                "request_scope_mismatch",
                403,
            )
            item = await reference(request)
            require(
                item.resource_kind == kind
                and claims["resource_scope"] == [{"kind": kind, "id": item.resource_id}]
                and claims["resource_versions"][0]["revision"]
                == item.resource_revision,
                "object_not_found",
                404,
            )
            ranges = [v for k, v in request.scope["headers"] if k.lower() == b"range"]
            require(len(ranges) <= 1, "invalid_object_range", 416)
            try:
                range_value = ranges[0].decode("ascii") if ranges else None
            except UnicodeError:
                raise IdentityError("invalid_object_range", 416) from None
            start, end = byte_range(range_value, item.size_bytes)
            provider = request.app.state.identity.object_storage
            if provider is None:
                raise IdentityError("object_dependency_unavailable", 503)
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
            # Storage I/O can outlive revocation. Repeat current object and
            # authority checks before sending any response bytes.
            require(await reference(request) == item, "object_not_found", 404)
            services = request.app.state.identity
            facts = await resolve_owned(
                services.ownership,
                item.tenant_id,
                item.workspace_id,
                claims["resource_scope"],
            )
            require(
                facts[0].revision == item.resource_revision
                and list(facts[0].farm_ids) == claims["farm_ids"],
                "object_not_found",
                404,
            )
            try:
                await asyncio.wait_for(
                    services.authority.check(
                        claims["actor"]["id"],
                        item.tenant_id,
                        item.workspace_id,
                        claims["authorization_version"],
                    ),
                    timeout=2,
                )
            except IdentityError:
                raise
            except Exception:
                raise IdentityError("identity_dependency_unavailable", 503) from None
            require(datetime.now(UTC) < proof.expires_at, "delegation_expired")
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

        app.add_api_route(
            PREFIX + f"/{kind}s/objects/{{object}}/versions/{{version}}",
            content,
            methods=["GET", "HEAD"],
        )

    register("asset", "agri.asset_download.read")
    register("report", "agri.report.read")
