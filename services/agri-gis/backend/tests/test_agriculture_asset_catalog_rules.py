"""Synthetic rights evidence exercises the existing W00 engine, not a license grant."""

import copy
import importlib.util
from datetime import UTC, datetime
from pathlib import Path

import pytest

from app.agriculture.asset_catalog_rules import (
    AssetCatalogRights,
    validate_metadata,
    validate_successor,
)
from app.agriculture.identity.verify import IdentityError


def metadata():
    return {
        "name": "Synthetic scene",
        "kind": "raster",
        "data_class": "demo",
        "farm_id": None,
        "source": {
            "kind": "customer_data",
            "source_id": "fixture-source",
            "reference": "fixture:source",
        },
        "rights_declaration": {
            "owner": "Fixture claimant",
            "license_ref": "fixture-license",
            "permitted_uses": ["analysis", "ingest"],
            "attribution": {"required": True, "text": "Synthetic fixture"},
            "valid_from": "2020-01-01T00:00:00Z",
            "expiry": "2099-01-01T00:00:00Z",
        },
        "sensitivity": "internal",
        "retention": {"retain_until": "2099-01-01T00:00:00Z", "legal_hold": True},
    }


def registry():
    return {
        "schema_version": "1.0.0",
        "sources": [
            {
                "id": "fixture-source",
                "kind": "fixture",
                "enabled": True,
                "data_class": "demo",
                "source_ref": "fixture:source",
                "parent_ids": [],
                "rights": {
                    "status": "approved",
                    "owner": "Fixture author",
                    "license_ref": "fixture-license",
                    "commercial_use": "allowed",
                    "redistribution": "prohibited",
                    "permitted_uses": ["ingest", "analysis"],
                    "attribution": {"required": True, "text": "Synthetic fixture"},
                    "valid_from": "2020-01-01T00:00:00Z",
                    "expiry": "2099-01-01T00:00:00Z",
                    "review_ref": "fixture-review",
                    "evidence": [{"ref": "fixture-proof", "sha256": "1" * 64}],
                },
            }
        ],
    }


def reviewed(document):
    path = (
        Path(__file__).resolve().parents[4] / "contracts/agriculture/source-rights.py"
    )
    spec = importlib.util.spec_from_file_location("actual_w00_rights", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    scopes = {
        "fixture-source": [
            {
                "tenant_id": "tenant_a",
                "workspace_id": "workspace_a",
                "source_kind": "customer_data",
            }
        ]
    }
    return AssetCatalogRights(
        lambda: document, lambda: scopes, module.authorize, module.validate_registry
    )


def test_asset_external_share_public_is_necessary_not_sufficient():
    document = registry()
    value = metadata()
    value["rights_declaration"]["permitted_uses"].append("external_share")
    rights = reviewed(document)
    with pytest.raises(IdentityError):
        rights.check(
            "tenant_a", "workspace_a", value, purpose="external_share", profile="demo"
        )
    value["sensitivity"] = "public"
    with pytest.raises(IdentityError):
        rights.check(
            "tenant_a", "workspace_a", value, purpose="external_share", profile="demo"
        )
    document["sources"][0]["rights"]["permitted_uses"].append("external_share")
    with pytest.raises(IdentityError):
        rights.check(
            "tenant_a", "workspace_a", value, purpose="external_share", profile="demo"
        )
    document["sources"][0]["rights"]["redistribution"] = "allowed"
    rights.check(
        "tenant_a", "workspace_a", value, purpose="external_share", profile="demo"
    )


def test_asset_context_sources_are_current_exact_scope_and_safe_labels():
    document = registry()
    rights = reviewed(document)
    assert rights.source_options("tenant_b", "workspace_a") == []
    assert rights.source_options("tenant_a", "workspace_b") == []
    assert rights.source_options("tenant_a", "workspace_a") == [
        {
            "source_id": "fixture-source",
            "kind": "customer_data",
            "label": "fixture-source",
        }
    ]
    document["sources"][0]["rights"]["status"] = "revoked"
    assert rights.source_options("tenant_a", "workspace_a") == []
    with pytest.raises(IdentityError) as revoked:
        rights.snapshot("tenant_a", "workspace_a", metadata())
    assert revoked.value.code == "SOURCE_NOT_AVAILABLE"
    document["sources"][0]["rights"]["status"] = "approved"
    document["sources"][0]["rights"]["expiry"] = "2021-01-01T00:00:00Z"
    assert rights.source_options("tenant_a", "workspace_a") == []
    assert AssetCatalogRights().source_options("tenant_a", "workspace_a") == []
    value = metadata()
    value["source"]["source_id"] = "unknown-source"
    with pytest.raises(IdentityError) as error:
        rights.snapshot("tenant_a", "workspace_a", value)
    assert error.value.code == "SOURCE_NOT_AVAILABLE"


def test_catalog_declaration_cannot_self_approve_or_set_entity():
    item = metadata()
    assert validate_metadata(item) == item
    with pytest.raises(IdentityError) as error:
        AssetCatalogRights().snapshot("tenant_a", "workspace_a", item)
    assert error.value.code == "SOURCE_NOT_AVAILABLE"
    item["source"]["source_id"] = None
    assert (
        AssetCatalogRights().snapshot("tenant_a", "workspace_a", item)["status"]
        == "pending"
    )
    for extra in ["approved", "status", "object_key", "entity", "ready", "tenant_id"]:
        bad = copy.deepcopy(item)
        bad[extra] = True
        with pytest.raises(IdentityError):
            validate_metadata(bad)
    bad = copy.deepcopy(item)
    bad["rights_declaration"]["status"] = "approved"
    with pytest.raises(IdentityError):
        validate_metadata(bad)


@pytest.mark.parametrize(
    "field,value",
    [
        ("kind", {}),
        ("data_class", "production"),
        ("farm_id", "arbitrary"),
        ("sensitivity", []),
    ],
)
def test_catalog_invalid_metadata_is_safe_denial(field, value):
    item = metadata()
    item[field] = value
    with pytest.raises(IdentityError):
        validate_metadata(item)


def test_catalog_successor_cannot_release_scope_hold_retention_or_sensitivity():
    before = metadata()
    after = copy.deepcopy(before)
    after["name"] = "Same asset, new metadata"
    assert validate_successor(before, after)["name"] == after["name"]
    mutations = [
        lambda x: x.update(kind="photo"),
        lambda x: x.update(data_class="operational"),
        lambda x: x.update(farm_id="farm_" + "1" * 32),
        lambda x: x.update(sensitivity="public"),
        lambda x: x["retention"].update(legal_hold=False),
        lambda x: x["retention"].update(retain_until="2030-01-01T00:00:00Z"),
    ]
    for mutate in mutations:
        item = copy.deepcopy(after)
        mutate(item)
        with pytest.raises(IdentityError):
            validate_successor(before, item)


def test_catalog_real_w00_rules_reloaded_for_expiry_revocation_and_use():
    document = registry()
    policy = reviewed(document)
    item = metadata()
    now = datetime(2030, 1, 1, tzinfo=UTC)
    approved = policy.check(
        "tenant_a", "workspace_a", item, purpose="analysis", profile="demo", now=now
    )
    assert (
        approved["allowed"]
        and approved["attributions"][0]["text"] == "Synthetic fixture"
    )
    snapshot = policy.snapshot("tenant_a", "workspace_a", item)
    assert snapshot["status"] == "approved" and len(snapshot["snapshot_hash"]) == 64
    for purpose, profile in [("external_share", "demo"), ("analysis", "operational")]:
        with pytest.raises(IdentityError):
            policy.check(
                "tenant_a",
                "workspace_a",
                item,
                purpose=purpose,
                profile=profile,
                now=now,
            )
    for mutation in [
        lambda r: r.update(expiry="2029-01-01T00:00:00Z"),
        lambda r: r.update(status="revoked"),
        lambda r: r.update(permitted_uses=[]),
        lambda r: r.update(evidence=[]),
    ]:
        document["sources"][0]["rights"] = registry()["sources"][0]["rights"]
        mutation(document["sources"][0]["rights"])
        with pytest.raises(IdentityError):
            policy.check(
                "tenant_a",
                "workspace_a",
                item,
                purpose="analysis",
                profile="demo",
                now=now,
            )
    assert snapshot["reviewed_sources"][0]["status"] == "approved"
    assert set(snapshot["reviewed_sources"][0]) == {
        "source_id",
        "data_class",
        "status",
        "source_hash",
    }
    assert len(snapshot["reviewed_sources"][0]["source_hash"]) == 64


def test_catalog_reviewed_source_is_not_cross_scope_authority():
    policy = reviewed(registry())
    for tenant, workspace in [("tenant_b", "workspace_a"), ("tenant_a", "workspace_b")]:
        with pytest.raises(IdentityError):
            policy.snapshot(tenant, workspace, metadata())
    item = metadata()
    item["source"]["kind"] = "public_imagery"
    with pytest.raises(IdentityError):
        policy.snapshot("tenant_a", "workspace_a", item)
