"""Trusted deployment composition; synthetic sources grant no real rights."""

import hashlib
import json
import shutil
from pathlib import Path

import pytest
from test_agriculture_asset_catalog_rules import metadata, registry

from app.agriculture.asset_catalog_config import (
    CONTRACT_HASHES,
    configured_asset_rights,
)
from app.agriculture.identity.verify import IdentityError

ROOT = Path(__file__).resolve().parents[4]


def configuration(tmp_path):
    root = tmp_path / "contract"
    root.mkdir()
    for name in CONTRACT_HASHES:
        shutil.copyfile(ROOT / "contracts/agriculture" / name, root / name)
    document = tmp_path / "registry.json"
    document.write_text(json.dumps(registry()))
    scopes = tmp_path / "scopes.json"
    scopes.write_text(
        json.dumps(
            {
                "fixture-source": [
                    {
                        "tenant_id": "tenant_a",
                        "workspace_id": "workspace_a",
                        "source_kind": "customer_data",
                    }
                ]
            }
        )
    )
    return {
        "AGRI_ASSET_RIGHTS_CONTRACT_ROOT": str(root),
        "AGRI_ASSET_RIGHTS_REGISTRY": str(document),
        "AGRI_ASSET_SOURCE_SCOPES": str(scopes),
    }


def test_catalog_policy_release_hashes_are_exact_repository_sources():
    for name, expected in CONTRACT_HASHES.items():
        assert (
            hashlib.sha256(
                (ROOT / "contracts/agriculture" / name).read_bytes()
            ).hexdigest()
            == expected
        )


def test_unconfigured_rights_remain_pending_and_deny_use():
    rights = configured_asset_rights({})
    item = metadata()
    item["source"]["source_id"] = None
    assert rights.snapshot("tenant_a", "workspace_a", item)["status"] == "pending"
    with pytest.raises(IdentityError):
        rights.check(
            "tenant_a", "workspace_a", item, purpose="analysis", profile="demo"
        )


def test_configured_policy_uses_actual_engine_and_reloads_revocation(tmp_path):
    env = configuration(tmp_path)
    rights = configured_asset_rights(env)
    assert (
        rights.snapshot("tenant_a", "workspace_a", metadata())["status"] == "approved"
    )
    rights.check(
        "tenant_a", "workspace_a", metadata(), purpose="analysis", profile="demo"
    )
    doc = registry()
    doc["sources"][0]["rights"]["status"] = "revoked"
    Path(env["AGRI_ASSET_RIGHTS_REGISTRY"]).write_text(json.dumps(doc))
    with pytest.raises(IdentityError):
        rights.check(
            "tenant_a", "workspace_a", metadata(), purpose="analysis", profile="demo"
        )


@pytest.mark.parametrize(
    "mutation", ["partial", "engine", "schema", "duplicate", "symlink", "writable"]
)
def test_configuration_rejects_partial_or_replaced_contracts(tmp_path, mutation):
    env = configuration(tmp_path)
    if mutation == "partial":
        env.pop("AGRI_ASSET_SOURCE_SCOPES")
    elif mutation in {"engine", "schema"}:
        path = Path(env["AGRI_ASSET_RIGHTS_CONTRACT_ROOT"]) / (
            "source-rights.py" if mutation == "engine" else "source-rights.schema.json"
        )
        path.write_text("SECRET unreviewed replacement")
    elif mutation == "duplicate":
        Path(env["AGRI_ASSET_SOURCE_SCOPES"]).write_text(
            '{"fixture-source":[],"fixture-source":[]}'
        )
    elif mutation == "symlink":
        link = tmp_path / "linked.json"
        link.symlink_to(env["AGRI_ASSET_RIGHTS_REGISTRY"])
        env["AGRI_ASSET_RIGHTS_REGISTRY"] = str(link)
    else:
        Path(env["AGRI_ASSET_RIGHTS_REGISTRY"]).chmod(0o666)
    with pytest.raises(ValueError) as error:
        configured_asset_rights(env)
    assert "SECRET" not in str(error.value)
    assert str(tmp_path) not in str(error.value)
