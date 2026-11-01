"""Real role/RLS/domain persistence fixtures, NOT HTTP authorization acceptance.

Verified objects here are explicit test inputs. Signature, preview, platform
current-authority callback and initial grant delivery require the separate
Go/GIS integration suite; this module never substitutes for those checks.
"""

import asyncio
import copy
import hashlib
import json
from contextlib import asynccontextmanager
from datetime import UTC, datetime, timedelta
from decimal import Decimal
from uuid import uuid4

import asyncpg
import pytest
from domain_fixture_support import install_domain_schema
from test_agriculture_actors_postgres import pg_config
from test_agriculture_farms_config import dictionary_document
from test_agriculture_farms_geometry import rectangle

from app.agriculture.database import create_runtime_pool
from app.agriculture.farms_config import FarmDictionary
from app.agriculture.farms_store import FarmOwnership, FarmStore, digest
from app.agriculture.identity.verify import IdentityError, Verified
from app.agriculture.migrate import migrate_schema

TABLES = (
    "agriculture_farms",
    "agriculture_farm_revisions",
    "agriculture_farm_commands",
    "agriculture_farm_outbox",
    "agriculture_farm_dictionaries",
)


def farm_fields(code="fixture_farm"):
    return {
        "external_code": code,
        "name": "Synthetic farm",
        "operator_name": "Fixture operator",
        "contact": {
            "name": "Fixture contact",
            "email": "fixture@example.invalid",
            "phone": "",
        },
        "region_code": "south",
        "dictionary_version": "fixture-1",
        "timezone": "UTC",
        "area_unit": "ha",
        "geometry": rectangle(),
        "source_crs": "EPSG:4326",
        "coordinate_order": "longitude_latitude",
        "data_class": "demo",
    }


def identity(tenant=None, workspace=None):
    return Verified(
        {
            "tenant_id": tenant or "tenant_" + uuid4().hex,
            "workspace_id": workspace or "workspace_" + uuid4().hex,
            "actor": {"kind": "user", "id": "fixture_actor"},
        },
        datetime.now(UTC) + timedelta(seconds=30),
        {},
    )


def command(
    kind="farm.create", *, operation=None, farm=None, revision=None, reason=None
):
    body = {
        "operation_id": operation or "operation_" + uuid4().hex,
        "grant_plan_hash": "1" * 64 if kind == "farm.create" else "0" * 64,
    }
    if farm is not None:
        body["farm"] = farm
    elif kind == "farm.create":
        body["farm"] = farm_fields()
    if revision is not None:
        body["expected_revision"] = revision
    if reason is not None:
        body["reason"] = reason
    body["request_hash"] = digest({"kind": kind, **body})
    return body


def farm_input():
    """Shared explicitly synthetic input for isolated route integration setup."""
    return farm_fields("canary_" + uuid4().hex)


def fixtureproof(tenant="tenant_a", workspace="workspace_a"):
    """Test-only scope input; not a signature or an authorization assertion."""
    return identity(tenant=tenant, workspace=workspace)


async def create_canary(store, tenant="tenant_a", workspace="workspace_a"):
    value = farm_input()
    config = store.dictionary.snapshot()
    region = config["regions"][0]
    value.update(
        dictionary_version=config["dictionary_version"],
        region_code=region["code"],
        timezone=region["timezones"][0],
        area_unit=region["area_units"][0],
    )
    return await store.execute(
        fixtureproof(tenant, workspace), "farm.create", command(farm=value)
    )


async def snapshot(owner, tenant):
    # Only a digest leaves the assertion helper, never raw rows/contact data.
    rows = {}
    for table in TABLES:
        rows[table] = await owner.fetchval(
            "SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),"
            "'[]'::jsonb)::text "
            f"FROM {table} t WHERE tenant_id=$1",
            tenant,
        )
    return hashlib.sha256(json.dumps(rows, sort_keys=True).encode()).hexdigest()


@asynccontextmanager
async def setup():
    owner_dsn, runtime_dsn = pg_config()
    await install_domain_schema(owner_dsn)
    owner = await asyncpg.connect(owner_dsn)
    pool = await create_runtime_pool(runtime_dsn, min_size=1, max_size=2)
    store = FarmStore(pool, FarmDictionary.from_document(dictionary_document()))
    try:
        await store.require_ready()
        yield store, owner, pool, identity(), owner_dsn
    finally:
        await pool.close()
        await owner.close()


@pytest.mark.asyncio
async def test_farm_store_create_geometry_receipt_outbox_dictionary_atomic():
    async with setup() as (store, owner, _pool, actor, _dsn):
        body = command()
        receipt = await store.execute(actor, "farm.create", body)
        farm = await store.get(actor, receipt["farm_id"])
        assert Decimal(farm["area_m2"]) == Decimal("1158452.1897")
        assert Decimal(farm["area_ha"]) == Decimal(farm["area_m2"]) / 10000
        assert Decimal(farm["area_mu"]) == Decimal(farm["area_m2"]) * 3 / 2000
        assert farm["data_class"] == "demo" and farm["revision"] == 1
        assert farm["source_crs"] == "EPSG:4326"
        assert farm["coordinate_order"] == "longitude_latitude"
        assert receipt["operation_id"] == body["operation_id"]
        assert (
            receipt["producer"] == "agri-gis"
            and receipt["event_type"] == "agri.farm.created"
        )
        assert receipt["payload_hash"] == digest(
            {k: v for k, v in receipt.items() if k != "payload_hash"}
        )
        for table in TABLES:
            assert (
                await owner.fetchval(
                    f"SELECT count(*) FROM {table} WHERE tenant_id=$1",
                    actor.claims["tenant_id"],
                )
                == 1
            )
        command_row = await owner.fetchrow(
            "SELECT receipt_json FROM agriculture_farm_commands WHERE operation_id=$1",
            body["operation_id"],
        )
        event = await owner.fetchrow(
            "SELECT payload_json,payload_hash FROM agriculture_farm_outbox "
            "WHERE operation_id=$1",
            body["operation_id"],
        )
        assert json.loads(command_row["receipt_json"]) == receipt
        assert json.loads(event["payload_json"]) == receipt
        assert event["payload_hash"] == receipt["payload_hash"]
        stored = await owner.fetchrow(
            "SELECT document,document_hash FROM agriculture_farm_dictionaries "
            "WHERE tenant_id=$1",
            actor.claims["tenant_id"],
        )
        assert json.loads(stored["document"]) == dictionary_document()
        assert stored["document_hash"] == store.dictionary.sha256
        provenance = json.loads(
            await owner.fetchval(
                "SELECT provenance FROM agriculture_farms WHERE farm_id=$1", farm["id"]
            )
        )
        assert provenance["transform"]["operation"] == "identity"
        facts = await FarmOwnership(store.pool).resolve(
            actor.claims["tenant_id"],
            actor.claims["workspace_id"],
            [{"kind": "farm", "id": farm["id"]}],
        )
        assert len(facts) == 1 and facts[0].farm_ids == (farm["id"],)


@pytest.mark.asyncio
async def test_farm_store_receipt_idempotency_and_payload_conflicts():
    async with setup() as (store, owner, _pool, actor, _dsn):
        body = command()
        receipt = await store.execute(actor, "farm.create", body)
        before = await snapshot(owner, actor.claims["tenant_id"])
        results = await asyncio.gather(
            *(store.execute(actor, "farm.create", body) for _ in range(4))
        )
        assert all(result == receipt for result in results)
        assert (
            await store.receipt(
                actor,
                body["operation_id"],
                body["request_hash"],
                body["grant_plan_hash"],
            )
            == receipt
        )
        for changed in (
            {**body, "request_hash": "2" * 64},
            {**body, "grant_plan_hash": "3" * 64},
        ):
            with pytest.raises(IdentityError) as error:
                await store.execute(actor, "farm.create", changed)
            assert error.value.status == 409
        with pytest.raises(IdentityError) as error:
            await store.execute(actor, "farm.archive", body, farm_id=receipt["farm_id"])
        assert error.value.status == 409
        other_workspace = identity(tenant=actor.claims["tenant_id"])
        with pytest.raises(IdentityError) as error:
            await store.execute(other_workspace, "farm.create", body)
        assert error.value.status == 409
        assert await snapshot(owner, actor.claims["tenant_id"]) == before


@pytest.mark.asyncio
async def test_farm_store_duplicate_external_code_concurrent_atomic():
    async with setup() as (store, owner, _pool, actor, _dsn):
        results = await asyncio.gather(
            store.execute(actor, "farm.create", command()),
            store.execute(actor, "farm.create", command()),
            return_exceptions=True,
        )
        assert sum(isinstance(item, dict) for item in results) == 1
        denied = [item for item in results if not isinstance(item, dict)]
        assert (
            len(denied) == 1
            and isinstance(denied[0], IdentityError)
            and denied[0].status == 409
        )
        for table in TABLES:
            assert (
                await owner.fetchval(
                    f"SELECT count(*) FROM {table} WHERE tenant_id=$1",
                    actor.claims["tenant_id"],
                )
                == 1
            )


@pytest.mark.asyncio
async def test_farm_store_rls_cross_tenant_workspace_and_pool_reset():
    async with setup() as (store, owner, pool, actor, _dsn):
        body = command()
        receipt = await store.execute(actor, "farm.create", body)
        before = await snapshot(owner, actor.claims["tenant_id"])
        async with pool.acquire() as connection:
            assert not await connection.fetchval(
                "SELECT rolsuper OR rolbypassrls FROM pg_roles "
                "WHERE rolname=current_user"
            )
            for table in TABLES:
                assert await connection.fetchval(f"SELECT count(*) FROM {table}") == 0
        for other in (
            identity(workspace=actor.claims["workspace_id"]),
            identity(tenant=actor.claims["tenant_id"]),
        ):
            with pytest.raises(IdentityError) as error:
                await store.get(other, receipt["farm_id"])
            assert error.value.status == 404
            with pytest.raises(IdentityError) as error:
                await store.receipt(
                    other,
                    body["operation_id"],
                    body["request_hash"],
                    body["grant_plan_hash"],
                )
            assert error.value.status == 404
            listing = await store.list(
                other, [receipt["farm_id"]], "active", 50, "", ""
            )
            assert listing["items"] == []
            assert (
                await FarmOwnership(pool).resolve(
                    other.claims["tenant_id"],
                    other.claims["workspace_id"],
                    [{"kind": "farm", "id": receipt["farm_id"]}],
                )
                == []
            )
        for _ in range(4):
            assert (await store.get(actor, receipt["farm_id"]))["id"] == receipt[
                "farm_id"
            ]
            async with pool.acquire() as connection:
                assert (
                    await connection.fetchval(
                        "SELECT coalesce(current_setting('app.tenant_ids',true),'')"
                    )
                    == ""
                )
                assert (
                    await connection.fetchval("SELECT count(*) FROM agriculture_farms")
                    == 0
                )
        assert await snapshot(owner, actor.claims["tenant_id"]) == before


@pytest.mark.asyncio
async def test_farm_store_expected_revision_race_archive_and_history():
    async with setup() as (store, owner, _pool, actor, _dsn):
        receipt = await store.execute(actor, "farm.create", command())
        farm_id = receipt["farm_id"]
        original_snapshot = json.loads(
            await owner.fetchval(
                "SELECT snapshot_json FROM agriculture_farm_revisions "
                "WHERE farm_id=$1 AND revision=1",
                farm_id,
            )
        )
        changed_geometry = copy.deepcopy(farm_fields()["geometry"])
        for point in changed_geometry["coordinates"][0]:
            point[0] += 1
        updates = [
            command(
                "farm.update",
                farm={
                    "name": name,
                    "geometry": changed_geometry,
                    "source_crs": "OGC:CRS84",
                },
                revision=1,
            )
            for name in ("first", "second")
        ]
        results = await asyncio.gather(
            *(
                store.execute(actor, "farm.update", value, farm_id=farm_id)
                for value in updates
            ),
            return_exceptions=True,
        )
        assert sum(isinstance(item, dict) for item in results) == 1
        rejected = [item for item in results if not isinstance(item, dict)]
        assert (
            len(rejected) == 1
            and isinstance(rejected[0], IdentityError)
            and rejected[0].code == "REVISION_CONFLICT"
        )
        assert (await store.get(actor, farm_id))["revision"] == 2
        archive = command("farm.archive", revision=2, reason="fixture archive")
        archived = await store.execute(actor, "farm.archive", archive, farm_id=farm_id)
        assert (
            archived["revision"] == 3 and archived["event_type"] == "agri.farm.archived"
        )
        with pytest.raises(IdentityError) as error:
            await store.get(actor, farm_id)
        assert error.value.status == 404
        history = await store.get(actor, farm_id, archived=True)
        assert history["status"] == "archived" and history["revision"] == 3
        assert (
            await FarmOwnership(store.pool).resolve(
                actor.claims["tenant_id"],
                actor.claims["workspace_id"],
                [{"kind": "farm", "id": farm_id}],
            )
            == []
        )
        assert (await store.list(actor, [farm_id], "archived", 50, "", ""))["items"][0][
            "id"
        ] == farm_id
        rows = await owner.fetch(
            "SELECT revision,snapshot_json FROM agriculture_farm_revisions "
            "WHERE farm_id=$1 ORDER BY revision",
            farm_id,
        )
        assert [row["revision"] for row in rows] == [1, 2, 3]
        snapshots = [json.loads(row["snapshot_json"]) for row in rows]
        assert snapshots[0] == original_snapshot
        assert snapshots[0]["name"] == "Synthetic farm"
        assert snapshots[0]["provenance"]["source_crs"] == "EPSG:4326"
        assert snapshots[1]["provenance"]["source_crs"] == "OGC:CRS84"
        assert snapshots[0]["geometry_hash"] != snapshots[1]["geometry_hash"]
        for item in snapshots:
            assert item["provenance"]["geometry_hash"] == item["geometry_hash"]
            assert item["provenance"]["transform"]["operation"] == "identity"
            assert (
                item["provenance"]["area_algorithm"] == "postgis-geography-spheroid/1"
            )
            assert item["provenance"]["algorithm_version"]
        assert snapshots[2]["geometry_hash"] == snapshots[1]["geometry_hash"]
        assert snapshots[2]["provenance"] == snapshots[1]["provenance"]
        assert [item["change"] for item in snapshots] == [
            {"operation_kind": "farm.create", "reason": ""},
            {"operation_kind": "farm.update", "reason": ""},
            {"operation_kind": "farm.archive", "reason": "fixture archive"},
        ]
        before = await snapshot(owner, actor.claims["tenant_id"])
        with pytest.raises(IdentityError):
            await store.execute(
                actor,
                "farm.update",
                command("farm.update", farm={"name": "restore"}, revision=3),
                farm_id=farm_id,
            )
        assert await snapshot(owner, actor.claims["tenant_id"]) == before


@pytest.mark.asyncio
async def test_farm_store_bad_changes_expiry_and_dictionary_drift_no_writes():
    async with setup() as (store, owner, _pool, actor, _dsn):
        receipt = await store.execute(actor, "farm.create", command())
        farm_id = receipt["farm_id"]
        before = await snapshot(owner, actor.claims["tenant_id"])
        for changes in (
            {"external_code": "replacement"},
            {"data_class": "operational"},
            {"tenant_id": "forged"},
        ):
            with pytest.raises(IdentityError) as error:
                await store.execute(
                    actor,
                    "farm.update",
                    command("farm.update", farm=changes, revision=1),
                    farm_id=farm_id,
                )
            assert error.value.status == 400
        with pytest.raises(IdentityError) as error:
            await store.execute(
                actor,
                "farm.archive",
                command("farm.archive", reason="missing revision"),
                farm_id=farm_id,
            )
        assert error.value.status == 428
        expired = Verified(
            copy.deepcopy(actor.claims), datetime.now(UTC) - timedelta(seconds=1), {}
        )
        with pytest.raises(IdentityError) as error:
            await store.get(expired, farm_id)
        assert error.value.status == 401
        changed = dictionary_document()
        changed["regions"][0]["name"] = "Changed same version"
        drift = FarmStore(store.pool, FarmDictionary.from_document(changed))
        with pytest.raises(IdentityError) as error:
            await drift.execute(
                actor, "farm.create", command(farm=farm_fields("another"))
            )
        assert error.value.code == "DICTIONARY_VERSION_CONFLICT"
        assert await snapshot(owner, actor.claims["tenant_id"]) == before


@pytest.mark.asyncio
async def test_farm_store_database_identity_and_append_only_guards():
    async with setup() as (store, owner, pool, actor, _dsn):
        receipt = await store.execute(actor, "farm.create", command())
        before = await snapshot(owner, actor.claims["tenant_id"])
        for column, value in (
            ("data_class", "operational"),
            ("external_code", "changed"),
        ):
            with pytest.raises(asyncpg.CheckViolationError):
                async with store.transaction(actor) as connection:
                    await connection.execute(
                        f"UPDATE agriculture_farms SET {column}=$1,"
                        "revision=revision+1,state_revision=state_revision+1 "
                        "WHERE farm_id=$2",
                        value,
                        receipt["farm_id"],
                    )
        for table in TABLES[1:]:
            for sql in (
                f"DELETE FROM {table} WHERE tenant_id=$1",
                f"UPDATE {table} SET tenant_id=tenant_id WHERE tenant_id=$1",
            ):
                with pytest.raises(asyncpg.CheckViolationError):
                    await owner.execute(sql, actor.claims["tenant_id"])
            async with pool.acquire() as runtime:
                with pytest.raises(asyncpg.InsufficientPrivilegeError):
                    await runtime.execute(f"TRUNCATE {table}")
        with pytest.raises(asyncpg.CheckViolationError):
            await owner.execute(
                "DELETE FROM agriculture_farms WHERE farm_id=$1", receipt["farm_id"]
            )
        assert await snapshot(owner, actor.claims["tenant_id"]) == before


@pytest.mark.asyncio
async def test_farm_store_real_migration_replay_preserves_data_and_rls():
    async with setup() as (store, owner, pool, actor, owner_dsn):
        receipt = await store.execute(actor, "farm.create", command())
        before = await snapshot(owner, actor.claims["tenant_id"])
        first = await migrate_schema(owner_dsn, mode="existing")
        second = await migrate_schema(owner_dsn, mode="existing")
        assert first["migrations"] == second["migrations"]
        assert any(
            row["name"] == "20261101_agriculture_farms.sql"
            for row in first["migrations"]
        )
        await store.require_ready()
        assert (await store.get(actor, receipt["farm_id"]))["revision"] == 1
        assert await snapshot(owner, actor.claims["tenant_id"]) == before
        async with pool.acquire() as connection:
            assert (
                await connection.fetchval("SELECT count(*) FROM agriculture_farms") == 0
            )
