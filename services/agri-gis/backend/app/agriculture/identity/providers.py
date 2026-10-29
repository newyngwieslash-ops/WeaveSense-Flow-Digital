"""Domain-owned facts and authoritative epoch interfaces. No fixture fallback."""

import asyncio
from dataclasses import dataclass
from typing import Protocol

from .verify import ID, IdentityError, integer, require


@dataclass(frozen=True)
class Ownership:
    kind: str
    resource_id: str
    tenant_id: str
    workspace_id: str
    farm_ids: tuple[str, ...]
    revision: int
    status: str = "active"
    assigned_actor_id: str | None = None


class OwnershipProvider(Protocol):
    async def resolve(
        self, tenant_id: str, workspace_id: str, resources: list[dict]
    ) -> list[Ownership]: ...


class AuthorityProvider(Protocol):
    async def check(
        self,
        actor_user_id: str,
        tenant_id: str,
        workspace_id: str,
        authorization_version: int,
    ) -> None: ...


async def resolve_owned(
    provider: OwnershipProvider | None, tenant: str, workspace: str, refs: list[dict]
) -> list[Ownership]:
    if provider is None:
        raise IdentityError("identity_dependency_unavailable", 503)
    try:
        facts = await asyncio.wait_for(
            provider.resolve(tenant, workspace, refs), timeout=2
        )
        require(len(facts) == len(refs), "resource_not_found", 404)
        indexed = {(f.kind, f.resource_id): f for f in facts}
        require(len(indexed) == len(refs), "resource_not_found", 404)
        ordered = []
        for ref in refs:
            fact = indexed.get((ref["kind"], ref["id"]))
            require(
                fact is not None
                and fact.tenant_id == tenant
                and fact.workspace_id == workspace
                and fact.status == "active",
                "resource_not_found",
                404,
            )
            require(
                integer(fact.revision)
                and 1 <= len(fact.farm_ids) <= 200
                and tuple(sorted(set(fact.farm_ids))) == fact.farm_ids
                and all(ID.fullmatch(f) for f in fact.farm_ids),
                "identity_dependency_unavailable",
                503,
            )
            require(
                fact.kind != "farm" or fact.farm_ids == (fact.resource_id,),
                "identity_dependency_unavailable",
                503,
            )
            ordered.append(fact)
        return ordered
    except IdentityError:
        raise
    except Exception:
        raise IdentityError("identity_dependency_unavailable", 503) from None
