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
import traceback
from pathlib import Path
from urllib.parse import parse_qsl, urlsplit

import httpx
import uvicorn
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from domain_fixture_support import install_domain_schema

from app.agriculture.database import create_runtime_pool
from app.agriculture.asset_catalog_identity import AssetCatalogAuthority
from app.agriculture.asset_catalog_rules import AssetCatalogRights
from app.agriculture.asset_catalog_store import AssetCatalogOwnership, AssetCatalogStore
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
from app.agriculture.parcels_identity import ParcelAuthority
from app.agriculture.parcels_store import ParcelOwnership, ParcelStore


def read_config(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, encoding="utf-8") as source:
        info = os.fstat(source.fileno())
        if (
            not stat.S_ISREG(info.st_mode)
            or stat.S_IMODE(info.st_mode) != 0o600
            or info.st_size > 65536
        ):
            raise ValueError("private bounded configuration required")
        value = json.load(source)
    if set(value) != {
        "platform",
        "platform_keys",
        "callback_key",
        "geo_migration_dsn",
        "geo_runtime_dsn",
        "dictionary",
    }:
        raise ValueError("unknown configuration fields")
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
        asset_catalog_store = AssetCatalogStore(pool, AssetCatalogRights())
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
                ownership=DomainOwnership({"farm": FarmOwnership(pool), "parcel": ParcelOwnership(pool)}),
                authority=PlatformAuthority(config["platform"], key, client),
                replay=PostgresReplayLedger(pool),
                farm_store=store,
                farm_authority=FarmAuthority(config["platform"], key, client),
                parcel_store=parcel_store,
                parcel_authority=ParcelAuthority(config["platform"], key, client),
                asset_catalog_store=asset_catalog_store,
                asset_catalog_authority=AssetCatalogAuthority(config["platform"], key, client),
                asset_catalog_ownership=AssetCatalogOwnership(pool),
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
