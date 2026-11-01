"""Real PostgreSQL catalog fixtures, not public HTTP or live-source acceptance."""

import asyncio
import copy
import hashlib
import json
from contextlib import asynccontextmanager
from datetime import UTC, datetime, timedelta
from pathlib import Path
from uuid import uuid4

import asyncpg
import pytest
from domain_fixture_support import install_domain_schema
from test_agriculture_actors_postgres import pg_config
from test_agriculture_asset_catalog_rules import metadata, registry, reviewed

from app.agriculture.asset_catalog_objects import CatalogObjectStore
from app.agriculture.asset_catalog_rules import digest
from app.agriculture.asset_catalog_store import AssetCatalogOwnership, AssetCatalogStore
from app.agriculture.database import create_runtime_pool
from app.agriculture.identity.verify import IdentityError, Verified
from app.agriculture.object_storage import PrivateLocalStorage, object_key
from app.agriculture.object_store import PostgresObjectStore


def command(
    action="asset.create", *, asset=None, revision=0, value=None, operation=None
):
    body = {
        "operation_id": operation or "aop_" + uuid4().hex,
        "grant_plan_hash": "1" * 64,
        "asset_id": asset,
        "expected_revision": revision,
        "metadata": value or metadata(),
    }
    body["request_hash"] = digest({"action": action, **body})
    claims = {
        "tenant_id": "tenant_a",
        "workspace_id": "workspace_a",
        "actor": {"kind": "user", "id": "actor_a"},
        "action": action,
        "asset_ids": [] if action == "asset.create" else [asset],
        **{k: body[k] for k in ("operation_id", "request_hash", "grant_plan_hash")},
    }
    return Verified(claims, datetime.now(UTC) + timedelta(minutes=5), body), body


async def trusted_fixture_entity(owner, store, asset_id, value, revision):
    """Owner-only fixture publication; no public ready or storage-key endpoint."""
    object_id, version_id = "object_" + uuid4().hex, "av_" + uuid4().hex
    content = b"Synthetic catalog entity fixture; not a licensed production image."
    key = object_key("tenant_a", "workspace_a", object_id, 1)
    snapshot = store.rights.snapshot("tenant_a", "workspace_a", value)
    async with owner.transaction():
        await owner.execute(
            "INSERT INTO agriculture_private_objects "
            "(tenant_id,workspace_id,object_id,version,resource_kind,resource_id,"
            "resource_revision,category,object_key,content_sha256,size_bytes,media_type,"
            "data_class,distribution_allowed) "
            "VALUES('tenant_a','workspace_a',$1,1,'asset',$2,$3,'original',$4,$5,$6,"
            "'application/octet-stream','demo',true)",
            object_id,
            asset_id,
            revision,
            key,
            hashlib.sha256(content).hexdigest(),
            len(content),
        )
        await owner.execute(
            "UPDATE agriculture_asset_catalog SET current_revision=$2,"
            "current_version_id=$3,"
            "status='ready',updated_at=clock_timestamp() WHERE asset_id=$1",
            asset_id,
            revision,
            version_id,
        )
        await owner.execute(
            "INSERT INTO agriculture_asset_catalog_versions "
            "(tenant_id,workspace_id,asset_id,version,asset_version_id,metadata_json,"
            "metadata_hash,rights_snapshot,object_id,object_version,created_by) "
            "VALUES('tenant_a','workspace_a',$1,$2,$3,$4::jsonb,$5,$6::jsonb,$7,1,'fixture_owner')",
            asset_id,
            revision,
            version_id,
            json.dumps(value),
            digest(value),
            json.dumps(snapshot),
            object_id,
        )
    return object_id, key, content


@pytest.mark.asyncio
async def test_asset_catalog_real_migration_replay_and_precise_grants():
    async with setup() as (store, owner, _pool, _source):
        sql = (
            Path(__file__).parents[1]
            / "scripts/migrations/20261101_agriculture_asset_catalog.sql"
        ).read_text()
        await owner.execute(sql)
        await owner.execute(sql)
        await store.require_ready()
        allowed = await owner.fetch(
            "SELECT column_name FROM information_schema.column_privileges "
            "WHERE table_name='agriculture_asset_catalog' AND grantee='flow_geo_app' "
            "AND privilege_type='UPDATE' ORDER BY column_name"
        )
        assert [r["column_name"] for r in allowed] == [
            "current_revision",
            "current_version_id",
            "status",
            "updated_at",
        ]


@pytest.mark.asyncio
async def test_asset_catalog_real_ready_bytes_history_and_current_withdrawal(tmp_path):
    async with setup() as (store, owner, _pool, source):
        verified, body = command()
        receipt = await store.execute(verified, "asset.create", body)
        asset_id = receipt["asset_id"]
        obj, key, content = await trusted_fixture_entity(
            owner, store, asset_id, metadata(), 2
        )
        location = tmp_path / key
        location.parent.mkdir(parents=True)
        location.write_bytes(content)
        storage = PrivateLocalStorage(tmp_path.resolve())
        await store.authorize_use(
            "tenant_a", "workspace_a", asset_id, 2, purpose="analysis", profile="demo"
        )
        assert (
            storage.read(
                tenant="tenant_a",
                workspace="workspace_a",
                object_id=obj,
                version=1,
                key=key,
                size=len(content),
                sha256=hashlib.sha256(content).hexdigest(),
            )
            == content
        )
        tightened = metadata()
        tightened["rights_declaration"]["permitted_uses"] = ["ingest"]
        await trusted_fixture_entity(owner, store, asset_id, tightened, 3)
        with pytest.raises(IdentityError):
            await store.authorize_use(
                "tenant_a",
                "workspace_a",
                asset_id,
                2,
                purpose="analysis",
                profile="demo",
            )
        await store.authorize_use(
            "tenant_a", "workspace_a", asset_id, 2, purpose="ingest", profile="demo"
        )
        source["sources"][0]["rights"]["status"] = "revoked"
        with pytest.raises(IdentityError):
            await store.authorize_use(
                "tenant_a", "workspace_a", asset_id, 2, purpose="ingest", profile="demo"
            )
        with pytest.raises(IdentityError) as error:
            await store.authorize_use(
                "tenant_a",
                "workspace_a",
                asset_id,
                2,
                purpose="ingest",
                profile="demo",
                require_entity=False,
            )
        assert error.value.code == "ENTITY_CHECK_REQUIRED"


@pytest.mark.asyncio
async def test_asset_catalog_real_object_guard_rechecks_after_storage_read(tmp_path):
    async with setup() as (catalog, owner, pool, source):
        value = metadata()
        value["rights_declaration"]["permitted_uses"].append("export")
        source["sources"][0]["rights"]["permitted_uses"].append("export")
        source["sources"][0]["rights"]["redistribution"] = "allowed"
        verified, body = command(value=value)
        receipt = await catalog.execute(verified, "asset.create", body)
        obj, key, content = await trusted_fixture_entity(
            owner, catalog, receipt["asset_id"], value, 2
        )
        path = tmp_path / key
        path.parent.mkdir(parents=True)
        path.write_bytes(content)
        storage = PrivateLocalStorage(tmp_path.resolve())
        guarded = CatalogObjectStore(PostgresObjectStore(pool), catalog)
        await guarded.require_ready()
        item = await guarded.get("tenant_a", "workspace_a", obj, 1)

        def read_then_withdraw():
            result = storage.read(
                tenant=item.tenant_id,
                workspace=item.workspace_id,
                object_id=item.object_id,
                version=item.version,
                key=item.object_key,
                size=item.size_bytes,
                sha256=item.content_sha256,
            )
            source["sources"][0]["rights"]["status"] = "revoked"
            return result

        assert await asyncio.to_thread(read_then_withdraw) == content
        # This is the same get used by gateway.reference after byte I/O; bytes
        # must not be emitted until it succeeds. No mocked successful store.
        with pytest.raises(IdentityError) as error:
            await guarded.get("tenant_a", "workspace_a", obj, 1)
        assert error.value.code == "RIGHTS_NOT_PERMITTED"
        with pytest.raises(IdentityError):
            await guarded.get("tenant_b", "workspace_a", obj, 1)


async def catalog_snapshot(owner):
    result = {}
    for name in (
        "agriculture_asset_catalog",
        "agriculture_asset_catalog_versions",
        "agriculture_asset_catalog_commands",
        "agriculture_asset_catalog_outbox",
    ):
        result[name] = await owner.fetchval(
            "SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),"
            f"'[]'::jsonb)::text FROM {name} t"
        )
    return result


@pytest.mark.asyncio
@pytest.mark.parametrize("lock_kind", ["advisory", "row", "commit"])
async def test_asset_catalog_real_expired_ticket_rolls_back_full_snapshot(lock_kind):
    async with setup() as (store, owner, _pool, _source):
        initial, initial_body = command()
        receipt = await store.execute(initial, "asset.create", initial_body)
        verified, body = command("asset.version", asset=receipt["asset_id"], revision=1)
        verified = Verified(
            verified.claims,
            datetime.now(UTC) + timedelta(milliseconds=450),
            verified.body,
        )
        before = await catalog_snapshot(owner)
        blocker = await asyncpg.connect(pg_config()[0])
        transaction = blocker.transaction()
        await transaction.start()
        task = None
        try:
            if lock_kind == "advisory":
                await blocker.execute(
                    "SELECT pg_advisory_xact_lock(hashtextextended($1,0))",
                    "tenant_a:workspace_a:" + body["operation_id"],
                )
            elif lock_kind == "row":
                await blocker.fetchrow(
                    "SELECT asset_id FROM agriculture_asset_catalog "
                    "WHERE asset_id=$1 FOR UPDATE",
                    receipt["asset_id"],
                )
            else:
                await transaction.rollback()
                transaction = None
                await owner.execute("""
CREATE OR REPLACE FUNCTION public.asset_test_fixture_commit_delay()
 RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN PERFORM pg_sleep(0.7); RETURN NEW; END $$;
CREATE TRIGGER asset_test_fixture_commit_delay
 BEFORE INSERT ON public.agriculture_asset_catalog_outbox
 FOR EACH ROW EXECUTE FUNCTION public.asset_test_fixture_commit_delay();
""")
            task = asyncio.create_task(store.execute(verified, "asset.version", body))
            if lock_kind != "commit":
                for _ in range(150):
                    waiting = await owner.fetchval(
                        "SELECT count(*) FROM pg_locks WHERE NOT granted"
                    )
                    if waiting:
                        break
                    assert not task.done()
                    await asyncio.sleep(0.005)
                assert waiting > 0
                await asyncio.sleep(
                    max(0, (verified.expires_at - datetime.now(UTC)).total_seconds())
                    + 0.08
                )
                await transaction.rollback()
                transaction = None
            with pytest.raises(IdentityError) as error:
                await task
            assert (
                error.value.code == "delegation_expired" and error.value.status == 401
            )
            assert await catalog_snapshot(owner) == before
        finally:
            if transaction is not None:
                await transaction.rollback()
            if task is not None and not task.done():
                await asyncio.gather(task, return_exceptions=True)
            if lock_kind == "commit":
                await owner.execute(
                    "DROP TRIGGER IF EXISTS asset_test_fixture_commit_delay "
                    "ON agriculture_asset_catalog_outbox; "
                    "DROP FUNCTION IF EXISTS public.asset_test_fixture_commit_delay()"
                )
            await blocker.close()


@asynccontextmanager
async def setup():
    owner_dsn, runtime_dsn = pg_config()
    owner = await asyncpg.connect(owner_dsn)
    try:
        present = await owner.fetchval(
            "SELECT to_regclass('public.agriculture_asset_catalog') IS NOT NULL"
        )
        if not present:
            await install_domain_schema(owner_dsn)
            # Explicit component provisioning until the integrator registers
            # this immutable migration. Never a production auto-create path.
            if not await owner.fetchval(
                "SELECT to_regclass('public.agriculture_asset_catalog') IS NOT NULL"
            ):
                await owner.execute(
                    (
                        Path(__file__).parents[1]
                        / "scripts/migrations/20261101_agriculture_asset_catalog.sql"
                    ).read_text()
                )
        pool = await create_runtime_pool(runtime_dsn, min_size=1, max_size=3)
        try:
            source = registry()
            store = AssetCatalogStore(pool, reviewed(source))
            await store.require_ready()
            yield store, owner, pool, source
        finally:
            await pool.close()
    finally:
        await owner.close()


@pytest.mark.asyncio
async def test_asset_catalog_real_register_receipt_and_no_ready_claim():
    async with setup() as (store, owner, _pool, _source):
        verified, body = command()
        receipt = await store.execute(verified, "asset.create", body)
        value = await store.get("tenant_a", "workspace_a", receipt["asset_id"])
        assert value["version"] == value["revision"] == 1
        assert value["entity"] is None and value["status"] == "validating"
        assert value["rights"]["status"] == "approved"
        assert "private_reviewed_sources" not in value["rights"]
        stored_rights = json.loads(
            await owner.fetchval(
                "SELECT rights_snapshot::text FROM agriculture_asset_catalog_versions "
                "WHERE asset_id=$1 AND version=1",
                receipt["asset_id"],
            )
        )
        assert stored_rights["private_reviewed_sources"][0]["rights"]["evidence"]
        assert stored_rights["snapshot_hash"] == value["rights"]["snapshot_hash"]
        preview = await store.preview(
            "tenant_a", "workspace_a", "asset.create", metadata()
        )
        assert "private_reviewed_sources" not in preview["rights"]
        assert value["metadata_hash"] == digest(value["metadata"])
        assert await store.execute(verified, "asset.create", body) == receipt
        assert (
            await owner.fetchval(
                "SELECT count(*) FROM agriculture_asset_catalog_outbox "
                "WHERE operation_id=$1",
                body["operation_id"],
            )
            == 1
        )
        with pytest.raises(IdentityError) as error:
            await store.authorize_use(
                "tenant_a",
                "workspace_a",
                receipt["asset_id"],
                1,
                purpose="analysis",
                profile="demo",
            )
        assert error.value.code == "ENTITY_NOT_READY"


@pytest.mark.asyncio
async def test_asset_catalog_real_versions_concurrent_cas_and_immutable_history():
    async with setup() as (store, owner, _pool, _source):
        verified, body = command()
        first = await store.execute(verified, "asset.create", body)
        value = metadata()
        value["name"] = "Identical display name allowed"
        pairs = [
            command("asset.version", asset=first["asset_id"], revision=1, value=value)
            for _ in range(2)
        ]
        results = await asyncio.gather(
            *(store.execute(v, "asset.version", b) for v, b in pairs),
            return_exceptions=True,
        )
        assert sum(isinstance(r, dict) for r in results) == 1
        assert (
            sum(
                isinstance(r, IdentityError) and r.code == "REVISION_CONFLICT"
                for r in results
            )
            == 1
        )
        history = await store.get("tenant_a", "workspace_a", first["asset_id"], 1)
        latest = await store.get("tenant_a", "workspace_a", first["asset_id"])
        assert history["metadata"]["name"] == metadata()["name"]
        assert (
            latest["version"] == 2
            and latest["asset_version_id"] != history["asset_version_id"]
        )
        with pytest.raises(asyncpg.CheckViolationError):
            await owner.execute(
                "UPDATE agriculture_asset_catalog_versions SET metadata_hash=$1 "
                "WHERE asset_id=$2",
                "0" * 64,
                first["asset_id"],
            )


@pytest.mark.asyncio
async def test_asset_catalog_real_scope_pool_reuse_and_null_farm_projection():
    async with setup() as (store, _owner, pool, _source):
        verified, body = command()
        receipt = await store.execute(verified, "asset.create", body)
        identifier = receipt["asset_id"]
        facts = await AssetCatalogOwnership(pool).resolve_asset(
            "tenant_a", "workspace_a", [identifier]
        )
        assert len(facts) == 1 and facts[0].farm_ids == () and facts[0].revision == 1
        for tenant, workspace in [
            ("tenant_b", "workspace_a"),
            ("tenant_a", "workspace_b"),
        ]:
            assert (
                await AssetCatalogOwnership(pool).resolve_asset(
                    tenant, workspace, [identifier]
                )
                == []
            )
            with pytest.raises(IdentityError):
                await store.get(tenant, workspace, identifier)
            assert (await store.list(tenant, workspace, [identifier]))["items"] == []
        async with pool.acquire() as conn:
            assert (
                await conn.fetchval("SELECT count(*) FROM agriculture_asset_catalog")
                == 0
            )
        assert (await store.get("tenant_a", "workspace_a", identifier))[
            "asset_id"
        ] == identifier
        assert (
            len(
                (await store.list("tenant_a", "workspace_a", [identifier], limit=200))[
                    "items"
                ]
            )
            == 1
        )
        for limit in (0, 201, True):
            with pytest.raises(IdentityError):
                await store.list("tenant_a", "workspace_a", [identifier], limit=limit)


@pytest.mark.asyncio
async def test_asset_catalog_real_same_names_and_old_command_hash_conflict():
    async with setup() as (store, _owner, _pool, _source):
        first, first_body = command()
        second, second_body = command()
        a = await store.execute(first, "asset.create", first_body)
        b = await store.execute(second, "asset.create", second_body)
        assert a["asset_id"] != b["asset_id"]
        assert (
            len(
                (
                    await store.list(
                        "tenant_a", "workspace_a", [a["asset_id"], b["asset_id"]]
                    )
                )["items"]
            )
            == 2
        )
        different, changed_body = command(operation=first_body["operation_id"])
        changed_body["request_hash"] = "e" * 64
        different.claims["request_hash"] = "e" * 64
        with pytest.raises(IdentityError) as error:
            await store.execute(different, "asset.create", changed_body)
        assert error.value.code == "OPERATION_CONFLICT"


@pytest.mark.asyncio
async def test_asset_catalog_real_current_rights_revocation_expiry_and_demo_block():
    async with setup() as (store, _owner, _pool, source):
        verified, body = command()
        receipt = await store.execute(verified, "asset.create", body)
        identifier = receipt["asset_id"]
        for mutate in [
            lambda r: r.update(status="revoked"),
            lambda r: r.update(expiry="2021-01-01T00:00:00Z"),
            lambda r: r.update(permitted_uses=[]),
        ]:
            source["sources"][0]["rights"] = registry()["sources"][0]["rights"]
            mutate(source["sources"][0]["rights"])
            with pytest.raises(IdentityError) as error:
                await store.authorize_use(
                    "tenant_a",
                    "workspace_a",
                    identifier,
                    1,
                    purpose="analysis",
                    profile="demo",
                )
            assert error.value.code == "RIGHTS_NOT_PERMITTED"
        with pytest.raises(IdentityError) as error:
            await store.authorize_use(
                "tenant_a",
                "workspace_a",
                identifier,
                1,
                purpose="analysis",
                profile="operational",
            )
        assert error.value.code == "DATA_CLASS_MISMATCH"


@pytest.mark.asyncio
async def test_asset_catalog_real_successor_cannot_release_security_constraints():
    async with setup() as (store, owner, _pool, _source):
        verified, body = command()
        receipt = await store.execute(verified, "asset.create", body)
        before = await owner.fetchval(
            "SELECT row_to_json(t)::text FROM agriculture_asset_catalog t "
            "WHERE asset_id=$1",
            receipt["asset_id"],
        )
        changed = copy.deepcopy(metadata())
        changed["retention"]["legal_hold"] = False
        proof, mutation = command(
            "asset.version", asset=receipt["asset_id"], revision=1, value=changed
        )
        with pytest.raises(IdentityError):
            await store.execute(proof, "asset.version", mutation)
        assert (
            await owner.fetchval(
                "SELECT row_to_json(t)::text FROM agriculture_asset_catalog t "
                "WHERE asset_id=$1",
                receipt["asset_id"],
            )
            == before
        )
