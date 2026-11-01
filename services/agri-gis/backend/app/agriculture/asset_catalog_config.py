"""Operator-only composition of the reviewed W00 rights contract."""

import hashlib
import json
import os
import stat
from pathlib import Path
from types import ModuleType

from .asset_catalog_rules import AssetCatalogRights

CONTRACT_HASHES = {
    "source-rights.py": (
        "117c28096e5d29e2831b5ad8590f6cd430"
        "17c92a2a57599f851e3feef17481c7"
    ),
    "source-rights.schema.json": (
        "543452243e26b328a996038dbfbd6024f"
        "ae10a36bec144c7a755be57293c257d"
    ),
}


def bounded_file(path: Path) -> bytes:
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, "rb") as source:
        info = os.fstat(source.fileno())
        if (
            not stat.S_ISREG(info.st_mode)
            or info.st_mode & 0o022
            or info.st_size > 2 * 1024 * 1024
        ):
            raise ValueError("invalid asset rights configuration")
        data = source.read(2 * 1024 * 1024 + 1)
        if len(data) > 2 * 1024 * 1024:
            raise ValueError("invalid asset rights configuration")
        return data


def json_document(path: Path):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate asset rights configuration key")
            result[key] = value
        return result

    return json.loads(bounded_file(path), object_pairs_hook=unique)


def configured_asset_rights(environment=None) -> AssetCatalogRights:
    env = os.environ if environment is None else environment
    registry, scopes = (
        env.get("AGRI_ASSET_RIGHTS_REGISTRY"),
        env.get("AGRI_ASSET_SOURCE_SCOPES"),
    )
    if not registry and not scopes:
        # Catalog registration remains possible; no source becomes approved.
        return AssetCatalogRights()
    if not registry or not scopes:
        raise ValueError("asset rights registry and scopes must be configured together")
    root = Path(
        env.get("AGRI_ASSET_RIGHTS_CONTRACT_ROOT")
        or Path(__file__).resolve().parents[5] / "contracts/agriculture"
    )

    def verified_contract():
        values = {name: bounded_file(root / name) for name in CONTRACT_HASHES}
        if any(
            hashlib.sha256(values[name]).hexdigest() != digest
            for name, digest in CONTRACT_HASHES.items()
        ):
            raise ValueError("asset rights contract release mismatch")
        return values["source-rights.py"]

    try:
        source = verified_contract()
        module = ModuleType("agriculture_reviewed_source_rights")
        module.__file__ = str(root / "source-rights.py")
        # Only exact release-hashed repository code can be loaded; environment
        # paths cannot select arbitrary Python or replace the rights engine.
        exec(compile(source, module.__file__, "exec"), module.__dict__)

        def load_registry():
            verified_contract()
            return json_document(Path(registry))

        def load_scopes():
            value = json_document(Path(scopes))
            if not isinstance(value, dict):
                raise ValueError("invalid asset source scopes")
            return value

        module.validate_registry(load_registry())
        load_scopes()
        return AssetCatalogRights(
            load_registry, load_scopes, module.authorize, module.validate_registry
        )
    except Exception:
        raise ValueError("asset rights configuration unavailable") from None
