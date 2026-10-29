"""Strict compact-JWS and request verification, independent of business handlers."""

import hashlib
import json
import re
from dataclasses import dataclass
from datetime import datetime

from .canonical import canonicalize
from .keys import KeyRing, b64decode, utc_second

BUSINESS_TYP = "weavesense-agri-delegation+jws"
SERVICE_TYP = "weavesense-agri-service+jws"
POLICY_VERSION = "agriculture-policy/1.0.0"
SAFE_INT = 9007199254740991
ID = re.compile(r"[A-Za-z][A-Za-z0-9_-]{0,127}\Z")
OPAQUE_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}\Z")


class IdentityError(Exception):
    def __init__(self, code: str, status: int = 401):
        self.code, self.status = code, status


def require(condition: bool, code: str = "invalid_delegation", status: int = 401):
    if not condition:
        raise IdentityError(code, status)


def resource_refs(value) -> list[dict]:
    require(isinstance(value, list) and 1 <= len(value) <= 200)
    keys = []
    for item in value:
        require(isinstance(item, dict) and set(item) == {"kind", "id"})
        require(
            isinstance(item["kind"], str)
            and item["kind"] in {"farm", "parcel", "asset", "job", "report"}
            and isinstance(item["id"], str)
            and ID.fullmatch(item["id"]) is not None
        )
        keys.append((item["kind"], item["id"]))
    require(keys == sorted(set(keys)))
    return value


def integer(value) -> bool:
    return type(value) is int and 1 <= value <= SAFE_INT


def validate_path(path: str, query: bytes):
    require(
        not query
        and isinstance(path, str)
        and re.fullmatch(r"/internal/agri/v1/[A-Za-z0-9_/-]+", path) is not None
    )
    require("//" not in path and not path.endswith("/"))


@dataclass(frozen=True)
class Verified:
    claims: dict
    expires_at: datetime
    body: dict


class Verifier:
    def __init__(self, keys: KeyRing):
        self.keys = keys

    def verify(
        self,
        token: str,
        *,
        typ: str,
        method: str,
        path: str,
        query: bytes,
        raw_body: bytes,
        now: datetime,
        action: str,
    ) -> Verified:
        try:
            return self._verify(
                token,
                typ=typ,
                method=method,
                path=path,
                query=query,
                raw_body=raw_body,
                now=now,
                action=action,
            )
        except IdentityError:
            raise
        except Exception:
            # Never propagate crypto/parser exceptions containing token material.
            raise IdentityError("invalid_delegation") from None

    def _verify(
        self,
        token: str,
        *,
        typ: str,
        method: str,
        path: str,
        query: bytes,
        raw_body: bytes,
        now: datetime,
        action: str,
    ) -> Verified:
        validate_path(path, query)
        require(typ in {BUSINESS_TYP, SERVICE_TYP})
        require(isinstance(token, str) and len(token) <= 196608)
        parts = token.split(".")
        require(len(parts) == 3)
        decoded_header, decoded_claims = b64decode(parts[0]), b64decode(parts[1])
        require(len(decoded_header) <= 2048 and len(decoded_claims) <= 131072)
        header = json.loads(canonicalize(decoded_header))
        require(
            set(header) == {"alg", "typ", "kid"}
            and header["alg"] == "EdDSA"
            and header["typ"] == typ
            and isinstance(header["kid"], str)
        )
        key = self.keys.require(header["kid"], now)
        key.public_key.verify(
            b64decode(parts[2]), (parts[0] + "." + parts[1]).encode("ascii")
        )
        claims = json.loads(canonicalize(decoded_claims))
        common = {
            "schema_version",
            "issuer",
            "subject",
            "audience",
            "tenant_id",
            "workspace_id",
            "issued_at",
            "expires_at",
            "jti",
            "trace_id",
            "request_id",
            "request",
        }
        extra = (
            {"capability"}
            if typ == SERVICE_TYP
            else {
                "actor",
                "resource_scope",
                "scopes",
                "authorization_version",
                "policy_version",
                "farm_ids",
                "resource_versions",
            }
        )
        require(set(claims) == common | extra)
        require(
            claims["schema_version"] == "1.0"
            and claims["issuer"] == claims["subject"] == "flow-platform"
            and claims["audience"] == "agri-gis"
        )
        for name in ("tenant_id", "workspace_id", "jti", "trace_id", "request_id"):
            require(
                isinstance(claims[name], str) and ID.fullmatch(claims[name]) is not None
            )
        issued, expires = (
            utc_second(claims["issued_at"]),
            utc_second(claims["expires_at"]),
        )
        require(
            0 < (expires - issued).total_seconds() <= 30
            and (issued - now).total_seconds() <= 5
            and now < expires
        )
        require(key.not_before <= issued < key.expires_at and expires <= key.expires_at)
        request = claims["request"]
        require(
            isinstance(request, dict)
            and request.get("method") == method
            and request.get("path") == path
        )
        if method in {"GET", "HEAD"}:
            require(set(request) == {"method", "path"} and not raw_body)
            body = {}
        else:
            require(method in {"POST", "PUT", "PATCH", "DELETE"})
            fields = {"method", "path", "canonicalization_version", "payload_hash"}
            if typ == BUSINESS_TYP:
                fields.add("operation_id")
                require(
                    isinstance(request.get("operation_id"), str)
                    and ID.fullmatch(request["operation_id"]) is not None
                )
            require(
                set(request) == fields
                and request["canonicalization_version"] == "rfc8785-safe-v1"
            )
            canonical = canonicalize(raw_body)
            require(request["payload_hash"] == hashlib.sha256(canonical).hexdigest())
            body = json.loads(canonical)
        if typ == SERVICE_TYP:
            require(action == "ownership.resolve" and claims["capability"] == action)
        else:
            require(
                claims["scopes"] == [action]
                and claims["policy_version"] == POLICY_VERSION
            )
            require(integer(claims["authorization_version"]))
            actor = claims["actor"]
            require(
                isinstance(actor, dict)
                and set(actor) == {"kind", "id"}
                and actor["kind"] == "user"
                and isinstance(actor["id"], str)
                and OPAQUE_ID.fullmatch(actor["id"]) is not None
            )
            refs = resource_refs(claims["resource_scope"])
            farms = claims["farm_ids"]
            require(
                isinstance(farms, list)
                and 1 <= len(farms) <= 200
                and all(isinstance(f, str) and ID.fullmatch(f) for f in farms)
            )
            require(farms == sorted(set(farms)))
            versions = claims["resource_versions"]
            require(isinstance(versions, list) and len(versions) == len(refs))
            for version, ref in zip(versions, refs, strict=True):
                require(
                    isinstance(version, dict)
                    and set(version) == {"resource", "revision"}
                    and version["resource"] == ref
                    and integer(version["revision"])
                )
        return Verified(claims, expires, body)
