"""Atomic audit boundary for explicitly adapted ORM-only legacy operations.

Raw/bulk DML and external side effects are not supported by this boundary.
No original router is enabled by installing this session class.
"""

from collections.abc import Callable
from dataclasses import dataclass
from datetime import UTC, datetime
from uuid import uuid4

from sqlalchemy import event, inspect, select
from sqlalchemy.ext.asyncio import AsyncSession
from sqlalchemy.orm import Session
from sqlalchemy.sql.elements import TextClause

from .actor_models import ActorAudit, ActorBinding, ProjectBinding
from .actors import actor_query
from .identity.verify import require


@dataclass(frozen=True)
class AuditAdapter:
    model: type
    action: str
    operations: frozenset[str]
    resources: Callable
    project: Callable | None = None


class AgricultureSession(Session):
    def __init__(self, *args, audit_adapters=(), **kwargs):
        kwargs["autoflush"] = False
        super().__init__(*args, **kwargs)
        self.audit_adapters = tuple(audit_adapters)


class AgricultureAsyncSession(AsyncSession):
    sync_session_class = AgricultureSession


def current_actor(session):
    actor = session.info.get("agriculture_resolved_actor")
    require(actor is not None, "trusted_actor_required", 403)
    require(
        session.info.get("agriculture_action") == actor.signed_action,
        "trusted_action_required",
        403,
    )
    require(datetime.now(UTC) < actor.expires_at, "delegation_expired")
    user = session.execute(
        actor_query(
            actor.tenant_id, actor.workspace_id, actor.stable_actor_id, actor.project_id
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
    return actor


@event.listens_for(AgricultureSession, "do_orm_execute")
def prohibit_bulk(state):
    statement = state.statement
    if isinstance(statement, TextClause):
        require(
            str(statement) == "SELECT set_config('app.tenant_ids', :tenant, true), "
            "set_config('app.workspace_id', :workspace, true)",
            "unadapted_database_operation",
            403,
        )
        scope = getattr(state.session, "_database_scope", None)
        if scope is not None:
            scope.validate()
            require(
                state.parameters
                == {"tenant": scope.tenant_id, "workspace": scope.workspace_id},
                "database_scope_rebinding_denied",
                403,
            )
    else:
        require(state.is_select, "unadapted_database_operation", 403)


@event.listens_for(AgricultureSession, "before_flush")
def audit_flush(session, flush_context, instances):
    changes = []
    actor = None
    for operation, objects in (
        ("insert", session.new),
        ("update", session.dirty),
        ("delete", session.deleted),
    ):
        for obj in objects:
            if operation == "update" and not session.is_modified(
                obj, include_collections=False
            ):
                continue
            require(
                not isinstance(obj, ActorAudit | ActorBinding | ProjectBinding),
                "server_owned_binding",
                403,
            )
            actor = actor or current_actor(session)
            action = session.info.get("agriculture_action")
            adapter = next(
                (
                    a
                    for a in session.audit_adapters
                    if type(obj) is a.model
                    and action == a.action
                    and operation in a.operations
                ),
                None,
            )
            require(adapter is not None, "unadapted_database_operation", 403)
            state = inspect(obj)
            if adapter.project is not None:
                require(
                    adapter.project(session, obj, actor) == actor.project_id,
                    "project_scope_mismatch",
                    403,
                )
            else:
                require(
                    hasattr(obj, "project_id") and obj.project_id == actor.project_id,
                    "project_scope_mismatch",
                    403,
                )
            if state.persistent and "project_id" in state.mapper.local_table.c:
                table = state.mapper.local_table
                old_project = (
                    session.connection()
                    .execute(
                        select(table.c.project_id).where(
                            *[
                                column == value
                                for column, value in zip(
                                    state.mapper.primary_key,
                                    state.identity,
                                    strict=True,
                                )
                            ]
                        )
                    )
                    .scalar_one()
                )
                require(old_project == actor.project_id, "project_scope_mismatch", 403)
            actual_resources = tuple(sorted(adapter.resources(obj, actor)))
            require(
                actual_resources == tuple(sorted(actor.resources)),
                "resource_scope_mismatch",
                403,
            )
            changes.append(
                {"operation": operation, "table": inspect(obj).mapper.local_table.name}
            )
    if not changes:
        return
    actor = current_actor(session)
    action = session.info.get("agriculture_action")
    require(isinstance(action, str) and action, "trusted_action_required", 403)
    # Connection shares the transaction that flushes business objects. If either
    # insert or business flush fails, neither can commit, even in legacy commit().
    session.connection().execute(
        ActorAudit.__table__.insert().values(
            id=str(uuid4()),
            tenant_id=actor.tenant_id,
            workspace_id=actor.workspace_id,
            actor_id=actor.stable_actor_id,
            project_id=actor.project_id,
            legacy_member_id=actor.legacy_member_id,
            legacy_user_code=actor.legacy_user_code,
            legacy_role=actor.legacy_role,
            policy_version=actor.policy_version,
            authorization_version=actor.authorization_version,
            request_id=actor.request_id,
            operation_id=actor.operation_id,
            action=action,
            resources=[{"kind": k, "id": i} for k, i in actor.resources],
            changes=changes,
        )
    )


@event.listens_for(AgricultureSession, "before_commit")
def check_commit(session):
    current_actor(session)


@event.listens_for(AgricultureSession, "after_commit")
@event.listens_for(AgricultureSession, "after_rollback")
def clear_actor(session):
    session.info.pop("agriculture_resolved_actor", None)
    session.info.pop("agriculture_action", None)
