"""Fixed parcel endpoints; all content uses current nonempty business proofs."""

from datetime import UTC, datetime
from decimal import Decimal

from fastapi import Request
from fastapi.responses import JSONResponse

from .identity.dependency import delegation_dependency, verify_request
from .identity.providers import resolve_owned
from .identity.verify import IdentityError, require
from .parcels_identity import PARCEL_OPERATION_TYP, RECEIPT_PATH
from .parcels_store import GEOMETRY_FIELDS


def response(value):
    return JSONResponse(value, headers={"Cache-Control": "private, no-store"})


def store_for(request):
    store = request.app.state.identity.parcel_store
    require(store is not None, "DEPENDENCY_UNAVAILABLE", 503)
    return store


async def current(request, verified):
    services, c = request.app.state.identity, verified.claims
    require(services.authority is not None, "DEPENDENCY_UNAVAILABLE", 503)
    facts = await resolve_owned(
        services.ownership, c["tenant_id"], c["workspace_id"], c["resource_scope"]
    )
    require(
        all(
            f.revision == v["revision"]
            for f, v in zip(facts, c["resource_versions"], strict=True)
        )
        and sorted({farm for f in facts for farm in f.farm_ids}) == c["farm_ids"],
        "resource_not_found",
        404,
    )
    await services.authority.check(
        c["actor"]["id"], c["tenant_id"], c["workspace_id"], c["authorization_version"]
    )
    require(datetime.now(UTC) < verified.expires_at, "delegation_expired", 401)


def list_resources(request, body):
    require(
        isinstance(body, dict)
        and set(body) == {"access_mode", "parcel_ids", "after_id", "limit"},
        "INVALID_ARGUMENT",
        400,
    )
    if body["access_mode"] == "farm":
        require(body["parcel_ids"] == [], "INVALID_ARGUMENT", 400)
        return [{"kind": "farm", "id": request.path_params["farm_id"]}]
    ids = body["parcel_ids"]
    require(
        body["access_mode"] == "ids"
        and isinstance(ids, list)
        and 1 <= len(ids) <= 200
        and all(isinstance(x, str) for x in ids)
        and ids == sorted(set(ids)),
        "INVALID_ARGUMENT",
        400,
    )
    return [{"kind": "parcel", "id": value} for value in ids]


def install_parcel_routes(app):
    def guarded(endpoint):
        async def safe(request: Request):
            try:
                return await endpoint(request)
            except IdentityError:
                raise
            except Exception:
                raise IdentityError("DEPENDENCY_UNAVAILABLE", 503) from None

        return safe

    async def receipt(request):
        verified = await verify_request(request, PARCEL_OPERATION_TYP, "parcel.receipt")
        authority = request.app.state.identity.parcel_authority
        require(authority is not None, "DEPENDENCY_UNAVAILABLE", 503)
        await authority.check_parcel(verified)
        value = await store_for(request).receipt(
            verified, verified.body["operation_id"], verified.body["request_hash"]
        )
        await authority.check_parcel(verified)
        return response(value)

    app.add_api_route(RECEIPT_PATH, guarded(receipt), methods=["POST"])

    def endpoint_for(mode):
        create = mode in {"preview-create", "create"}
        action = (
            "agri.parcel.create"
            if create
            else "agri.parcel.update"
            if mode in {"preview-revise", "revise"}
            else "agri.parcel.read"
        )
        if mode == "list":
            extractor = list_resources
        else:

            def extractor(request, _body):
                return [
                    {
                        "kind": "farm" if create else "parcel",
                        "id": request.path_params["farm_id" if create else "parcel_id"],
                    }
                ]

        check = delegation_dependency(
            action,
            extractor,
            scope_guard=(
                lambda request, verified: verified.claims["farm_ids"]
                == [request.path_params["farm_id"]]
            )
            if mode == "list"
            else None,
        )

        async def endpoint(request):
            verified = await check(request)
            body, store = verified.body, store_for(request)
            farm_id = request.path_params.get("farm_id")
            parcel_id = request.path_params.get("parcel_id")
            require(isinstance(body, dict), "INVALID_ARGUMENT", 400)
            if mode == "read":
                require(set(body) == {"revision", "at"}, "INVALID_ARGUMENT", 400)
                data = await store.read(
                    verified,
                    parcel_id,
                    selected_revision=body["revision"],
                    at=body["at"],
                )
                require(
                    data["current_geometry_revision"]
                    == verified.claims["resource_versions"][0]["revision"],
                    "resource_not_found",
                    404,
                )
            elif mode == "history":
                require(
                    set(body) == {"after_revision", "limit"}, "INVALID_ARGUMENT", 400
                )
                data = await store.history(
                    verified,
                    parcel_id,
                    after_revision=body["after_revision"],
                    limit=body["limit"],
                )
            elif mode == "list":
                data = await store.list(
                    verified,
                    farm_id,
                    parcel_ids=body["parcel_ids"]
                    if body["access_mode"] == "ids"
                    else None,
                    after_id=body["after_id"],
                    limit=body["limit"],
                )
            else:
                fields = {"expected_revision", "reason"} | (
                    {"expected_farm_revision", "parcel"} if create else GEOMETRY_FIELDS
                )
                write = mode in {"create", "revise"}
                if write:
                    fields |= {"operation_id", "request_hash"}
                require(
                    "expected_revision" in body
                    and (not create or "expected_farm_revision" in body),
                    "PRECONDITION_REQUIRED",
                    428,
                )
                require(set(body) == fields, "INVALID_ARGUMENT", 400)
                expected = (
                    body["expected_farm_revision"]
                    if create
                    else body["expected_revision"]
                )
                require(
                    expected == verified.claims["resource_versions"][0]["revision"],
                    "REVISION_CONFLICT",
                    409,
                )
                kind = "parcel.create" if create else "parcel.revise"
                if write:
                    require(
                        body["operation_id"]
                        == verified.claims["request"]["operation_id"],
                        "request_scope_mismatch",
                        403,
                    )
                    data = await store.execute(
                        verified,
                        kind,
                        body,
                        farm_id=farm_id,
                        parcel_id=parcel_id,
                        before_write=lambda: current(request, verified),
                    )
                    return response(data)
                geometry = await store.preview(
                    verified, kind, body, farm_id=farm_id, parcel_id=parcel_id
                )
                area = geometry["area_m2"]
                values = (
                    body["parcel"]
                    if create
                    else {key: body[key] for key in GEOMETRY_FIELDS}
                )
                data = {
                    "summary": {
                        **values,
                        "farm_id": farm_id or verified.claims["farm_ids"][0],
                        "parcel_id": parcel_id or "",
                        "expected_revision": body["expected_revision"],
                        "reason": body["reason"],
                        "geometry_hash": geometry["geometry_hash"],
                        "area_m2": format(area, "f"),
                        "area_ha": format(area / Decimal(10000), "f"),
                        "area_mu": format(area * Decimal(3) / Decimal(2000), "f"),
                        "affected_dependencies": {
                            "availability": "not_yet_integrated",
                            "count": None,
                        },
                    }
                }
            await current(request, verified)
            return response(data)

        return endpoint

    farm = "/internal/agri/v1/farms/{farm_id}/parcels"
    parcel = "/internal/agri/v1/parcels/{parcel_id}"
    for path, mode in (
        (farm + "/preview-create", "preview-create"),
        (farm, "create"),
        (farm + "/list", "list"),
        (parcel + "/preview-revise", "preview-revise"),
        (parcel + "/revisions", "revise"),
        (parcel + "/read", "read"),
        (parcel + "/history", "history"),
    ):
        app.add_api_route(path, guarded(endpoint_for(mode)), methods=["POST"])
