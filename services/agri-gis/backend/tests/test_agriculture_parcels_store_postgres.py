"""Actual PostgreSQL storage fixtures, NOT signed HTTP authorization acceptance.

Verified scope objects are test-only inputs. Platform sessions, approvals,
callbacks and current resource grants require the separate integration chain.
"""

import asyncio
import copy
import hashlib
import json
from contextlib import asynccontextmanager
from datetime import datetime, timedelta
from decimal import Decimal
from uuid import uuid4

import asyncpg
import pytest
from test_agriculture_farms_geometry import rectangle
from test_agriculture_farms_store_postgres import command as farm_command
from test_agriculture_farms_store_postgres import farm_fields, identity
from test_agriculture_farms_store_postgres import setup as farm_setup

from app.agriculture.farms_store import digest
from app.agriculture.identity.verify import IdentityError
from app.agriculture.parcels_store import ParcelOwnership, ParcelStore

TABLES = (
    "agriculture_parcels",
    "agriculture_parcel_revisions",
    "agriculture_parcel_commands",
    "agriculture_parcel_outbox",
)


def parcel_input():
    return {
        "external_code": "parcel_fixture",
        "name": "Synthetic parcel",
        "geometry": rectangle(110.001, 20.001, 0.002),
        "source_crs": "EPSG:4326",
        "coordinate_order": "longitude_latitude",
    }


def command(*, revise=False, expected=1, geometry=None):
    body = {
        "operation_id": "pop_" + uuid4().hex,
        "expected_revision": expected if revise else 0,
        "reason": "synthetic revision" if revise else "synthetic create",
    }
    if revise:
        body.update(
            geometry=geometry or rectangle(110.004, 20.004, 0.002),
            source_crs="OGC:CRS84",
            coordinate_order="longitude_latitude",
        )
    else:
        body.update(expected_farm_revision=1, parcel=parcel_input())
    body["request_hash"] = digest(body)
    return body


async def snapshot(owner, tenant):
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
async def setup(*, hole=False):
    async with farm_setup() as (farms, owner, pool, actor, dsn):
        value = farm_fields("parent_" + uuid4().hex)
        if hole:
            value["geometry"]["coordinates"].append(
                rectangle(110.004, 20.004, 0.002)["coordinates"][0]
            )
        farm = await farms.execute(actor, "farm.create", farm_command(farm=value))
        store = ParcelStore(pool)
        await store.require_ready()
        yield store, owner, pool, actor, farm["farm_id"], dsn


@pytest.mark.asyncio
async def test_parcel_store_create_atomic_idempotent_receipt_and_spheroid():
    async with setup() as (store, owner, _pool, actor, farm_id, _dsn):
        body = command()
        receipts = await asyncio.gather(
            *(
                store.execute(actor, "parcel.create", body, farm_id=farm_id)
                for _ in range(3)
            )
        )
        assert receipts[0] == receipts[1] == receipts[2]
        receipt = receipts[0]
        assert receipt["previous_geometry_revision"] == 0
        assert receipt["event_type"] == "agri.parcel.created"
        assert receipt["payload_hash"] == digest(
            {k: v for k, v in receipt.items() if k != "payload_hash"}
        )
        assert (
            await store.receipt(actor, body["operation_id"], body["request_hash"])
            == receipt
        )
        dto = await store.read(actor, receipt["parcel_id"])
        facts = await ParcelOwnership(store.pool).resolve(
            actor.claims["tenant_id"],
            actor.claims["workspace_id"],
            [{"kind": "parcel", "id": receipt["parcel_id"]}],
        )
        assert len(facts) == 1 and facts[0].revision == 1
        assert facts[0].farm_ids == (farm_id,)
        assert dto["data_class"] == "demo" and dto["geometry_revision"] == 1
        assert dto["farm_id"] == farm_id and dto["valid_to"] is None
        assert dto["geometry_hash"] == receipt["geometry_hash"]
        listing = await store.list(actor, farm_id)
        assert set(listing) == {"items", "has_more", "next_after_id"}
        assert listing["has_more"] is False and listing["next_after_id"] == ""
        assert len(listing["items"]) == 1
        item = listing["items"][0]
        assert set(item) == {
            "id",
            "workspace_id",
            "farm_id",
            "name",
            "external_code",
            "status",
            "data_class",
            "geometry_revision",
            "current_geometry_revision",
            "geometry_hash",
            "area_m2",
            "area_ha",
            "area_mu",
            "valid_from",
        }
        assert item["workspace_id"] == actor.claims["workspace_id"]
        assert item["geometry_revision"] == item["current_geometry_revision"] == 1
        assert item["id"] == dto["id"] and item["farm_id"] == farm_id
        assert (await store.list(actor, farm_id, parcel_ids=["parcel_" + "f" * 32]))[
            "items"
        ] == []
        assert Decimal(dto["area_m2"]) > 40000
        assert Decimal(dto["area_ha"]) == Decimal(dto["area_m2"]) / 10000
        for table in TABLES:
            assert (
                await owner.fetchval(
                    f"SELECT count(*) FROM {table} WHERE tenant_id=$1",
                    actor.claims["tenant_id"],
                )
                == 1
            )
        persisted = await owner.fetchrow(
            "SELECT payload_json,payload_hash FROM agriculture_parcel_outbox "
            "WHERE parcel_id=$1",
            dto["id"],
        )
        assert json.loads(persisted["payload_json"]) == receipt
        assert persisted["payload_hash"] == receipt["payload_hash"]


@pytest.mark.asyncio
async def test_parcel_store_concurrent_revision_history_exact_time_and_immutability():
    async with setup() as (store, owner, _pool, actor, farm_id, _dsn):
        created = await store.execute(
            actor, "parcel.create", command(), farm_id=farm_id
        )
        parcel_id = created["parcel_id"]
        first = await store.read(actor, parcel_id)
        old_row = await owner.fetchval(
            "SELECT to_jsonb(r)::text FROM agriculture_parcel_revisions r "
            "WHERE parcel_id=$1 AND revision=1",
            parcel_id,
        )
        results = await asyncio.gather(
            *(
                store.execute(
                    actor, "parcel.revise", command(revise=True), parcel_id=parcel_id
                )
                for _ in range(2)
            ),
            return_exceptions=True,
        )
        assert sum(isinstance(x, dict) for x in results) == 1
        errors = [x for x in results if isinstance(x, Exception)]
        assert (
            len(errors) == 1
            and isinstance(errors[0], IdentityError)
            and errors[0].status == 409
        )
        second = await store.read(actor, parcel_id)
        facts = await ParcelOwnership(store.pool).resolve(
            actor.claims["tenant_id"],
            actor.claims["workspace_id"],
            [{"kind": "parcel", "id": parcel_id}],
        )
        assert len(facts) == 1 and facts[0].revision == 2
        assert (
            second["geometry_revision"] == 2
            and second["geometry_hash"] != first["geometry_hash"]
        )
        assert (
            await owner.fetchval(
                "SELECT to_jsonb(r)::text FROM agriculture_parcel_revisions r "
                "WHERE parcel_id=$1 AND revision=1",
                parcel_id,
            )
            == old_row
        )
        historic = await store.read(actor, parcel_id, selected_revision=1)
        assert historic["geometry_hash"] == first["geometry_hash"]
        assert historic["provenance"] == first["provenance"]
        assert historic["valid_to"] == second["valid_from"]
        assert (await store.read(actor, parcel_id, at=second["valid_from"]))[
            "geometry_revision"
        ] == 2
        just_before = datetime.fromisoformat(
            second["valid_from"].replace("Z", "+00:00")
        ) - timedelta(microseconds=1)
        assert (
            await store.read(
                actor, parcel_id, at=just_before.isoformat().replace("+00:00", "Z")
            )
        )["geometry_revision"] == 1
        page = await store.history(actor, parcel_id, limit=1)
        assert page["has_more"] and page["next_after_revision"] == 1
        assert page["items"][0]["valid_to"] == second["valid_from"]
        end = await store.history(actor, parcel_id, after_revision=1, limit=1)
        assert not end["has_more"] and end["items"][0]["geometry_revision"] == 2


@pytest.mark.asyncio
async def test_parcel_store_scope_denials_and_pool_reuse():
    async with setup() as (store, owner, pool, actor, farm_id, _dsn):
        body = command()
        receipt = await store.execute(actor, "parcel.create", body, farm_id=farm_id)
        before = await snapshot(owner, actor.claims["tenant_id"])
        for wrong in (
            identity(tenant=actor.claims["tenant_id"]),
            identity(workspace=actor.claims["workspace_id"]),
        ):
            assert (
                await ParcelOwnership(pool).resolve(
                    wrong.claims["tenant_id"],
                    wrong.claims["workspace_id"],
                    [{"kind": "parcel", "id": receipt["parcel_id"]}],
                )
                == []
            )
            for call in (
                lambda wrong=wrong: store.list(wrong, farm_id),
                lambda wrong=wrong: store.read(wrong, receipt["parcel_id"]),
                lambda wrong=wrong: store.history(wrong, receipt["parcel_id"]),
                lambda wrong=wrong: store.receipt(
                    wrong, body["operation_id"], body["request_hash"]
                ),
            ):
                with pytest.raises(IdentityError) as exc:
                    await call()
                assert exc.value.status == 404
        for _ in range(3):
            await store.read(actor, receipt["parcel_id"])
            async with pool.acquire() as conn:
                assert not await conn.fetchval(
                    "SELECT rolsuper OR rolbypassrls FROM pg_roles "
                    "WHERE rolname=current_user"
                )
                for table in TABLES:
                    assert await conn.fetchval(f"SELECT count(*) FROM {table}") == 0
                assert (
                    await conn.fetchval(
                        "SELECT coalesce(current_setting('app.tenant_ids',true),'')"
                    )
                    == ""
                )
        assert await snapshot(owner, actor.claims["tenant_id"]) == before
        assert (
            await ParcelOwnership(pool).resolve(
                actor.claims["tenant_id"],
                actor.claims["workspace_id"],
                [{"kind": "parcel", "id": "parcel_" + "f" * 32}],
            )
            == []
        )
        # Real owner fixture transition proves active-parent filtering, not an
        # HTTP authorization or permission to archive through the product API.
        await owner.execute(
            "UPDATE agriculture_farms SET status='archived',revision=revision+1,"
            "state_revision=state_revision+1 WHERE farm_id=$1",
            farm_id,
        )
        assert (
            await ParcelOwnership(pool).resolve(
                actor.claims["tenant_id"],
                actor.claims["workspace_id"],
                [{"kind": "parcel", "id": receipt["parcel_id"]}],
            )
            == []
        )


@pytest.mark.asyncio
async def test_parcel_store_hole_outside_and_valid_multipolygon():
    async with setup(hole=True) as (store, owner, _pool, actor, farm_id, _dsn):
        before = await snapshot(owner, actor.claims["tenant_id"])
        for geometry in (rectangle(111, 21), rectangle(110.0042, 20.0042, 0.001)):
            body = command()
            body["parcel"]["geometry"] = geometry
            with pytest.raises(IdentityError) as exc:
                await store.execute(actor, "parcel.create", body, farm_id=farm_id)
            assert exc.value.code == "PARCEL_OUTSIDE_FARM" and exc.value.status == 422
        assert await snapshot(owner, actor.claims["tenant_id"]) == before
        body = command()
        body["parcel"]["geometry"] = {
            "type": "MultiPolygon",
            "coordinates": [
                rectangle(110.001, 20.001, 0.001)["coordinates"],
                rectangle(110.007, 20.007, 0.001)["coordinates"],
            ],
        }
        receipt = await store.execute(actor, "parcel.create", body, farm_id=farm_id)
        assert (
            len(
                (await store.read(actor, receipt["parcel_id"]))["geometry"][
                    "coordinates"
                ]
            )
            == 2
        )


@pytest.mark.asyncio
async def test_parcel_store_invalid_inputs_and_external_conflict_no_partial_writes():
    async with setup() as (store, owner, _pool, actor, farm_id, _dsn):
        before = await snapshot(owner, actor.claims["tenant_id"])
        missing = command()
        missing.pop("expected_revision")
        wrong_crs = command()
        wrong_crs["parcel"]["source_crs"] = "EPSG:3857"
        for body, status in ((missing, 428), (wrong_crs, 422)):
            with pytest.raises(IdentityError) as exc:
                await store.execute(actor, "parcel.create", body, farm_id=farm_id)
            assert exc.value.status == status
        assert await snapshot(owner, actor.claims["tenant_id"]) == before
        await store.execute(actor, "parcel.create", command(), farm_id=farm_id)
        before = await snapshot(owner, actor.claims["tenant_id"])
        with pytest.raises(IdentityError) as exc:
            await store.execute(actor, "parcel.create", command(), farm_id=farm_id)
        assert exc.value.status == 409
        assert await snapshot(owner, actor.claims["tenant_id"]) == before


@pytest.mark.asyncio
async def test_parcel_store_receipt_wrong_actor_hash_farm_and_parcel():
    async with setup() as (store, owner, _pool, actor, farm_id, _dsn):
        body = command()
        await store.execute(actor, "parcel.create", body, farm_id=farm_id)
        before = await snapshot(owner, actor.claims["tenant_id"])
        other = copy.deepcopy(actor)
        other.claims["actor"]["id"] = "other_fixture"
        calls = [
            lambda: store.receipt(other, body["operation_id"], body["request_hash"]),
            lambda: store.receipt(actor, body["operation_id"], "0" * 64),
            lambda: store.execute(
                actor, "parcel.create", body, farm_id="farm_" + "f" * 32
            ),
            lambda: store.execute(
                actor,
                "parcel.create",
                body,
                farm_id=farm_id,
                parcel_id="parcel_" + "f" * 32,
            ),
        ]
        for call in calls:
            with pytest.raises(IdentityError) as exc:
                await call()
            assert exc.value.status == 409
        assert await snapshot(owner, actor.claims["tenant_id"]) == before


@pytest.mark.asyncio
async def test_parcel_store_database_append_only_pointer_and_identity_guards():
    async with setup() as (store, owner, _pool, actor, farm_id, _dsn):
        receipt = await store.execute(
            actor, "parcel.create", command(), farm_id=farm_id
        )
        parcel_id = receipt["parcel_id"]
        before = await snapshot(owner, actor.claims["tenant_id"])
        for table in TABLES[1:]:
            for sql in (
                f"UPDATE {table} SET tenant_id=tenant_id WHERE parcel_id=$1",
                f"DELETE FROM {table} WHERE parcel_id=$1",
            ):
                with pytest.raises(asyncpg.CheckViolationError):
                    await owner.execute(sql, parcel_id)
        with pytest.raises(asyncpg.CheckViolationError):
            await owner.execute("TRUNCATE agriculture_parcel_outbox")
        for field, value in (
            ("data_class", "operational"),
            ("external_code", "changed"),
            ("name", "changed"),
            ("farm_id", "farm_" + "f" * 32),
        ):
            with pytest.raises(asyncpg.CheckViolationError):
                async with store.transaction(actor) as conn:
                    await conn.execute(
                        f"UPDATE agriculture_parcels SET {field}=$2,"
                        "current_revision=current_revision+1 WHERE parcel_id=$1",
                        parcel_id,
                        value,
                    )
        with pytest.raises(asyncpg.ForeignKeyViolationError):
            async with store.transaction(actor) as conn:
                await conn.execute(
                    "UPDATE agriculture_parcels "
                    "SET current_revision=current_revision+1 WHERE parcel_id=$1",
                    parcel_id,
                )
        assert await snapshot(owner, actor.claims["tenant_id"]) == before
        assert (await store.read(actor, parcel_id))["geometry_revision"] == 1


@pytest.mark.asyncio
async def test_parcel_store_database_revision_chronology_rejects_without_side_effect():
    async with setup() as (store, owner, _pool, actor, farm_id, _dsn):
        receipt = await store.execute(
            actor, "parcel.create", command(), farm_id=farm_id
        )
        parcel_id = receipt["parcel_id"]
        before = await snapshot(owner, actor.claims["tenant_id"])
        insert = """INSERT INTO agriculture_parcel_revisions
          (tenant_id,workspace_id,parcel_id,revision,geom,geometry_hash,provenance,
           effective_at,recorded_at,actor_id,reason,operation_id)
          SELECT tenant_id,workspace_id,parcel_id,$2,geom,geometry_hash,provenance,
                 effective_at,recorded_at,actor_id,reason,$3
          FROM agriculture_parcel_revisions WHERE parcel_id=$1 AND revision=1"""
        for move_pointer in (False, True):
            with pytest.raises(asyncpg.CheckViolationError):
                async with store.transaction(actor) as conn:
                    if move_pointer:
                        await conn.execute(
                            "UPDATE agriculture_parcels SET current_revision=2 "
                            "WHERE parcel_id=$1",
                            parcel_id,
                        )
                    await conn.execute(insert, parcel_id, 2, "pop_" + uuid4().hex)
        assert await snapshot(owner, actor.claims["tenant_id"]) == before
