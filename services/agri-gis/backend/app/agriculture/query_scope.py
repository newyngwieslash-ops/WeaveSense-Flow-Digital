"""Verified query scope, intersected with independent owner-provisioned references.

References adapt legacy rows; they do not create farms, parcel identities or
geometry. No environment option installs a fixture provider in production.
"""

import json
from dataclasses import dataclass
from datetime import UTC, datetime

from sqlalchemy import column, table, text

from .identity.verify import ID, OPAQUE_ID, IdentityError, Verified, require

REFERENCE_TABLE = "agriculture_legacy_resource_references"
TASK_LOCATOR = table(
    "agriculture_query_task_locators",
    column("id"),
    column("task_code"),
    column("project_id"),
    schema="public",
)


def task_locator(db):
    """Only safe columns, unlike the task entity's project-wide counters."""
    from app.models.workbench import MonitoringTask

    from .database import ScopedAgricultureSession

    if isinstance(getattr(db, "sync_session", None), ScopedAgricultureSession):
        return TASK_LOCATOR.c
    # Retained isolated upstream application is not registered in agriculture.
    # The actual agricultural factory never supplies an ordinary legacy session.
    return MonitoringTask


@dataclass(frozen=True)
class QueryScope:
    tenant_id: str
    workspace_id: str
    actor_id: str
    resources: tuple[tuple[str, str], ...]
    farm_ids: tuple[str, ...]
    expires_at: datetime

    @classmethod
    def from_verified(cls, verified):
        require(isinstance(verified, Verified), "trusted_query_scope_required", 403)
        claims = verified.claims
        actor = claims.get("actor")
        require(
            isinstance(actor, dict)
            and actor.get("kind") == "user"
            and isinstance(actor.get("id"), str)
            and OPAQUE_ID.fullmatch(actor["id"]),
            "trusted_query_scope_required",
            403,
        )
        refs, farms = claims.get("resource_scope"), claims.get("farm_ids")
        require(
            isinstance(refs, list)
            and 1 <= len(refs) <= 200
            and all(
                isinstance(r, dict)
                and set(r) == {"kind", "id"}
                and r["kind"] in {"farm", "parcel"}
                and isinstance(r["id"], str)
                and ID.fullmatch(r["id"])
                for r in refs
            ),
            "trusted_query_scope_required",
            403,
        )
        require(
            isinstance(farms, list)
            and 1 <= len(farms) <= 200
            and all(isinstance(v, str) and ID.fullmatch(v) for v in farms)
            and len(set(farms)) == len(farms),
            "trusted_query_scope_required",
            403,
        )
        resources = tuple(sorted((r["kind"], r["id"]) for r in refs))
        require(
            len(set(resources)) == len(resources), "trusted_query_scope_required", 403
        )
        scope = cls(
            claims.get("tenant_id"),
            claims.get("workspace_id"),
            actor["id"],
            resources,
            tuple(sorted(farms)),
            verified.expires_at,
        )
        scope.validate()
        return scope

    def validate(self):
        require(
            all(
                isinstance(v, str) and ID.fullmatch(v)
                for v in (self.tenant_id, self.workspace_id)
            ),
            "trusted_query_scope_required",
            403,
        )
        require(
            isinstance(self.expires_at, datetime)
            and self.expires_at.tzinfo is not None
            and datetime.now(UTC) < self.expires_at,
            "delegation_expired",
        )

    def parameters(self):
        self.validate()
        return {
            "query_parcels": json.dumps(
                [v for k, v in self.resources if k == "parcel"]
            ),
            "query_farms": json.dumps([v for k, v in self.resources if k == "farm"]),
            "query_parent_farms": json.dumps(self.farm_ids),
            "query_actor": self.actor_id,
            "query_expiry": self.expires_at.isoformat(),
        }


async def validate_reference_scope(connection, scope):
    """Check every requested reference, not just the authorized subset of a batch."""
    scope.validate()
    try:
        rows = (
            await connection.execute(
                text("""
SELECT requested.kind,requested.id, min(r.farm_id) AS farm_id
FROM jsonb_to_recordset(CAST(:resources AS jsonb)) AS requested(kind text,id text)
LEFT JOIN public.agriculture_legacy_resource_references r
 ON r.tenant_id=:tenant AND r.workspace_id=:workspace AND r.state='active'
 AND ((requested.kind='parcel' AND requested.id=r.parcel_id)
   OR (requested.kind='farm' AND requested.id=r.farm_id))
GROUP BY requested.kind,requested.id
"""),
                {
                    "resources": json.dumps(
                        [{"kind": k, "id": v} for k, v in scope.resources]
                    ),
                    "tenant": scope.tenant_id,
                    "workspace": scope.workspace_id,
                },
            )
        ).all()
        require(
            len(rows) == len(scope.resources)
            and all(row.farm_id is not None for row in rows)
            and {row.farm_id for row in rows} == set(scope.farm_ids),
            "query_resource_scope_denied",
            403,
        )
        scope.validate()
    except IdentityError:
        raise
    except Exception:
        raise IdentityError("query_resource_scope_unavailable", 503) from None
