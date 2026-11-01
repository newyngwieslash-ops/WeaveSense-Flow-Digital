"""Typed scope component tests; synthetic verification is not a signed HTTP test."""

from dataclasses import FrozenInstanceError
from types import SimpleNamespace

import pytest
from sqlalchemy import func, literal_column, select, text
from test_agriculture_actors import ticket

from app.agriculture.actors import ACTOR_BINDING_READINESS
from app.agriculture.database import reject_scope_mutating_queries
from app.agriculture.identity.verify import IdentityError
from app.agriculture.query_scope import QueryScope


def proof():
    value = ticket()
    value.claims["farm_ids"] = ["farm_a"]
    return value


def test_query_scope_copies_claims_and_cannot_be_rebound():
    value = proof()
    scope = QueryScope.from_verified(value)
    value.claims["farm_ids"].append("farm_other")
    value.claims["resource_scope"][0]["id"] = "farm_other"
    assert scope.resources == (("farm", "farm_a"),)
    assert scope.farm_ids == ("farm_a",)
    with pytest.raises(FrozenInstanceError):
        scope.resources = (("farm", "farm_other"),)


@pytest.mark.parametrize(
    "refs",
    [
        None,
        [],
        [{"kind": "asset", "id": "asset_a"}],
        [{"kind": "parcel", "id": "a,b"}],
        [{"kind": "farm", "id": "farm_a", "tenant": "other"}],
        [{"kind": "farm", "id": "farm_a"}] * 2,
    ],
)
def test_empty_unsupported_duplicate_or_client_scope_denied(refs):
    value = proof()
    value.claims["resource_scope"] = refs
    with pytest.raises(IdentityError):
        QueryScope.from_verified(value)


@pytest.mark.parametrize(
    "statement",
    [
        select(func.set_config("app.query_farms", '["other"]', True)),
        select(func.pg_catalog.set_config("app.tenant_ids", "other", True)),
        select(literal_column("set_config('app.query_farms','[]',true)")),
        select(text("set_config('app.query_parcels','[]',true)")),
        select(func.count()).where(
            text("set_config('app.tenant_ids','other',true)='other'")
        ),
    ],
)
def test_orm_and_core_scope_mutation_is_not_a_select_loophole(statement):
    with pytest.raises(IdentityError):
        reject_scope_mutating_queries(SimpleNamespace(statement=statement))


def test_parameterized_spatial_aggregation_is_not_blocked():
    reject_scope_mutating_queries(SimpleNamespace(statement=select(func.count())))
    reject_scope_mutating_queries(
        SimpleNamespace(statement=select(func.ST_MakePoint(1, 2)))
    )


def test_only_exact_internal_readiness_statement_allows_text():
    fixed = select(text(ACTOR_BINDING_READINESS))
    reject_scope_mutating_queries(SimpleNamespace(statement=fixed))
    for statement in (
        fixed.add_columns(text("set_config('app.query_farms','[]',true)")),
        fixed.where(text("set_config('app.tenant_ids','other',true)='other'")),
        select(text(ACTOR_BINDING_READINESS + " OR true")),
    ):
        with pytest.raises(IdentityError):
            reject_scope_mutating_queries(SimpleNamespace(statement=statement))
