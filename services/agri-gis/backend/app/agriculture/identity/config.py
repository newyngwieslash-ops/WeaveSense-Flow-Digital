"""Mounted private configuration without fixture switches or key discovery."""

import base64
import json
import os
from contextlib import asynccontextmanager
from pathlib import Path

import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from ..database import create_runtime_pool
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
            # Real GIS ownership is intentionally unconfigured until its domain
            # provider exists; neither config nor environment can select a fixture.
            yield IdentityServices(
                Verifier(ring),
                authority=authority,
                replay=PostgresReplayLedger(pool) if pool else None,
            )
        except Exception:
            raise RuntimeError(
                "agricultural identity dependency initialization failed"
            ) from None
        finally:
            if pool is not None:
                await pool.close()
