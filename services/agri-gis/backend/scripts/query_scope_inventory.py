"""Reviewable database-call inventory; adding a call never silently opts it in."""

import argparse
import ast
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SNAPSHOT = ROOT / "app/agriculture/query_inventory.json"
EXECUTE = {"execute", "scalar", "scalars", "stream", "stream_scalars", "refresh"}


def inventory(root=ROOT):
    entries = []
    files = {}
    for path in sorted((root / "app").rglob("*.py")):
        data = path.read_bytes()
        tree = ast.parse(data)
        relative = path.relative_to(root).as_posix()
        files[relative] = hashlib.sha256(data).hexdigest()
        for function in ast.walk(tree):
            if not isinstance(function, ast.FunctionDef | ast.AsyncFunctionDef):
                continue
            calls = []

            def visit(node, function=function, calls=calls):
                if node is not function and isinstance(
                    node, ast.FunctionDef | ast.AsyncFunctionDef | ast.ClassDef
                ):
                    return
                if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute):
                    receiver = ast.unparse(node.func.value)
                    if node.func.attr in EXECUTE or (
                        node.func.attr in {"get", "query"}
                        and receiver
                        in {"db", "session", "connection", "conn", "self.db"}
                    ):
                        calls.append(
                            {
                                "line": node.lineno,
                                "receiver": receiver,
                                "method": node.func.attr,
                            }
                        )
                for child in ast.iter_child_nodes(node):
                    visit(child)

            visit(function)
            if not calls:
                continue
            if relative.startswith(("app/dao/", "app/services/")):
                boundary = "agricultural_factory_and_resource_rls_or_closed_domain"
            elif relative.startswith("app/agriculture/"):
                boundary = "explicit_identity_owner_or_internal_boundary_review"
            else:
                boundary = "legacy_entrypoint_not_registered_in_agriculture"
            entries.append(
                {
                    "path": relative,
                    "function": function.name,
                    "line": function.lineno,
                    "sha256": hashlib.sha256(data).hexdigest(),
                    "boundary": boundary,
                    "calls": calls,
                }
            )
    return {
        "schema_version": "1.0",
        "source_files": files,
        "entries": entries,
        "note": "Inventory is not open-API acceptance; real RLS tests are required.",
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--check", action="store_true")
    parser.add_argument("--write", action="store_true")
    args = parser.parse_args()
    if args.check == args.write:
        parser.error("choose exactly --check or --write after explicit source review")
    value = inventory()
    if args.write:
        SNAPSHOT.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
    elif not SNAPSHOT.exists() or json.loads(SNAPSHOT.read_text()) != value:
        raise SystemExit(
            "query inventory drift: review every new or changed database entry"
        )
    print(json.dumps({"passed": True, "entry_count": len(value["entries"])}))


if __name__ == "__main__":
    main()
