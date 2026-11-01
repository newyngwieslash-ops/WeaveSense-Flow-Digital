"""Authority protocol fixtures, not production jobs or agronomic evidence."""

import copy
import hashlib
import json
from datetime import UTC, datetime, timedelta

import pytest

from app.agriculture.identity.canonical import canonicalize
from app.agriculture.identity.verify import IdentityError, Verified
from app.agriculture.job_authority import (
    JobAuthority,
    JobCheckpoint,
    match_snapshot,
    snapshot_bytes,
)


def job_fixture(tenant="tenant_job", workspace="workspace_job"):
    now = datetime.now(UTC).replace(microsecond=0)
    data = {"algorithm": "ndvi", "revision": 1}
    snapshot = {
        "schema_version": "agriculture-job-authority/1",
        "job_id": "job_fixture",
        "operation_id": "operation_fixture",
        "actor_user_id": "actor_fixture",
        "tenant_id": tenant,
        "workspace_id": workspace,
        "action": "agri.analysis.submit",
        "resources": [{"kind": "asset", "id": "asset_fixture"}],
        "resource_versions": [
            {"resource": {"kind": "asset", "id": "asset_fixture"}, "revision": 1}
        ],
        "farm_ids": ["farm_fixture"],
        "authorization_version": 1,
        "policy_version": "agriculture-policy/1.0.0",
        "input_hash": hashlib.sha256(
            canonicalize(json.dumps(data).encode())
        ).hexdigest(),
        "created_at": now.strftime("%Y-%m-%dT%H:%M:%SZ"),
    }
    claims = {
        k: copy.deepcopy(snapshot[k])
        for k in (
            "tenant_id",
            "workspace_id",
            "resource_versions",
            "farm_ids",
            "authorization_version",
            "policy_version",
        )
    }
    claims.update(
        {
            "actor": {"kind": "user", "id": snapshot["actor_user_id"]},
            "resource_scope": snapshot["resources"],
            "scopes": [snapshot["action"]],
            "jti": "jti_fixture",
            "request": {"operation_id": snapshot["operation_id"]},
        }
    )
    verified = Verified(
        claims, now + timedelta(seconds=30), {"snapshot": snapshot, "input": data}
    )
    return snapshot, verified


def test_job_snapshot_has_no_implicit_scope_or_identity():
    snapshot, verified = job_fixture()
    match_snapshot(verified, snapshot)
    for field, value in [
        ("tenant_id", "other"),
        ("actor_user_id", "other"),
        ("workspace_id", "other"),
        ("authorization_version", 2),
        ("resources", [{"kind": "asset", "id": "other"}]),
    ]:
        mutated = copy.deepcopy(snapshot)
        mutated[field] = value
        with pytest.raises(IdentityError):
            match_snapshot(verified, mutated)
    with pytest.raises(IdentityError):
        snapshot_bytes({**snapshot, "operator_code": "admin"})
    with pytest.raises(IdentityError):
        match_snapshot(
            Verified(
                verified.claims, datetime.now(UTC) - timedelta(seconds=1), verified.body
            ),
            snapshot,
        )


def test_checkpoint_rejects_missing_fencing_and_message_identity():
    snapshot, _ = job_fixture()
    body = {
        "job_id": snapshot["job_id"],
        "snapshot_hash": hashlib.sha256(snapshot_bytes(snapshot)).hexdigest(),
        "input_hash": snapshot["input_hash"],
        "worker_id": "worker_fixture",
        "attempt": 1,
        "fencing": 1,
        "phase": "claim",
    }
    assert JobCheckpoint.from_body(body).attempt == 1
    for extra in (
        {"tenant_id": "other"},
        {"operator_code": "admin"},
        {"fencing": 0},
        {"attempt": True},
        {"phase": "system"},
    ):
        with pytest.raises(IdentityError):
            JobCheckpoint.from_body({**body, **extra})


@pytest.mark.asyncio
async def test_no_execution_adapter_never_claims_a_job():
    snapshot, verified = job_fixture()
    digest = hashlib.sha256(snapshot_bytes(snapshot)).hexdigest()

    class Store:
        async def get(self, identity, job_id):
            return copy.deepcopy(snapshot)

    body = {
        "job_id": snapshot["job_id"],
        "snapshot_hash": digest,
        "input_hash": snapshot["input_hash"],
        "worker_id": "worker_fixture",
        "attempt": 1,
        "fencing": 1,
        "phase": "claim",
    }
    component = JobAuthority(Store(), None)
    with pytest.raises(IdentityError) as failure:
        await component.checkpoint(Verified(verified.claims, verified.expires_at, body))
    assert failure.value.code == "job_execution_adapter_unavailable"
    assert failure.value.status == 503
