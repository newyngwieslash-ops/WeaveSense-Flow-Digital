"""New source/table/query paths must receive an explicit inventory review."""

import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).parents[1]
SPEC = importlib.util.spec_from_file_location(
    "query_scope_inventory", ROOT / "scripts/query_scope_inventory.py"
)
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


def test_current_query_inventory_is_exact_reviewed_source():
    assert MODULE.inventory() == json.loads(MODULE.SNAPSHOT.read_text())


def test_service_direct_query_and_new_dynamic_source_change_inventory(tmp_path):
    service = tmp_path / "app/services/test.py"
    service.parent.mkdir(parents=True)
    service.write_text(
        "async def direct(db):\n    return await db.execute(statement)\n"
    )
    first = MODULE.inventory(tmp_path)
    assert first["entries"][0]["function"] == "direct"
    assert first["entries"][0]["calls"][0]["method"] == "execute"
    service.write_text(
        "async def dynamic(db):\n    run=getattr(db,'execute')\n"
        "    return await run(statement)\n"
    )
    second = MODULE.inventory(tmp_path)
    assert first != second
    assert first["source_files"] != second["source_files"]
    added = tmp_path / "app/new_unreviewed_route.py"
    added.write_text("def future_route():\n    return 1\n")
    assert MODULE.inventory(tmp_path) != second
