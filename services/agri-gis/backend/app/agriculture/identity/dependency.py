"""FastAPI verification dependency shared by future real agricultural routes."""

import asyncio
import inspect
from dataclasses import dataclass
from datetime import UTC, datetime

from fastapi import Request

from .providers import AuthorityProvider, OwnershipProvider, resolve_owned
from .replay import ReplayLedger
from .verify import (
    BUSINESS_TYP,
    IdentityError,
    Verified,
    Verifier,
    require,
    resource_refs,
)


@dataclass
class IdentityServices:
    verifier: Verifier
    ownership: OwnershipProvider | None = None
    authority: AuthorityProvider | None = None
    replay: ReplayLedger | None = None


async def verify_request(request: Request, typ: str, action: str) -> Verified:
    require(
        len(request.scope["headers"]) <= 128
        and sum(len(k) + len(v) + 4 for k, v in request.scope["headers"]) <= 262144,
        "request_headers_too_large",
        431,
    )
    headers = [v for k, v in request.scope["headers"] if k.lower() == b"authorization"]
    require(len(headers) == 1 and headers[0].startswith(b"Bearer "))
    try:
        token = headers[0][7:].decode("ascii")
        path = request.scope["raw_path"].decode("ascii")
    except UnicodeError:
        raise IdentityError("invalid_delegation") from None
    require(path == request.url.path)
    raw = bytearray()
    async for chunk in request.stream():
        raw.extend(chunk)
        require(len(raw) <= 1048576, "request_too_large", 413)
    return request.app.state.identity.verifier.verify(
        token,
        typ=typ,
        method=request.method,
        path=path,
        query=request.scope["query_string"],
        raw_body=bytes(raw),
        now=datetime.now(UTC),
        action=action,
    )


def delegation_dependency(action: str, resource_extractor):
    """Fixed route code extracts complete handler resources from path/body.

    An extractor never receives token claims. Missing configuration must fail
    rather than infer actual handler resources from the ticket being checked.
    """
    if not callable(resource_extractor):
        raise ValueError("a route resource extractor is required")

    async def verify(request: Request) -> Verified:
        services: IdentityServices = request.app.state.identity
        verified = await verify_request(request, BUSINESS_TYP, action)
        c = verified.claims
        try:
            actual = resource_extractor(request, verified.body)
            if inspect.isawaitable(actual):
                actual = await asyncio.wait_for(actual, timeout=2)
            require(isinstance(actual, list) and 1 <= len(actual) <= 200)
            for ref in actual:
                resource_refs([ref])
            actual = resource_refs(
                sorted(actual, key=lambda ref: (ref["kind"], ref["id"]))
            )
        except Exception:
            raise IdentityError("invalid_route_resources", 400) from None
        require(actual == c["resource_scope"], "request_scope_mismatch", 403)
        if services.authority is None:
            raise IdentityError("identity_dependency_unavailable", 503)
        facts = await resolve_owned(
            services.ownership, c["tenant_id"], c["workspace_id"], c["resource_scope"]
        )
        farms = sorted({farm for fact in facts for farm in fact.farm_ids})
        require(farms == c["farm_ids"], "resource_not_found", 404)
        require(
            all(
                fact.revision == version["revision"]
                for fact, version in zip(facts, c["resource_versions"], strict=True)
            ),
            "resource_not_found",
            404,
        )
        try:
            await asyncio.wait_for(
                services.authority.check(
                    c["actor"]["id"],
                    c["tenant_id"],
                    c["workspace_id"],
                    c["authorization_version"],
                ),
                timeout=2,
            )
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("identity_dependency_unavailable", 503) from None
        require(datetime.now(UTC) < verified.expires_at, "delegation_expired")
        if request.method not in {"GET", "HEAD"}:
            if services.replay is None:
                raise IdentityError("identity_dependency_unavailable", 503)
            await services.replay.claim(
                jti=c["jti"],
                tenant_id=c["tenant_id"],
                workspace_id=c["workspace_id"],
                expires_at=verified.expires_at,
            )
        # Pool/lock waits may outlive a valid ticket. A committed nonce remains
        # consumed; expiration must never release it or enter the handler.
        require(datetime.now(UTC) < verified.expires_at, "delegation_expired")
        return verified

    return verify
