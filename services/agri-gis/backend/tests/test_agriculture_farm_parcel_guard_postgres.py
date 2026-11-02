"""Real PostGIS parent/child containment and lock-race regression fixtures."""

import asyncio
import hashlib
import json
from uuid import uuid4

import pytest
from test_agriculture_farms_geometry import rectangle
from test_agriculture_farms_store_postgres import command, farm_fields, identity, setup

from app.agriculture.farms_store import digest
from app.agriculture.identity.verify import IdentityError
from app.agriculture.parcels_store import ParcelStore


def parcel_command(geometry, *, expected=0, farm_revision=1):
    fields = {
        "geometry": geometry,
        "source_crs": "EPSG:4326",
        "coordinate_order": "longitude_latitude",
    }
    body = {
        "operation_id": "operation_" + uuid4().hex,
        "expected_revision": expected,
        "reason": "Synthetic containment regression",
    }
    if expected == 0:
        body.update(
            expected_farm_revision=farm_revision,
            parcel={
                "external_code": "parcel_" + uuid4().hex,
                "name": "Synthetic parcel",
                **fields,
            },
        )
    else:
        body.update(fields)
    body["request_hash"] = digest(body)
    return body


async def snapshot(owner, actor):
    tables = (
        "agriculture_farms",
        "agriculture_farm_revisions",
        "agriculture_farm_commands",
        "agriculture_farm_outbox",
        "agriculture_farm_dictionaries",
        "agriculture_parcels",
        "agriculture_parcel_revisions",
        "agriculture_parcel_commands",
        "agriculture_parcel_outbox",
    )
    rows = {}
    for table in tables:
        rows[table] = await owner.fetchval(
            "SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),"
            "'[]'::jsonb)::text FROM "
            + table
            + " t WHERE tenant_id=$1",
            actor.claims["tenant_id"],
        )
    return hashlib.sha256(json.dumps(rows, sort_keys=True).encode()).hexdigest()


async def create_farm(store, actor):
    return (
        await store.execute(
            actor, "farm.create", command(farm=farm_fields("farm_" + uuid4().hex))
        )
    )["farm_id"]


@pytest.mark.asyncio
async def test_farm_shrink_rejects_current_parcel_without_any_domain_write():
    async with setup() as (store, owner, pool, actor, _):
        farm_id = await create_farm(store, actor)
        parcels = ParcelStore(pool)
        parcel = await parcels.execute(
            actor,
            "parcel.create",
            parcel_command(rectangle(110.007, 20.007, 0.002)),
            farm_id=farm_id,
        )
        before = await snapshot(owner, actor)
        with pytest.raises(IdentityError) as error:
            await store.execute(
                actor,
                "farm.update",
                command(
                    "farm.update", farm={"geometry": rectangle(width=0.005)}, revision=1
                ),
                farm_id=farm_id,
            )
        assert (error.value.code, error.value.status) == ("STATE_CONFLICT", 409)
        assert await snapshot(owner, actor) == before
        assert (await parcels.read(actor, parcel["parcel_id"]))[
            "geometry_revision"
        ] == 1


@pytest.mark.asyncio
async def test_farm_boundary_uses_only_current_revision_and_preserves_history():
    async with setup() as (store, owner, pool, actor, _):
        farm_id = await create_farm(store, actor)
        parcels = ParcelStore(pool)
        parcel = await parcels.execute(
            actor,
            "parcel.create",
            parcel_command(rectangle(110.007, 20.007, 0.002)),
            farm_id=farm_id,
        )
        original = await parcels.read(actor, parcel["parcel_id"], selected_revision=1)
        await parcels.execute(
            actor,
            "parcel.revise",
            parcel_command(rectangle(110.001, 20.001, 0.002), expected=1),
            parcel_id=parcel["parcel_id"],
        )
        updated = await store.execute(
            actor,
            "farm.update",
            command(
                "farm.update", farm={"geometry": rectangle(width=0.005)}, revision=1
            ),
            farm_id=farm_id,
        )
        assert updated["aggregate_revision"] == 2
        current = await parcels.read(actor, parcel["parcel_id"])
        assert await parcels.read(actor, parcel["parcel_id"], selected_revision=1) == {
            **original,
            "current_geometry_revision": 2,
            "valid_to": current["valid_from"],
        }
        assert current["geometry_revision"] == 2
        assert (
            await owner.fetchval(
                "SELECT count(*) FROM agriculture_parcel_revisions WHERE tenant_id=$1",
                actor.claims["tenant_id"],
            )
            == 2
        )


@pytest.mark.asyncio
async def test_farm_update_ignores_other_farms_and_workspaces():
    async with setup() as (store, _owner, pool, actor, _):
        own_farm = await create_farm(store, actor)
        parcels = ParcelStore(pool)
        for other in (actor, identity(tenant=actor.claims["tenant_id"]), identity()):
            farm_id = await create_farm(store, other)
            await parcels.execute(
                other,
                "parcel.create",
                parcel_command(rectangle(110.007, 20.007, 0.002)),
                farm_id=farm_id,
            )
        result = await store.execute(
            actor,
            "farm.update",
            command(
                "farm.update", farm={"geometry": rectangle(width=0.005)}, revision=1
            ),
            farm_id=own_farm,
        )
        assert result["aggregate_revision"] == 2


@pytest.mark.asyncio
async def test_parcel_pending_create_cannot_escape_new_parent_boundary():
    async with setup() as (store, owner, pool, actor, _):
        farm_id = await create_farm(store, actor)
        parcels = ParcelStore(pool)
        entered, release = asyncio.Event(), asyncio.Event()

        async def pause():
            entered.set()
            await release.wait()

        task = asyncio.create_task(
            parcels.execute(
                actor,
                "parcel.create",
                parcel_command(rectangle(110.007, 20.007, 0.002)),
                farm_id=farm_id,
                before_write=pause,
            )
        )
        try:
            await asyncio.wait_for(entered.wait(), 2)
            await store.execute(
                actor,
                "farm.update",
                command(
                    "farm.update", farm={"geometry": rectangle(width=0.005)}, revision=1
                ),
                farm_id=farm_id,
            )
            before = await snapshot(owner, actor)
            release.set()
            with pytest.raises(IdentityError) as error:
                await asyncio.wait_for(task, 3)
            assert error.value.code in {"REVISION_CONFLICT", "PARCEL_OUTSIDE_FARM"}
            assert await snapshot(owner, actor) == before
        finally:
            release.set()
            if not task.done():
                task.cancel()
                await asyncio.gather(task, return_exceptions=True)


@pytest.mark.asyncio
async def test_farm_pending_update_rechecks_parcel_created_after_preparation():
    async with setup() as (store, owner, pool, actor, _):
        farm_id = await create_farm(store, actor)
        entered, release = asyncio.Event(), asyncio.Event()

        async def pause():
            entered.set()
            await release.wait()

        task = asyncio.create_task(
            store.execute(
                actor,
                "farm.update",
                command(
                    "farm.update", farm={"geometry": rectangle(width=0.005)}, revision=1
                ),
                farm_id=farm_id,
                before_write=pause,
            )
        )
        try:
            await asyncio.wait_for(entered.wait(), 2)
            await ParcelStore(pool).execute(
                actor,
                "parcel.create",
                parcel_command(rectangle(110.007, 20.007, 0.002)),
                farm_id=farm_id,
            )
            before = await snapshot(owner, actor)
            release.set()
            with pytest.raises(IdentityError) as error:
                await asyncio.wait_for(task, 3)
            assert (error.value.code, error.value.status) == ("STATE_CONFLICT", 409)
            assert await snapshot(owner, actor) == before
        finally:
            release.set()
            if not task.done():
                task.cancel()
                await asyncio.gather(task, return_exceptions=True)
