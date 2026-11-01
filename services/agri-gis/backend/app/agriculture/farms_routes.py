"""Fixed farm routes; server composition is the only source of providers."""

from datetime import UTC, datetime
from decimal import Decimal

from fastapi import Request
from fastapi.responses import JSONResponse

from .farms_identity import WORKSPACE_PATHS, WORKSPACE_TYP
from .farms_store import FARM_FIELDS, ZERO_HASH, text_field
from .identity.dependency import delegation_dependency, verify_request
from .identity.providers import resolve_owned
from .identity.verify import IdentityError, require


def farm_store(request):
    value = request.app.state.identity.farm_store
    require(value is not None, "DEPENDENCY_UNAVAILABLE", 503)
    return value


def response(value):
    return JSONResponse(value, headers={"Cache-Control": "private, no-store"})


def summary(value, measurement):
    area = measurement["area_m2"]
    return {
        **value,
        "area_m2": format(area, "f"),
        "area_ha": format(area / Decimal(10000), "f"),
        "area_mu": format(area * Decimal(3) / Decimal(2000), "f"),
    }


async def current_workspace(request, verified):
    authority = request.app.state.identity.farm_authority
    require(authority is not None, "DEPENDENCY_UNAVAILABLE", 503)
    await authority.check_farm(verified)
    require(datetime.now(UTC) < verified.expires_at, "delegation_expired", 401)


async def current_business(request, verified):
    services = request.app.state.identity
    c = verified.claims
    require(services.authority is not None, "DEPENDENCY_UNAVAILABLE", 503)
    facts = await resolve_owned(
        services.ownership, c["tenant_id"], c["workspace_id"], c["resource_scope"]
    )
    require(
        len(facts) == 1 and facts[0].revision == c["resource_versions"][0]["revision"],
        "resource_not_found",
        404,
    )
    await services.authority.check(
        c["actor"]["id"], c["tenant_id"], c["workspace_id"], c["authorization_version"]
    )
    require(datetime.now(UTC) < verified.expires_at, "delegation_expired", 401)


def install_farm_routes(app):
    def guarded(endpoint):
        async def safe_endpoint(request: Request):
            try:
                return await endpoint(request)
            except IdentityError:
                raise
            except Exception:
                # SQL/driver/parser values must never reach a public response.
                raise IdentityError("DEPENDENCY_UNAVAILABLE", 503) from None

        return safe_endpoint

    def workspace_endpoint(action):
        async def endpoint(request: Request):
            verified = await verify_request(request, WORKSPACE_TYP, action)
            await current_workspace(request, verified)
            store = farm_store(request)
            body = verified.body
            if action == "farm.context":
                config = store.configuration().snapshot()
                data = {
                    "dictionary_version": config["dictionary_version"],
                    "regions": config["regions"],
                    "area_units": ["m2", "ha", "mu"],
                }
            elif action == "farm.preview.create":
                value, geometry = await store.prepare(body["farm"])
                data = {"summary": summary(value, geometry)}
            elif action == "farm.create":
                replay = request.app.state.identity.replay
                require(replay is not None, "DEPENDENCY_UNAVAILABLE", 503)
                await replay.claim(
                    jti=verified.claims["jti"],
                    tenant_id=verified.claims["tenant_id"],
                    workspace_id=verified.claims["workspace_id"],
                    expires_at=verified.expires_at,
                )
                # Repeat current intent/epoch after validation and before writes.
                await current_workspace(request, verified)
                data = await store.execute(
                    verified,
                    "farm.create",
                    body,
                    before_write=lambda: current_workspace(request, verified),
                )
                return response(data)
            elif action == "farm.receipt":
                data = await store.receipt(
                    verified,
                    body["operation_id"],
                    body["request_hash"],
                    body["grant_plan_hash"],
                )
            elif action == "farm.list":
                data = await store.list(
                    verified,
                    body["farm_ids"],
                    body["status"],
                    body["limit"],
                    body["after_id"],
                    body["search"],
                )
                for item in data["items"]:
                    item.pop("geometry", None)
                    item.pop("contact", None)
            else:
                data = await store.get(verified, body["farm_id"], archived=True)
            # Read-only profiles do not permit stale grants after DB/geometry waits.
            await current_workspace(request, verified)
            return response(data)

        return endpoint

    for action, path in WORKSPACE_PATHS.items():
        app.add_api_route(path, guarded(workspace_endpoint(action)), methods=["POST"])

    def business_endpoint(action, mode):
        check = delegation_dependency(
            action,
            lambda request, _body: [
                {"kind": "farm", "id": request.path_params["farm_id"]}
            ],
        )

        async def endpoint(request: Request):
            verified = await check(request)
            store = farm_store(request)
            body = verified.body
            farm_id = request.path_params["farm_id"]
            if mode == "read":
                data = await store.get(verified, farm_id)
                require(
                    data["revision"]
                    == verified.claims["resource_versions"][0]["revision"],
                    "resource_not_found",
                    404,
                )
                await current_business(request, verified)
                return response(data)
            fields = (
                {"expected_revision", "farm"}
                if "update" in mode
                else {"expected_revision", "reason"}
            )
            if mode in {"update", "archive"}:
                fields |= {"operation_id", "request_hash"}
            require(
                isinstance(body, dict) and "expected_revision" in body,
                "PRECONDITION_REQUIRED",
                428,
            )
            require(
                isinstance(body, dict) and set(body) == fields, "INVALID_ARGUMENT", 400
            )
            require(
                type(body["expected_revision"]) is int
                and body["expected_revision"] >= 1,
                "INVALID_ARGUMENT",
                400,
            )
            current = await store.get(verified, farm_id)
            require(
                current["revision"] == body["expected_revision"],
                "REVISION_CONFLICT",
                409,
            )
            if mode == "preview-update":
                changes = body["farm"]
                require(
                    isinstance(changes, dict)
                    and changes
                    and set(changes) <= FARM_FIELDS - {"external_code", "data_class"},
                    "INVALID_ARGUMENT",
                    400,
                )
                value = {k: current[k] for k in FARM_FIELDS}
                value.update(changes)
                value, geometry = await store.prepare(value)
                data = {
                    "summary": {
                        **summary(value, geometry),
                        "id": farm_id,
                        "revision": current["revision"],
                    }
                }
            elif mode == "preview-archive":
                text_field(body["reason"], 500)
                data = {
                    "summary": {
                        "id": farm_id,
                        "revision": current["revision"],
                        "reason": body["reason"],
                    }
                }
            else:
                require(
                    body["operation_id"] == verified.claims["request"]["operation_id"],
                    "request_scope_mismatch",
                    403,
                )
                await current_business(request, verified)
                data = await store.execute(
                    verified,
                    "farm." + mode,
                    {**body, "grant_plan_hash": ZERO_HASH},
                    farm_id=farm_id,
                    before_write=lambda: current_business(request, verified),
                )
                return response(data)
            await current_business(request, verified)
            return response(data)

        return endpoint

    prefix = "/internal/agri/v1/farms/{farm_id}"
    for method, path, action, mode in (
        ("GET", prefix, "agri.farm.read", "read"),
        ("POST", prefix + "/preview-update", "agri.farm.update", "preview-update"),
        ("POST", prefix + "/preview-archive", "agri.farm.archive", "preview-archive"),
        ("PATCH", prefix, "agri.farm.update", "update"),
        ("POST", prefix + "/archive", "agri.farm.archive", "archive"),
    ):
        app.add_api_route(
            path, guarded(business_endpoint(action, mode)), methods=[method]
        )
