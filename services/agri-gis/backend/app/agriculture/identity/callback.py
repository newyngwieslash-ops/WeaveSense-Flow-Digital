"""One-way authoritative platform callback; never recurses through GIS ownership."""

import base64
import hashlib
import ipaddress
import json
import uuid
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from urllib.parse import urlsplit

import httpx
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

from .canonical import canonicalize
from .verify import SERVICE_TYP, IdentityError

CALLBACK_PATH = "/internal/agri/v1/authority/check"


def encode(raw: bytes) -> str:
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")


@dataclass(frozen=True)
class CallbackKey:
    kid: str
    private_key: Ed25519PrivateKey
    not_before: datetime
    not_after: datetime
    revoked: bool = False


class PlatformAuthority:
    def __init__(self, base_url: str, key: CallbackKey, client: httpx.AsyncClient):
        parsed = urlsplit(base_url)
        if (
            parsed.username
            or parsed.password
            or parsed.query
            or parsed.fragment
            or parsed.path not in {"", "/"}
            or not parsed.hostname
        ):
            raise ValueError("invalid configured platform origin")
        if "%" in parsed.hostname:
            raise ValueError("scoped callback addresses are not supported")
        loopback = False
        try:
            loopback = ipaddress.ip_address(parsed.hostname).is_loopback
        except ValueError:
            pass
        if parsed.scheme != "https" and not (parsed.scheme == "http" and loopback):
            raise ValueError("platform callback requires HTTPS or loopback")
        self.url, self.key, self.client = (
            base_url.rstrip("/") + CALLBACK_PATH,
            key,
            client,
        )

    async def check(
        self,
        actor_user_id: str,
        tenant_id: str,
        workspace_id: str,
        authorization_version: int,
    ) -> None:
        now = datetime.now(UTC).replace(microsecond=0)
        expires = min(now + timedelta(seconds=30), self.key.not_after)
        if self.key.revoked or not self.key.not_before <= now < expires:
            raise IdentityError("identity_dependency_unavailable", 503)
        body = canonicalize(
            json.dumps(
                {
                    "actor_user_id": actor_user_id,
                    "tenant_id": tenant_id,
                    "workspace_id": workspace_id,
                    "authorization_version": authorization_version,
                }
            ).encode()
        )
        claims = {
            "schema_version": "1.0",
            "issuer": "agri-gis",
            "subject": "agri-gis",
            "audience": "flow-platform",
            "capability": "authority.check",
            "tenant_id": tenant_id,
            "workspace_id": workspace_id,
            "issued_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "expires_at": expires.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "jti": "jti_" + uuid.uuid4().hex,
            "request_id": "req_" + uuid.uuid4().hex,
            "trace_id": "trace_" + uuid.uuid4().hex,
            "request": {
                "method": "POST",
                "path": CALLBACK_PATH,
                "canonicalization_version": "rfc8785-safe-v1",
                "payload_hash": hashlib.sha256(body).hexdigest(),
            },
        }
        header = {"alg": "EdDSA", "typ": SERVICE_TYP, "kid": self.key.kid}
        signing = (
            encode(canonicalize(json.dumps(header).encode()))
            + "."
            + encode(canonicalize(json.dumps(claims).encode()))
        )
        token = (
            signing + "." + encode(self.key.private_key.sign(signing.encode("ascii")))
        )
        try:
            async with self.client.stream(
                "POST",
                self.url,
                content=body,
                headers={
                    "Authorization": "Bearer " + token,
                    "Content-Type": "application/json",
                },
                follow_redirects=False,
                timeout=2,
            ) as response:
                if response.status_code in {401, 403, 404, 409}:
                    raise IdentityError("authority_denied", 403)
                if response.status_code != 200:
                    raise IdentityError("identity_dependency_unavailable", 503)
                raw = bytearray()
                async for chunk in response.aiter_bytes():
                    raw.extend(chunk)
                    if len(raw) > 4096:
                        raise IdentityError("identity_dependency_unavailable", 503)
                result = json.loads(canonicalize(bytes(raw)))
                if (
                    set(result) != {"active", "authorization_version"}
                    or result["active"] is not True
                    or type(result["authorization_version"]) is not int
                    or result["authorization_version"] != authorization_version
                ):
                    raise IdentityError("authority_denied", 403)
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("identity_dependency_unavailable", 503) from None
