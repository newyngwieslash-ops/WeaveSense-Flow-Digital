"""Composition contracts, not substitutes for real domain database tests."""

import asyncio
from dataclasses import replace

import pytest

from app.agriculture.domain_ownership import DomainOwnership
from app.agriculture.identity.providers import Ownership
from app.agriculture.identity.verify import IdentityError


class Provider:
    def __init__(self, facts):
        self.facts, self.calls = facts, []

    async def resolve(self, tenant, workspace, refs):
        self.calls.append((tenant, workspace, refs))
        return self.facts


FARM = Ownership("farm", "farm_a", "tenant_a", "workspace_a", ("farm_a",), 1)
PARCEL = Ownership("parcel", "parcel_a", "tenant_a", "workspace_a", ("farm_a",), 2)


def resolve(provider, refs):
    return asyncio.run(provider.resolve("tenant_a", "workspace_a", refs))


def test_domain_dispatch_is_exact_and_preserves_requested_order():
    farm, parcel = Provider([FARM]), Provider([PARCEL])
    configured = {"farm": farm, "parcel": parcel}
    composed = DomainOwnership(configured)
    configured.clear()
    refs = [{"kind": "farm", "id": "farm_a"}, {"kind": "parcel", "id": "parcel_a"}]
    assert resolve(composed, refs) == [FARM, PARCEL]
    assert farm.calls[0][2] == [refs[0]]
    assert parcel.calls[0][2] == [refs[1]]


def test_missing_domain_does_not_call_other_domain_or_fall_back():
    farm = Provider([FARM])
    with pytest.raises(IdentityError) as error:
        resolve(DomainOwnership({"farm": farm}), [{"kind": "asset", "id": "asset_a"}, {"kind": "farm", "id": "farm_a"}])
    assert error.value.status == 503
    assert farm.calls == []


@pytest.mark.parametrize("facts", [[], [PARCEL, PARCEL], [replace(PARCEL, tenant_id="other")],
    [replace(PARCEL, workspace_id="other")], [replace(PARCEL, status="archived")],
    [replace(PARCEL, kind="asset")], [replace(PARCEL, farm_ids=())], [replace(PARCEL, revision=0)]])
def test_domain_result_cannot_weaken_ordinary_ownership_validation(facts):
    with pytest.raises(IdentityError):
        resolve(DomainOwnership({"parcel": Provider(facts)}), [{"kind": "parcel", "id": "parcel_a"}])


def test_empty_duplicate_or_unknown_resource_requests_rejected():
    provider = Provider([PARCEL])
    domain = DomainOwnership({"parcel": provider})
    for refs in ([], [{"kind": "parcel", "id": "parcel_a"}] * 2, [{"kind": "unknown", "id": "parcel_a"}],
                 [{"kind": "parcel", "id": "parcel_a"}, {"kind": "farm", "id": "farm_a"}]):
        with pytest.raises(IdentityError):
            resolve(domain, refs)
    assert provider.calls == []
