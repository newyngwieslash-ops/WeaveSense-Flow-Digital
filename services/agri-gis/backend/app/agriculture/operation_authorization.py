"""Server-installed, operation-specific legacy resource adapters.

The production registry is empty. A resolver must join explicit stable-resource
bindings to the actual method inputs; copying token resources is not a resolver.
"""

import asyncio
from collections.abc import Awaitable, Callable, Mapping
from dataclasses import dataclass
from types import MappingProxyType

from sqlalchemy.ext.asyncio import AsyncSession

from .actors import TrustedActor
from .identity.verify import IdentityError, require


@dataclass(frozen=True)
class OperationFacts:
    tenant_id: str
    workspace_id: str
    project_id: int
    resources: tuple[tuple[str, str], ...]


@dataclass(frozen=True)
class OperationAdapter:
    operation: str
    capability: str
    action: str
    resolve: Callable[
        [AsyncSession, TrustedActor, Mapping[str, str]], Awaitable[OperationFacts]
    ]


async def authorize_operation(
    db: AsyncSession,
    actor: TrustedActor,
    capability: str,
    action: str,
    operation: str | None,
    targets: Mapping[str, str] | None,
) -> None:
    registry = db.info.get("agriculture_operation_adapters", ())
    require(
        isinstance(registry, tuple)
        and all(isinstance(a, OperationAdapter) for a in registry),
        "operation_adapter_unavailable",
        503,
    )
    matches = [a for a in registry if a.operation == operation]
    require(len(matches) == 1, "operation_adapter_unavailable", 503)
    adapter = matches[0]
    require(
        adapter.capability == capability and adapter.action == action,
        "operation_scope_mismatch",
        403,
    )
    require(
        isinstance(targets, Mapping)
        and 1 <= len(targets) <= 16
        and all(
            isinstance(k, str)
            and isinstance(v, str)
            and 0 < len(k) <= 80
            and 0 < len(v) <= 256
            for k, v in targets.items()
        ),
        "invalid_operation_targets",
        400,
    )
    try:
        facts = await asyncio.wait_for(
            adapter.resolve(db, actor, MappingProxyType(dict(targets))), timeout=2
        )
    except IdentityError:
        raise
    except Exception:
        raise IdentityError("operation_adapter_unavailable", 503) from None
    require(isinstance(facts, OperationFacts), "operation_adapter_unavailable", 503)
    require(
        isinstance(facts.resources, tuple)
        and 1 <= len(facts.resources) <= 200
        and all(
            isinstance(ref, tuple)
            and len(ref) == 2
            and all(isinstance(value, str) for value in ref)
            for ref in facts.resources
        ),
        "operation_adapter_unavailable",
        503,
    )
    require(
        (facts.tenant_id, facts.workspace_id, facts.project_id)
        == (actor.tenant_id, actor.workspace_id, actor.project_id)
        and facts.resources == tuple(sorted(set(facts.resources)))
        and facts.resources == tuple(sorted(actor.resources)),
        "operation_scope_mismatch",
        403,
    )
