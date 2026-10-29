"""Resolve signed platform actors through explicit, server-owned GIS bindings."""

from dataclasses import dataclass
from datetime import UTC, datetime

from app.models.workbench import MonitoringProject, ProjectUser
from sqlalchemy import func, select, text
from sqlalchemy.ext.asyncio import AsyncSession

from .actor_models import ActorBinding, ProjectBinding
from .identity.verify import IdentityError, Verified, require


@dataclass(frozen=True)
class TrustedActor:
    stable_actor_id: str
    tenant_id: str
    workspace_id: str
    project_id: int
    legacy_member_id: int
    legacy_user_code: str
    legacy_role: str
    policy_version: str
    authorization_version: int
    request_id: str
    operation_id: str | None
    resources: tuple[tuple[str, str], ...]
    signed_action: str
    expires_at: datetime


def actor_query(tenant_id: str, workspace_id: str, actor_id: str, project_id: int):
    # Scalar snapshots do not overwrite dirty ORM objects during commit checks.
    return (
        select(*ProjectUser.__table__.c)
        .join(
            ActorBinding,
            (ActorBinding.legacy_member_id == ProjectUser.id)
            & (ActorBinding.project_id == ProjectUser.project_id),
        )
        .join(
            ProjectBinding,
            (ProjectBinding.tenant_id == ActorBinding.tenant_id)
            & (ProjectBinding.workspace_id == ActorBinding.workspace_id)
            & (ProjectBinding.project_id == ActorBinding.project_id),
        )
        .join(MonitoringProject, MonitoringProject.id == ProjectUser.project_id)
        .where(
            ActorBinding.tenant_id == tenant_id,
            ActorBinding.workspace_id == workspace_id,
            ActorBinding.actor_id == actor_id,
            ActorBinding.project_id == project_id,
            ActorBinding.state == "active",
            ProjectBinding.state == "active",
            ProjectUser.status == "active",
            MonitoringProject.status == "active",
        )
    )


async def lock_binding(db, claims, project_id):
    """The runtime cannot inherit the guard or own any protected binding table."""
    readiness = """
      EXISTS(SELECT 1 FROM pg_catalog.pg_roles r WHERE r.rolname=current_user
        AND r.rolcanlogin AND NOT(r.rolsuper OR r.rolbypassrls OR r.rolinherit
          OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication))
      AND NOT pg_catalog.pg_has_role(current_user,'flow_geo_actor_guard','MEMBER')
      AND EXISTS(SELECT 1 FROM pg_catalog.pg_proc p
        JOIN pg_catalog.pg_roles r ON r.oid=p.proowner
        WHERE p.oid=
          'public.agriculture_lock_actor(text,text,text,integer)'::regprocedure
        AND p.prosecdef AND r.rolname='flow_geo_actor_guard'
        AND NOT(r.rolcanlogin OR r.rolsuper OR r.rolbypassrls OR r.rolinherit
          OR r.rolcreatedb OR r.rolcreaterole OR r.rolreplication)
        AND p.proconfig @> ARRAY['search_path=pg_catalog']
        AND NOT EXISTS(SELECT 1 FROM pg_catalog.aclexplode(p.proacl) acl
          WHERE acl.grantee=0 AND acl.privilege_type='EXECUTE'))
      AND NOT EXISTS(SELECT 1 FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
        WHERE n.nspname='public' AND c.relname IN
          ('agriculture_project_bindings','agriculture_actor_bindings',
           'agriculture_actor_audit')
        AND (NOT c.relrowsecurity OR NOT c.relforcerowsecurity
             OR pg_catalog.pg_has_role(current_user,c.relowner,'MEMBER')))
      AND NOT pg_catalog.has_table_privilege(current_user,
        'public.agriculture_actor_bindings','INSERT,UPDATE,DELETE,TRUNCATE')
      AND NOT pg_catalog.has_table_privilege(current_user,
        'public.agriculture_project_bindings','INSERT,UPDATE,DELETE,TRUNCATE')
      AND NOT pg_catalog.has_table_privilege(current_user,
        'public.agriculture_actor_audit','UPDATE,DELETE,TRUNCATE')
      AND NOT pg_catalog.has_any_column_privilege(current_user,
        'public.agriculture_actor_bindings','UPDATE')
      AND NOT pg_catalog.has_any_column_privilege(current_user,
        'public.agriculture_project_bindings','UPDATE')
    """
    try:
        safe = (await db.execute(select(text(readiness)))).scalar_one()
        require(safe is True, "actor_binding_unavailable", 503)
        await db.execute(
            text("SELECT set_config('app.tenant_ids', :tenant, true)"),
            {"tenant": claims["tenant_id"]},
        )
        locked = (
            await db.execute(
                select(
                    func.public.agriculture_lock_actor(
                        claims["tenant_id"],
                        claims["workspace_id"],
                        claims["actor"]["id"],
                        project_id,
                    )
                )
            )
        ).scalar_one()
        require(locked is True, "actor_binding_denied", 403)
    except IdentityError:
        raise
    except Exception:
        raise IdentityError("actor_binding_unavailable", 503) from None


async def resolve_trusted_actor(
    db: AsyncSession, verified: Verified, project_id: int
) -> TrustedActor:
    """Only call with DT012 dependency output, never request identity parameters."""
    require(isinstance(verified, Verified), "trusted_actor_required", 403)
    require(not db.info.get("agriculture_resolved_actor"), "actor_already_bound", 403)
    used = db.info.setdefault("agriculture_used_verifications", [])
    require(
        not any(previous is verified for previous in used),
        "fresh_verification_required",
        403,
    )
    c = verified.claims
    require(datetime.now(UTC) < verified.expires_at, "delegation_expired")
    require(c.get("actor", {}).get("kind") == "user", "trusted_actor_required", 403)
    if db.bind.dialect.name == "postgresql":
        await lock_binding(db, c, project_id)
    try:
        user = (
            await db.execute(
                actor_query(
                    c["tenant_id"], c["workspace_id"], c["actor"]["id"], project_id
                )
            )
        ).one_or_none()
    except Exception:
        raise IdentityError("actor_binding_unavailable", 503) from None
    require(user is not None, "actor_binding_denied", 403)
    require(datetime.now(UTC) < verified.expires_at, "delegation_expired")
    actor = TrustedActor(
        c["actor"]["id"],
        c["tenant_id"],
        c["workspace_id"],
        project_id,
        user.id,
        user.user_code,
        user.role_code,
        c["policy_version"],
        c["authorization_version"],
        c["request_id"],
        c["request"].get("operation_id"),
        tuple((r["kind"], r["id"]) for r in c["resource_scope"]),
        c["scopes"][0],
        verified.expires_at,
    )
    db.info["agriculture_resolved_actor"] = actor
    db.info["agriculture_action"] = c["scopes"][0]
    used.append(verified)
    return actor


async def recheck_trusted_actor(db: AsyncSession, actor: TrustedActor) -> ProjectUser:
    require(
        db.info.get("agriculture_resolved_actor") is actor,
        "trusted_actor_required",
        403,
    )
    require(datetime.now(UTC) < actor.expires_at, "delegation_expired")
    user = (
        await db.execute(
            actor_query(
                actor.tenant_id,
                actor.workspace_id,
                actor.stable_actor_id,
                actor.project_id,
            )
        )
    ).one_or_none()
    require(
        user is not None
        and user.id == actor.legacy_member_id
        and user.user_code == actor.legacy_user_code
        and user.role_code == actor.legacy_role,
        "actor_binding_denied",
        403,
    )
    return ProjectUser(**user._mapping)
