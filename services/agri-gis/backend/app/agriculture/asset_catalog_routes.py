"""Fixed catalog routes; registration does not create a downloadable entity."""

from datetime import UTC, datetime

from fastapi import Request
from fastapi.responses import JSONResponse

from .asset_catalog_identity import ASSET_PATHS, ASSET_TYP
from .identity.dependency import verify_request
from .identity.verify import IdentityError, require


async def current(request, verified):
    provider = getattr(request.app.state.identity, "asset_catalog_authority", None)
    require(provider is not None, "CATALOG_DEPENDENCY_UNAVAILABLE", 503)
    await provider.check_asset(verified)
    require(datetime.now(UTC) < verified.expires_at, "delegation_expired", 401)


def install_asset_catalog_routes(app):
    def handler(action):
        async def endpoint(request: Request):
            try:
                verified = await verify_request(request, ASSET_TYP, action)
                await current(request, verified)
                store = getattr(request.app.state.identity, "asset_catalog_store", None)
                require(store is not None, "CATALOG_DEPENDENCY_UNAVAILABLE", 503)
                c, body = verified.claims, verified.body
                tenant, workspace = c["tenant_id"], c["workspace_id"]
                if action == "asset.context":
                    value = {"sources": store.rights.source_options(tenant, workspace)}
                elif action == "asset.list":
                    value = await store.list(
                        tenant,
                        workspace,
                        body["asset_ids"],
                        limit=body["limit"],
                        after_id=body["after_id"],
                    )
                elif action == "asset.read":
                    value = await store.get(
                        tenant, workspace, body["asset_id"], body["version"]
                    )
                elif action == "asset.receipt":
                    value = await store.receipt(
                        tenant,
                        workspace,
                        body["operation_id"],
                        c["actor"]["id"],
                        body["request_hash"],
                        body["grant_plan_hash"],
                    )
                elif action.startswith("asset.preview."):
                    mode = (
                        "asset.create" if action.endswith("create") else "asset.version"
                    )
                    value = {
                        "summary": await store.preview(
                            tenant,
                            workspace,
                            mode,
                            body["metadata"],
                            body["asset_id"],
                            body["expected_revision"],
                        )
                    }
                else:
                    replay = request.app.state.identity.replay
                    require(replay is not None, "CATALOG_DEPENDENCY_UNAVAILABLE", 503)
                    await replay.claim(
                        jti=c["jti"],
                        tenant_id=tenant,
                        workspace_id=workspace,
                        expires_at=verified.expires_at,
                    )
                    value = await store.execute(
                        verified,
                        action,
                        body,
                        before_write=lambda: current(request, verified),
                    )
                await current(request, verified)
                return JSONResponse(
                    value, headers={"Cache-Control": "private, no-store"}
                )
            except IdentityError:
                raise
            except Exception:
                raise IdentityError("CATALOG_DEPENDENCY_UNAVAILABLE", 503) from None

        return endpoint

    for action, path in ASSET_PATHS.items():
        app.add_api_route(path, handler(action), methods=["POST"])
