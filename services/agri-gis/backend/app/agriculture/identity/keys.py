"""Configured Ed25519 keys: no token-provided URL or discovery mechanism."""

import base64
import re
from dataclasses import dataclass
from datetime import UTC, datetime

from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey


def utc_second(value: str) -> datetime:
    if not isinstance(value, str) or not re.fullmatch(
        r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z", value
    ):
        raise ValueError("invalid UTC second")
    return datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=UTC)


def b64decode(value: str) -> bytes:
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9_-]+", value):
        raise ValueError("invalid base64url")
    raw = base64.urlsafe_b64decode(value + "=" * (-len(value) % 4))
    if base64.urlsafe_b64encode(raw).rstrip(b"=").decode() != value:
        raise ValueError("noncanonical base64url")
    return raw


@dataclass(frozen=True)
class VerificationKey:
    kid: str
    public_key: Ed25519PublicKey
    not_before: datetime
    expires_at: datetime
    revoked: bool = False


class KeyRing:
    def __init__(self, keys: list[VerificationKey]):
        self._keys = {key.kid: key for key in keys}
        if len(self._keys) != len(keys):
            raise ValueError("duplicate configured key id")

    def require(self, kid: str, now: datetime) -> VerificationKey:
        key = self._keys.get(kid)
        if key is None or key.revoked or not key.not_before <= now < key.expires_at:
            raise ValueError("unavailable verification key")
        return key

    @classmethod
    def from_config(cls, records: list[dict]) -> "KeyRing":
        keys = []
        for record in records:
            if (
                set(record)
                != {"kid", "public_key", "not_before", "not_after", "revoked"}
                or type(record["revoked"]) is not bool
            ):
                raise ValueError("invalid configured verification key")
            if not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", record["kid"]):
                raise ValueError("invalid configured key id")
            keys.append(
                VerificationKey(
                    record["kid"],
                    Ed25519PublicKey.from_public_bytes(
                        base64.b64decode(record["public_key"], validate=True)
                    ),
                    utc_second(record["not_before"]),
                    utc_second(record["not_after"]),
                    record["revoked"],
                )
            )
        return cls(keys)
