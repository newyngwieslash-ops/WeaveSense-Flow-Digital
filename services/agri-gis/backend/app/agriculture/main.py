"""Agriculture ASGI profile. It deliberately imports no legacy GIS API router."""

import uuid
from contextlib import asynccontextmanager
from datetime import UTC, datetime, timedelta

from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

from .identity.config import configured_services
from .identity.dependency import IdentityServices, verify_request
from .identity.keys import KeyRing
from .identity.providers import resolve_owned
from .identity.verify import (
    SERVICE_TYP,
    IdentityError,
    Verifier,
    require,
    resource_refs,
)


def create_app(services: IdentityServices | None = None) -> FastAPI:
    @asynccontextmanager
    async def lifespan(application: FastAPI):
        if services is not None:
            yield
        else:
            async with configured_services() as configured:
                application.state.identity = configured
                yield

    app = FastAPI(
        title="WeaveSense Agricultural Service",
        docs_url=None,
        redoc_url=None,
        openapi_url=None,
        lifespan=lifespan,
    )
    app.state.identity = services or IdentityServices(Verifier(KeyRing([])))

    @app.exception_handler(IdentityError)
    async def identity_error(_request: Request, error: IdentityError):
        return JSONResponse(
            {"code": error.code},
            status_code=error.status,
            headers={"Cache-Control": "no-store"},
        )

    @app.get("/health")
    async def health():
        services = app.state.identity
        return {
            "status": "ok",
            "agriculture_content_ready": services.ownership is not None
            and services.authority is not None
            and services.replay is not None,
        }

    @app.post("/internal/agri/v1/ownership/resolve")
    async def ownership(request: Request):
        verified = await verify_request(request, SERVICE_TYP, "ownership.resolve")
        c, body = verified.claims, verified.body
        require(
            set(body) == {"tenant_id", "workspace_id", "resources"}
            and body["tenant_id"] == c["tenant_id"]
            and body["workspace_id"] == c["workspace_id"],
            "request_scope_mismatch",
            403,
        )
        refs = resource_refs(body["resources"])
        services = app.state.identity
        facts = await resolve_owned(
            services.ownership, c["tenant_id"], c["workspace_id"], refs
        )
        require(datetime.now(UTC) < verified.expires_at, "delegation_expired")
        # This credential permits only bounded read-only ownership metadata.
        # Its jti is correlation, not a sensitive-write replay receipt.
        now = datetime.now(UTC).replace(microsecond=0)
        result = []
        for fact in facts:
            record = {
                "resource": {"kind": fact.kind, "id": fact.resource_id},
                "tenant_id": fact.tenant_id,
                "workspace_id": fact.workspace_id,
                "farm_ids": list(fact.farm_ids),
                "revision": fact.revision,
                "status": fact.status,
                "receipt_id": "receipt_" + uuid.uuid4().hex,
                "issued_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "expires_at": (now + timedelta(seconds=30)).strftime(
                    "%Y-%m-%dT%H:%M:%SZ"
                ),
            }
            if fact.assigned_actor_id is not None:
                record["assigned_actor_id"] = fact.assigned_actor_id
            result.append(record)
        return JSONResponse(
            {"resources": result}, headers={"Cache-Control": "no-store"}
        )

    return app


app = create_app()
