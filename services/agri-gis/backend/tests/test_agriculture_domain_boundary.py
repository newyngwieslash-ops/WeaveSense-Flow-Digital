"""Machine coverage of the explicit migration inventory; not PostgreSQL proof."""

import json
import re
from collections import Counter
from pathlib import Path

import pytest

from app.agriculture import domain_boundary as boundary


def test_all_legacy_models_and_foreign_keys_are_in_explicit_inventory():
    assert len(boundary.validate_model_inventory()) == 93
    assert Counter(row["ownership_class"] for row in boundary.INVENTORY) == {
        "root": 1,
        "global_plot": 2,
        "project": 40,
        "task_only": 17,
        "parent_only": 33,
    }
    assert len(boundary.OPEN_TABLES) == 9
    assert len(boundary.LEGACY_TABLES - boundary.OPEN_TABLES) == 84


def test_missing_model_or_changed_parent_contract_fails_closed(monkeypatch):
    tables = boundary.actual_model_inventory()
    tables["unreviewed_model"] = tables["farmland_plots"]
    monkeypatch.setattr(boundary, "actual_model_inventory", lambda: tables)
    with pytest.raises(ValueError, match="inventory changed"):
        boundary.validate_model_inventory()


def test_migration_covers_inventory_and_restricts_existing_permissive_policies():
    sql = (
        Path(__file__).parents[1] / "scripts/migrations/"
        "20261030_agriculture_domain_rls.sql"
    ).read_text()
    expected = re.search(r"expected text\[\] := ARRAY\[(.*?)\];", sql, re.S)
    assert expected
    assert set(re.findall(r"'([a-z_]+)'", expected.group(1))) == boundary.LEGACY_TABLES
    for table in boundary.OPEN_TABLES | {boundary.OWNERSHIP_TABLE}:
        assert f"ON public.{table} AS RESTRICTIVE" in sql
    assert "ALTER DEFAULT PRIVILEGES FOR ROLE flow_geo_migrate" in sql
    assert "REVOKE ALL ON TABLES FROM flow_geo_app" in sql
    assert "pg_catalog.pg_depend" in sql and "d.deptype='e'" in sql
    # No auto-claim/backfill from task/plot intersections or default tenant.
    assert "INSERT INTO public.agriculture_legacy_plot_ownership" not in sql


def test_inventory_file_is_data_not_an_executable_policy_source():
    path = Path(boundary.__file__).with_name("domain_table_inventory.json")
    data = json.loads(path.read_text())
    assert data["schema_version"] == "1.0"
    assert all(
        set(row) == {"table", "ownership_class", "foreign_keys"}
        for row in data["legacy_tables"]
    )


def test_identity_metadata_requires_new_restrictive_single_scope():
    sql = (
        Path(__file__).parents[1] / "scripts/migrations/"
        "20261030_agriculture_domain_rls.sql"
    ).read_text()
    block = sql.split("DO $identity_scope$", 1)[1].split("END $identity_scope$", 1)[0]
    assert all("'" + name + "'" in block for name in boundary.IDENTITY_TABLES)
    assert "AS RESTRICTIVE TO flow_geo_app" in block
    assert "app.tenant_ids" in block and "app.workspace_id" in block
    assert "ANY(" not in block
