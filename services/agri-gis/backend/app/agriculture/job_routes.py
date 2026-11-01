"""Explicit adapter installation; unavailable queue providers never imply access."""

from datetime import UTC, datetime

from fastapi import Request

from .identity.dependency import verify_request
from .identity.providers import resolve_owned
from .identity.verify import BUSINESS_TYP, IdentityError, require
from .job_authority import JobCheckpoint, match_snapshot


def install_job_routes(app, authority):
    # Set only by trusted application composition, never by request data.
    app.state.job_authority = authority

    async def verified_scope(request, creating):
        authority = request.app.state.job_authority
        verified = await verify_request(request, BUSINESS_TYP, "agri.analysis.submit")
        if authority is None:
            raise IdentityError("job_authority_unavailable", 503)
        if creating:
            require(
                set(verified.body) == {"snapshot", "input"}, "invalid_job_scope", 400
            )
            snapshot = verified.body["snapshot"]
        else:
            checkpoint = JobCheckpoint.from_body(verified.body)
            snapshot = await authority.store.get(verified, checkpoint.job_id)
        match_snapshot(verified, snapshot)
        services = request.app.state.identity
        facts = await resolve_owned(
            services.ownership,
            snapshot["tenant_id"],
            snapshot["workspace_id"],
            snapshot["resources"],
        )
        require(
            sorted({farm for fact in facts for farm in fact.farm_ids})
            == snapshot["farm_ids"],
            "job_resource_withdrawn",
            403,
        )
        versions = {
            (v["resource"]["kind"], v["resource"]["id"]): v["revision"]
            for v in snapshot["resource_versions"]
        }
        require(
            all(
                versions.get((fact.kind, fact.resource_id)) == fact.revision
                for fact in facts
            ),
            "job_resource_withdrawn",
            403,
        )
        if services.replay is None:
            raise IdentityError("identity_dependency_unavailable", 503)
        await services.replay.claim(
            jti=verified.claims["jti"],
            tenant_id=snapshot["tenant_id"],
            workspace_id=snapshot["workspace_id"],
            expires_at=verified.expires_at,
        )
        require(verified.expires_at > datetime.now(UTC), "expired_job_identity", 401)
        return verified

    @app.post("/internal/agri/v1/jobs/authority/accept")
    async def accept(request: Request):
        try:
            verified = await verified_scope(request, True)
            return await request.app.state.job_authority.accept(verified)
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("job_authority_unavailable", 503) from None

    @app.post("/internal/agri/v1/jobs/authority/checkpoint")
    async def checkpoint(request: Request):
        try:
            verified = await verified_scope(request, False)
            return await request.app.state.job_authority.checkpoint(verified)
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("job_authority_unavailable", 503) from None
