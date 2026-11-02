"""Trusted composition of domain providers; no fallback or expanded scope."""

from types import MappingProxyType

from .identity.providers import Ownership, OwnershipProvider, resolve_owned
from .identity.verify import require, resource_refs


class DomainOwnership:
    def __init__(self, providers: dict[str, OwnershipProvider]):
        if not providers or not set(providers) <= {
            "farm",
            "parcel",
            "asset",
            "job",
            "report",
        }:
            raise ValueError("explicit domain ownership providers required")
        if any(
            not callable(getattr(provider, "resolve", None))
            for provider in providers.values()
        ):
            raise ValueError("invalid domain ownership provider")
        self._providers = MappingProxyType(dict(providers))

    async def resolve(
        self, tenant_id: str, workspace_id: str, resources: list[dict]
    ) -> list[Ownership]:
        refs = resource_refs(resources)
        # Reject an incomplete composition before calling any provider. Each
        # provider gets only its exact kind, and all original ownership checks
        # (including nonempty farms) remain in force for ordinary delegations.
        require(
            all(ref["kind"] in self._providers for ref in refs),
            "identity_dependency_unavailable",
            503,
        )
        groups: dict[str, list[dict]] = {}
        for ref in refs:
            groups.setdefault(ref["kind"], []).append(ref)
        indexed = {}
        for kind, group in groups.items():
            facts = await resolve_owned(
                self._providers[kind], tenant_id, workspace_id, group
            )
            indexed.update({(fact.kind, fact.resource_id): fact for fact in facts})
        return [indexed[(ref["kind"], ref["id"])] for ref in refs]
