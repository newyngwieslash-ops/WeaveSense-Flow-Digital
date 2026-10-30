"""Independent GIS service assertion; fixed fresh-authority path only."""

import hashlib
import json
import uuid
from datetime import UTC, datetime, timedelta

from .identity.callback import CALLBACK_PATH, PlatformAuthority, encode
from .identity.canonical import canonicalize
from .identity.keys import utc_second
from .identity.verify import SERVICE_TYP, IdentityError, require
from .job_authority import PHASES, snapshot_bytes

JOB_CALLBACK_PATH = "/internal/agri/v1/jobs/authority/check"


class PlatformJobAuthority(PlatformAuthority):
    def __init__(self, base_url, key, client):
        super().__init__(base_url, key, client)
        self.url = self.url.removesuffix(CALLBACK_PATH) + JOB_CALLBACK_PATH

    async def check(self, snapshot, digest, phase):
        require(
            phase in PHASES
            and hashlib.sha256(snapshot_bytes(snapshot)).hexdigest() == digest,
            "invalid_job_scope",
            400,
        )
        now = datetime.now(UTC).replace(microsecond=0)
        expires = min(now + timedelta(seconds=30), self.key.not_after)
        require(
            not self.key.revoked and self.key.not_before <= now < expires,
            "job_authority_unavailable",
            503,
        )
        body = canonicalize(
            json.dumps(
                {"snapshot": snapshot, "snapshot_hash": digest, "phase": phase}
            ).encode()
        )
        claims = {
            "schema_version": "1.0",
            "issuer": "agri-gis",
            "subject": "agri-gis",
            "audience": "flow-platform",
            "capability": "job.authority.check",
            "tenant_id": snapshot["tenant_id"],
            "workspace_id": snapshot["workspace_id"],
            "issued_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "expires_at": expires.strftime("%Y-%m-%dT%H:%M:%SZ"),
            "jti": "jti_" + uuid.uuid4().hex,
            "request_id": "req_" + uuid.uuid4().hex,
            "trace_id": "trace_" + uuid.uuid4().hex,
            "request": {
                "method": "POST",
                "path": JOB_CALLBACK_PATH,
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
                    raise IdentityError("job_authority_denied", 403)
                require(response.status_code == 200, "job_authority_unavailable", 503)
                raw = bytearray()
                async for chunk in response.aiter_bytes():
                    raw.extend(chunk)
                    require(len(raw) <= 4096, "job_authority_unavailable", 503)
                result = json.loads(canonicalize(bytes(raw)))
                require(
                    set(result)
                    == {
                        "active",
                        "snapshot_hash",
                        "authorization_version",
                        "checked_at",
                        "valid_until",
                    }
                    and result["active"] is True
                    and result["snapshot_hash"] == digest
                    and type(result["authorization_version"]) is int
                    and result["authorization_version"]
                    == snapshot["authorization_version"],
                    "job_authority_denied",
                    403,
                )
                checked, until = (
                    utc_second(result["checked_at"]),
                    utc_second(result["valid_until"]),
                )
                require(
                    now - timedelta(seconds=5) <= checked <= datetime.now(UTC)
                    and datetime.now(UTC) < until <= checked + timedelta(seconds=30),
                    "job_authority_denied",
                    403,
                )
                return result
        except IdentityError:
            raise
        except Exception:
            raise IdentityError("job_authority_unavailable", 503) from None
