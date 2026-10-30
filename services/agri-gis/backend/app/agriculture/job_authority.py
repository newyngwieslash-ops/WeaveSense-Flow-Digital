"""Immutable job authority sidecar; deliberately not a scheduler or queue.

ExecutionProvider must independently validate the real queue's current lease.
Missing W06 production adapter is fail-closed. No worker message selects a DB role.
"""

import hashlib
import json
import re
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Protocol

from .identity.canonical import canonicalize
from .identity.keys import utc_second
from .identity.verify import (
    OPAQUE_ID,
    POLICY_VERSION,
    IdentityError,
    Verified,
    integer,
    require,
    resource_refs,
)

SCHEMA = "agriculture-job-authority/1"
HASH = re.compile(r"[0-9a-f]{64}\Z")
PHASES = {"claim", "read_input", "publish"}
FIELDS = {
    "schema_version",
    "job_id",
    "operation_id",
    "actor_user_id",
    "tenant_id",
    "workspace_id",
    "action",
    "resources",
    "resource_versions",
    "farm_ids",
    "authorization_version",
    "policy_version",
    "input_hash",
    "created_at",
}


def snapshot_bytes(value: dict) -> bytes:
    require(isinstance(value, dict) and set(value) == FIELDS, "invalid_job_scope", 400)
    for key in ("job_id", "operation_id", "actor_user_id", "tenant_id", "workspace_id"):
        require(
            isinstance(value[key], str) and OPAQUE_ID.fullmatch(value[key]),
            "invalid_job_scope",
            400,
        )
    require(
        value["schema_version"] == SCHEMA
        and value["action"] == "agri.analysis.submit"
        and value["policy_version"] == POLICY_VERSION
        and integer(value["authorization_version"]),
        "invalid_job_scope",
        400,
    )
    refs = resource_refs(value["resources"])
    versions = value["resource_versions"]
    require(
        isinstance(versions, list) and len(versions) == len(refs),
        "invalid_job_scope",
        400,
    )
    for ref, version in zip(refs, versions, strict=True):
        require(
            isinstance(version, dict)
            and set(version) == {"resource", "revision"}
            and version["resource"] == ref
            and integer(version["revision"]),
            "invalid_job_scope",
            400,
        )
    farms = value["farm_ids"]
    require(
        isinstance(farms, list)
        and 1 <= len(farms) <= 200
        and all(isinstance(item, str) and OPAQUE_ID.fullmatch(item) for item in farms)
        and farms == sorted(set(farms)),
        "invalid_job_scope",
        400,
    )
    require(
        isinstance(value["input_hash"], str) and HASH.fullmatch(value["input_hash"]),
        "invalid_job_scope",
        400,
    )
    try:
        created = utc_second(value["created_at"])
        require(created <= datetime.now(UTC), "invalid_job_scope", 400)
        raw = canonicalize(json.dumps(value).encode())
        require(len(raw) <= 131072, "invalid_job_scope", 400)
        return raw
    except IdentityError:
        raise
    except Exception:
        raise IdentityError("invalid_job_scope", 400) from None


def match_snapshot(verified: Verified, snapshot: dict):
    require(
        isinstance(verified, Verified) and verified.expires_at > datetime.now(UTC),
        "expired_job_identity",
        401,
    )
    snapshot_bytes(snapshot)
    claims = verified.claims
    require(
        claims["actor"] == {"kind": "user", "id": snapshot["actor_user_id"]}
        and claims["scopes"] == [snapshot["action"]],
        "job_scope_mismatch",
        403,
    )
    for key in (
        "tenant_id",
        "workspace_id",
        "authorization_version",
        "policy_version",
        "resource_versions",
        "farm_ids",
    ):
        require(claims[key] == snapshot[key], "job_scope_mismatch", 403)
    require(
        claims["resource_scope"] == snapshot["resources"], "job_scope_mismatch", 403
    )


@dataclass(frozen=True)
class JobCheckpoint:
    job_id: str
    snapshot_hash: str
    worker_id: str
    attempt: int
    fencing: int
    phase: str
    input_hash: str

    @classmethod
    def from_body(cls, body):
        require(
            isinstance(body, dict) and set(body) == set(cls.__dataclass_fields__),
            "invalid_job_checkpoint",
            400,
        )
        require(
            all(
                isinstance(body[k], str) and OPAQUE_ID.fullmatch(body[k])
                for k in ("job_id", "worker_id")
            )
            and all(
                isinstance(body[k], str) and HASH.fullmatch(body[k])
                for k in ("snapshot_hash", "input_hash")
            )
            and integer(body["attempt"])
            and integer(body["fencing"])
            and body["phase"] in PHASES,
            "invalid_job_checkpoint",
            400,
        )
        return cls(**body)


class JobExecutionProvider(Protocol):
    async def check(self, snapshot: dict, checkpoint: JobCheckpoint) -> None:
        """Read actual persisted lease/attempt/fencing/state; never trust message."""


class JobAuthorityCallback(Protocol):
    async def check(self, snapshot: dict, digest: str, phase: str) -> dict: ...


class JobAuthority:
    def __init__(
        self,
        store,
        callback: JobAuthorityCallback,
        execution: JobExecutionProvider | None = None,
    ):
        self.store, self.callback, self.execution = store, callback, execution

    async def accept(self, verified: Verified):
        body = verified.body
        require(set(body) == {"snapshot", "input"}, "invalid_job_scope", 400)
        snapshot = body["snapshot"]
        match_snapshot(verified, snapshot)
        require(
            verified.claims["request"]["operation_id"] == snapshot["operation_id"],
            "job_scope_mismatch",
            403,
        )
        digest = hashlib.sha256(snapshot_bytes(snapshot)).hexdigest()
        raw_input = canonicalize(json.dumps(body["input"]).encode())
        require(
            hashlib.sha256(raw_input).hexdigest() == snapshot["input_hash"],
            "job_input_mismatch",
            403,
        )
        # Network finishes before opening the append-only DB transaction.
        current = await self.callback.check(snapshot, digest, "claim")
        await self.store.insert(
            verified, snapshot, digest, utc_second(current["valid_until"])
        )
        return {"job_id": snapshot["job_id"], "snapshot_hash": digest}

    async def checkpoint(self, verified: Verified):
        checkpoint = JobCheckpoint.from_body(verified.body)
        snapshot = await self.store.get(verified, checkpoint.job_id)
        match_snapshot(verified, snapshot)
        require(
            verified.claims["request"]["operation_id"] == snapshot["operation_id"],
            "job_scope_mismatch",
            403,
        )
        digest = hashlib.sha256(snapshot_bytes(snapshot)).hexdigest()
        require(
            digest == checkpoint.snapshot_hash
            and snapshot["input_hash"] == checkpoint.input_hash,
            "job_scope_mismatch",
            403,
        )
        if self.execution is None:
            raise IdentityError("job_execution_adapter_unavailable", 503)
        await self.execution.check(snapshot, checkpoint)
        current = await self.callback.check(snapshot, digest, checkpoint.phase)
        # Recheck lease after remote work; a stolen lease cannot publish a check.
        await self.execution.check(snapshot, checkpoint)
        require(verified.expires_at > datetime.now(UTC), "expired_job_identity", 401)
        await self.store.record(verified, snapshot, checkpoint, current)
        # This is only a bounded authority check. Result publication still needs
        # queue's atomic fenced publish, never a check-then-unfenced write.
        return {
            "job_id": checkpoint.job_id,
            "snapshot_hash": digest,
            "phase": checkpoint.phase,
            "fencing": checkpoint.fencing,
            "valid_until": current["valid_until"],
        }
