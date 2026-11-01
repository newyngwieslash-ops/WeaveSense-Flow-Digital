"""Mounted private configuration without fixture switches or key discovery."""

import base64
import json
import os
from contextlib import asynccontextmanager
from pathlib import Path

import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from ..database import create_runtime_pool
from ..asset_catalog_config import configured_asset_rights
from ..asset_catalog_identity import AssetCatalogAuthority
from ..asset_catalog_store import AssetCatalogOwnership, AssetCatalogStore
from ..asset_catalog_objects import CatalogObjectStore
from ..asset_object_identity import AssetObjectAuthority
from ..domain_ownership import DomainOwnership
from ..farms_config import FarmDictionary
from ..farms_identity import FarmAuthority
from ..farms_store import FarmOwnership, FarmStore
from ..object_storage import PrivateLocalStorage
from ..object_store import PostgresObjectStore
from ..parcels_identity import ParcelAuthority
from ..parcels_store import ParcelOwnership, ParcelStore
from .callback import CallbackKey, PlatformAuthority
from .dependency import IdentityServices
from .keys import KeyRing, utc_second
from .replay import PostgresReplayLedger
from .verify import Verifier


@asynccontextmanager
async def configured_services():
    path = os.environ.get("AGRI_SERVICE_IDENTITY_CONFIG")
    if not path:
        yield IdentityServices(Verifier(KeyRing([])))
        return
    try:
        file = Path(path)
        if file.stat().st_mode & 0o077:
            raise ValueError("private configuration permissions")
        config = json.loads(file.read_text(encoding="utf-8"))
        if set(config) != {
            "platform_base_url",
            "platform_keys",
            "gis_signing_kid",
            "gis_keys",
        }:
            raise ValueError("configuration fields")
        ring = KeyRing.from_config(config["platform_keys"])
        records = {item["kid"]: item for item in config["gis_keys"]}
        if len(records) != len(config["gis_keys"]):
            raise ValueError("duplicate signing key id")
        item = records[config["gis_signing_kid"]]
        if (
            set(item) != {"kid", "private_seed", "not_before", "not_after", "revoked"}
            or type(item["revoked"]) is not bool
        ):
            raise ValueError("signing configuration fields")
        key = CallbackKey(
            item["kid"],
            Ed25519PrivateKey.from_private_bytes(
                base64.b64decode(item["private_seed"], validate=True)
            ),
            utc_second(item["not_before"]),
            utc_second(item["not_after"]),
            item["revoked"],
        )
    except Exception:
        raise RuntimeError("invalid agricultural identity configuration") from None
    pool = None
    async with httpx.AsyncClient(trust_env=False) as client:
        try:
            authority = PlatformAuthority(config["platform_base_url"], key, client)
            dsn = os.environ.get("AGRI_GEO_DATABASE_URL")
            if dsn:
                pool = await create_runtime_pool(dsn)
                await PostgresReplayLedger(pool).require_safe_runtime()
            object_store = object_storage = None
            farm_store = None
            parcel_store = None
            asset_catalog_store = None
            if pool is not None:
                asset_catalog_store = AssetCatalogStore(pool, configured_asset_rights())
                await asset_catalog_store.require_ready()
            farm_config = os.environ.get("AGRI_FARM_DICTIONARY_CONFIG")
            if pool is not None and farm_config:
                dictionary = FarmDictionary.from_file(farm_config)
                farm_store = FarmStore(pool, dictionary)
                await farm_store.require_ready()
                parcel_store = ParcelStore(pool)
                await parcel_store.require_ready()
            object_root = os.environ.get("AGRI_PRIVATE_OBJECT_ROOT")
            if object_root:
                if pool is None:
                    raise ValueError("private object database required")
                object_store = CatalogObjectStore(PostgresObjectStore(pool), asset_catalog_store)
                await object_store.require_ready()
                object_storage = PrivateLocalStorage(object_root)
            # Real GIS ownership is intentionally unconfigured until its domain
            # provider exists; neither config nor environment can select a fixture.
            yield IdentityServices(
                Verifier(ring),
                authority=authority,
                replay=PostgresReplayLedger(pool) if pool else None,
                object_store=object_store,
                object_storage=object_storage,
                ownership=DomainOwnership({"farm": FarmOwnership(pool), "parcel": ParcelOwnership(pool)}) if farm_store else None,
                farm_store=farm_store,
                farm_authority=FarmAuthority(config["platform_base_url"], key, client),
                parcel_store=parcel_store,
                parcel_authority=ParcelAuthority(config["platform_base_url"], key, client),
                asset_catalog_store=asset_catalog_store,
                asset_catalog_authority=AssetCatalogAuthority(config["platform_base_url"], key, client),
                asset_catalog_ownership=AssetCatalogOwnership(pool) if pool is not None else None,
                asset_object_authority=AssetObjectAuthority(config["platform_base_url"], key, client),
            )
        except Exception:
            raise RuntimeError(
                "agricultural identity dependency initialization failed"
            ) from None
        finally:
            if pool is not None:
                await pool.close()
