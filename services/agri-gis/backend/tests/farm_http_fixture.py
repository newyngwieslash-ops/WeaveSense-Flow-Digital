"""Owned test process composing actual farm routes, PostgreSQL and callbacks.

No extra domain route, fake ownership, seeded farm or token-minting endpoint is
installed. Private credentials are read once from a bounded 0600 config file.
"""

import asyncio
import base64
import ipaddress
import json
import os
import socket
import stat
import sys
import time
import traceback
from pathlib import Path
from urllib.parse import parse_qsl, urlsplit

import httpx
import uvicorn
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from domain_fixture_support import install_domain_schema

from app.agriculture.asset_catalog_config import configured_asset_rights
from app.agriculture.asset_catalog_identity import AssetCatalogAuthority
from app.agriculture.asset_catalog_objects import CatalogObjectStore
from app.agriculture.asset_catalog_rules import AssetCatalogRights
from app.agriculture.asset_catalog_store import AssetCatalogOwnership, AssetCatalogStore
from app.agriculture.asset_object_identity import AssetObjectAuthority
from app.agriculture.database import create_runtime_pool
from app.agriculture.domain_ownership import DomainOwnership
from app.agriculture.farms_config import FarmDictionary
from app.agriculture.farms_identity import FarmAuthority
from app.agriculture.farms_store import FarmOwnership, FarmStore
from app.agriculture.identity.callback import CallbackKey, PlatformAuthority
from app.agriculture.identity.dependency import IdentityServices
from app.agriculture.identity.keys import KeyRing, utc_second
from app.agriculture.identity.replay import PostgresReplayLedger
from app.agriculture.identity.verify import Verifier
from app.agriculture.main import create_app
from app.agriculture.object_storage import PrivateLocalStorage
from app.agriculture.object_store import PostgresObjectStore
from app.agriculture.parcels_identity import ParcelAuthority
from app.agriculture.parcels_store import ParcelOwnership, ParcelStore


def read_config(path):
    path = Path(path).absolute()
    if path.parent.resolve() != path.parent:
        raise ValueError("real private fixture directory required")
    private_fixture_path(path.parent.parent, path.parent.name, folder=True)
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, encoding="utf-8") as source:
        info = os.fstat(source.fileno())
        if (
            not stat.S_ISREG(info.st_mode)
            or stat.S_IMODE(info.st_mode) != 0o600
            or info.st_uid != os.getuid()
            or info.st_nlink != 1
            or info.st_size > 65536
        ):
            raise ValueError("private bounded configuration required")

        def unique(pairs):
            result = {}
            for key, item in pairs:
                if key in result:
                    raise ValueError("duplicate fixture configuration key")
                result[key] = item
            return result

        value = json.load(source, object_pairs_hook=unique)
    if not isinstance(value, dict):
        raise ValueError("fixture configuration object required")
    required = {
        "platform",
        "platform_keys",
        "callback_key",
        "geo_migration_dsn",
        "geo_runtime_dsn",
        "dictionary",
    }
    if set(value) not in (required, required | {"asset_object_fixture"}):
        raise ValueError("unknown configuration fields")
    if "asset_object_fixture" in value and value["asset_object_fixture"] is not True:
        raise ValueError("invalid owned object fixture")
    for key in ("geo_migration_dsn", "geo_runtime_dsn"):
        parsed = urlsplit(value[key])
        if (
            parsed.scheme not in {"postgres", "postgresql"}
            or not parsed.hostname
            or "%" in parsed.hostname
            or not ipaddress.ip_address(parsed.hostname).is_loopback
            or parsed.fragment
            or parse_qsl(parsed.query) not in ([], [("sslmode", "disable")])
        ):
            raise ValueError("literal loopback owned PostgreSQL required")
    target = urlsplit(value["platform"])
    if (
        target.scheme != "http"
        or not target.hostname
        or not ipaddress.ip_address(target.hostname).is_loopback
        or target.username
        or target.password
        or target.query
        or target.fragment
        or target.path
    ):
        raise ValueError("literal loopback callback required")
    return value


def private_fixture_path(directory, name, *, folder=False):
    path = directory / name
    info = path.lstat()
    if (
        info.st_uid != os.getuid()
        or stat.S_IMODE(info.st_mode) != (0o700 if folder else 0o600)
        or (
            not stat.S_ISDIR(info.st_mode) if folder else not stat.S_ISREG(info.st_mode)
        )
        or (not folder and (info.st_nlink != 1 or info.st_size > 65536))
    ):
        raise ValueError("owned object fixture path rejected")
    return path


class OwnedBarrierStorage:
    """Private subprocess test synchronization, never an HTTP control route."""

    def __init__(self, directory):
        self.directory = directory
        self.storage = PrivateLocalStorage(
            private_fixture_path(directory, "asset-storage", folder=True)
        )

    def read(self, **kwargs):
        body = self.storage.read(**kwargs)
        arm = self.directory / "asset-read-arm"
        if arm.exists():
            if private_fixture_path(self.directory, arm.name).read_bytes() != b"armed":
                raise ValueError("invalid read barrier")
            marker = self.directory / "asset-read-started"
            fd = os.open(
                marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600
            )
            with os.fdopen(fd, "wb") as stream:
                stream.write(b"read")
            deadline = time.monotonic() + 15
            release = self.directory / "asset-read-release"
            while not release.exists():
                if time.monotonic() >= deadline:
                    raise ValueError("read barrier deadline")
                time.sleep(0.01)
            if (
                private_fixture_path(self.directory, release.name).read_bytes()
                != b"release"
            ):
                raise ValueError("invalid read release")
        return body


async def main():
    config = read_config(sys.argv[1])
    await install_domain_schema(config["geo_migration_dsn"])
    pool = await create_runtime_pool(config["geo_runtime_dsn"])
    try:
        store = FarmStore(pool, FarmDictionary.from_document(config["dictionary"]))
        await store.require_ready()
        parcel_store = ParcelStore(pool)
        await parcel_store.require_ready()
        # Unknown source rights remain pending; this fixture never approves a
        # source or fabricates an entity merely to make metadata tests pass.
        rights = AssetCatalogRights()
        object_storage = None
        if config.get("asset_object_fixture"):
            directory = Path(sys.argv[1]).absolute().parent
            if directory.resolve() != directory:
                raise ValueError("real private fixture directory required")
            private_fixture_path(directory.parent, directory.name, folder=True)
            registry = private_fixture_path(directory, "asset-rights.json")
            scopes = private_fixture_path(directory, "asset-scopes.json")
            rights = configured_asset_rights(
                {
                    "AGRI_ASSET_RIGHTS_REGISTRY": str(registry),
                    "AGRI_ASSET_SOURCE_SCOPES": str(scopes),
                }
            )
            object_storage = OwnedBarrierStorage(directory)
        asset_catalog_store = AssetCatalogStore(pool, rights)
        await asset_catalog_store.require_ready()
        raw = config["callback_key"]
        key = CallbackKey(
            raw["kid"],
            Ed25519PrivateKey.from_private_bytes(
                base64.b64decode(raw["seed"], validate=True)
            ),
            utc_second(raw["not_before"]),
            utc_second(raw["not_after"]),
        )
        async with httpx.AsyncClient(trust_env=False, timeout=5) as client:
            services = IdentityServices(
                Verifier(KeyRing.from_config(config["platform_keys"])),
                ownership=DomainOwnership(
                    {"farm": FarmOwnership(pool), "parcel": ParcelOwnership(pool)}
                ),
                authority=PlatformAuthority(config["platform"], key, client),
                replay=PostgresReplayLedger(pool),
                farm_store=store,
                farm_authority=FarmAuthority(config["platform"], key, client),
                parcel_store=parcel_store,
                parcel_authority=ParcelAuthority(config["platform"], key, client),
                asset_catalog_store=asset_catalog_store,
                asset_catalog_authority=AssetCatalogAuthority(
                    config["platform"], key, client
                ),
                asset_catalog_ownership=AssetCatalogOwnership(pool),
                object_store=CatalogObjectStore(
                    PostgresObjectStore(pool), asset_catalog_store
                )
                if object_storage
                else None,
                object_storage=object_storage,
                asset_object_authority=AssetObjectAuthority(
                    config["platform"], key, client
                )
                if object_storage
                else None,
            )
            app = create_app(services)
            with socket.socket() as sock:
                sock.bind(("127.0.0.1", 0))
                sock.listen(128)
                print(
                    json.dumps({"port": sock.getsockname()[1], "farm_routes": True}),
                    flush=True,
                )
                server = uvicorn.Server(
                    uvicorn.Config(
                        app,
                        log_config=None,
                        log_level="critical",
                        access_log=False,
                        http="h11",
                        h11_max_incomplete_event_size=262144,
                    )
                )
                await server.serve(sockets=[sock])
    finally:
        await pool.close()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except Exception as error:
        frames = traceback.extract_tb(error.__traceback__)
        print(
            json.dumps(
                {
                    "code": "identity_chain_fixture_failed",
                    "type": type(error).__name__,
                    "file": Path(frames[-1].filename).name if frames else None,
                    "line": frames[-1].lineno if frames else None,
                }
            ),
            file=sys.stderr,
        )
        sys.exit(1)
