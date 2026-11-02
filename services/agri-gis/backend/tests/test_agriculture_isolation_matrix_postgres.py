"""Exact production route matrix with real crypto and runtime PostgreSQL.

Ownership rows are explicit test facts, not production farm registration. No
mock authorize callback is installed. Successful content/current-authority
checks belong to the separate real Go/PostgreSQL callback chain. Job adapter
installation here exposes only the real PG sidecar's rejection paths; callback
and execution remain unavailable. This is not W06 scheduler/live acceptance.
"""

import copy
import hashlib
import json
from dataclasses import replace
from datetime import UTC, datetime, timedelta
from uuid import uuid4

import httpx
import pytest
import pytest_asyncio
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
from job_fixture_support import prepare_jobs
from test_agriculture_actors_postgres import pg_config
from test_agriculture_asset_catalog_postgres import command as asset_command
from test_agriculture_asset_catalog_rules import metadata as asset_metadata
from test_agriculture_asset_catalog_rules import registry, reviewed
from test_agriculture_farms_config import dictionary_document
from test_agriculture_farms_store_postgres import (
    create_canary,
    farm_input,
    fixtureproof,
)
from test_agriculture_identity import sign
from test_agriculture_job_authority import job_fixture
from test_agriculture_objects_postgres import insert, setup
from test_agriculture_parcels_store_postgres import command as parcel_command

from app.agriculture.asset_catalog_identity import ASSET_TYP
from app.agriculture.asset_catalog_store import AssetCatalogOwnership, AssetCatalogStore
from app.agriculture.asset_object_identity import ASSET_OBJECT_TYP
from app.agriculture.domain_boundary import LEGACY_TABLES
from app.agriculture.farms_config import FarmDictionary
from app.agriculture.farms_identity import WORKSPACE_TYP
from app.agriculture.farms_store import FarmStore
from app.agriculture.identity.canonical import canonicalize
from app.agriculture.identity.replay import PostgresReplayLedger
from app.agriculture.identity.verify import BUSINESS_TYP, SERVICE_TYP
from app.agriculture.job_authority import JobAuthority, snapshot_bytes
from app.agriculture.job_store import PostgresJobAuthorityStore
from app.agriculture.main import create_app
from app.agriculture.object_storage import object_key
from app.agriculture.parcels_identity import PARCEL_OPERATION_TYP
from app.agriculture.parcels_store import ParcelStore

PREFIX = "/internal/agri/v1"
LOCATOR = PREFIX + "/tenants/{tenant}/workspaces/{workspace}"
ROUTES = {
    "ownership": ("POST", PREFIX + "/ownership/resolve"),
    "metadata": ("POST", PREFIX + "/objects/resolve"),
    "asset_get": ("GET", LOCATOR + "/assets/objects/{object}/versions/{version}"),
    "asset_head": ("HEAD", LOCATOR + "/assets/objects/{object}/versions/{version}"),
    "report_get": ("GET", LOCATOR + "/reports/objects/{object}/versions/{version}"),
    "report_head": ("HEAD", LOCATOR + "/reports/objects/{object}/versions/{version}"),
    "job_accept": ("POST", PREFIX + "/jobs/authority/accept"),
    "job_checkpoint": ("POST", PREFIX + "/jobs/authority/checkpoint"),
}
FARM_ROUTES = {
    "farm_preview_create": (
        "POST",
        PREFIX + "/farms/preview-create",
        "farm.preview.create",
    ),
    "farm_create": ("POST", PREFIX + "/farms/commands/create", "farm.create"),
    "farm_receipt": ("POST", PREFIX + "/farms/commands/receipt", "farm.receipt"),
    "farm_context": ("POST", PREFIX + "/farms/context", "farm.context"),
    "farm_list": ("POST", PREFIX + "/farms/list", "farm.list"),
    "farm_history": ("POST", PREFIX + "/farms/history", "farm.history.read"),
    "farm_read": ("GET", PREFIX + "/farms/{farm_id}", "agri.farm.read"),
    "farm_preview_update": (
        "POST",
        PREFIX + "/farms/{farm_id}/preview-update",
        "agri.farm.update",
    ),
    "farm_preview_archive": (
        "POST",
        PREFIX + "/farms/{farm_id}/preview-archive",
        "agri.farm.archive",
    ),
    "farm_update": ("PATCH", PREFIX + "/farms/{farm_id}", "agri.farm.update"),
    "farm_archive": ("POST", PREFIX + "/farms/{farm_id}/archive", "agri.farm.archive"),
}
FARM_SIGNATURE_CASES = (
    "unsigned",
    "wrong_signature",
    "audience",
    "path",
    "body",
    "expired",
)
PARCEL_ROUTES = {
    "parcel_preview_create": (
        "POST",
        PREFIX + "/farms/{farm_id}/parcels/preview-create",
        "agri.parcel.create",
    ),
    "parcel_create": (
        "POST",
        PREFIX + "/farms/{farm_id}/parcels",
        "agri.parcel.create",
    ),
    "parcel_list": (
        "POST",
        PREFIX + "/farms/{farm_id}/parcels/list",
        "agri.parcel.read",
    ),
    "parcel_preview_revise": (
        "POST",
        PREFIX + "/parcels/{parcel_id}/preview-revise",
        "agri.parcel.update",
    ),
    "parcel_revise": (
        "POST",
        PREFIX + "/parcels/{parcel_id}/revisions",
        "agri.parcel.update",
    ),
    "parcel_read": ("POST", PREFIX + "/parcels/{parcel_id}/read", "agri.parcel.read"),
    "parcel_history": (
        "POST",
        PREFIX + "/parcels/{parcel_id}/history",
        "agri.parcel.read",
    ),
    "parcel_receipt": ("POST", PREFIX + "/parcels/commands/receipt", "parcel.receipt"),
}
ASSET_CATALOG_ROUTES = {
    "catalog_context": ("POST", PREFIX + "/assets/context", "asset.context"),
    "catalog_ownership": (
        "POST",
        PREFIX + "/assets/ownership/resolve",
        "asset.ownership.read",
    ),
    "catalog_preview_create": (
        "POST",
        PREFIX + "/assets/preview-create",
        "asset.preview.create",
    ),
    "catalog_preview_version": (
        "POST",
        PREFIX + "/assets/preview-version",
        "asset.preview.version",
    ),
    "catalog_create": ("POST", PREFIX + "/assets/commands/create", "asset.create"),
    "catalog_version": ("POST", PREFIX + "/assets/commands/version", "asset.version"),
    "catalog_receipt": ("POST", PREFIX + "/assets/commands/receipt", "asset.receipt"),
    "catalog_list": ("POST", PREFIX + "/assets/list", "asset.list"),
    "catalog_read": ("POST", PREFIX + "/assets/read", "asset.read"),
}
ASSET_OBJECT_ROUTES = {
    "catalog_object_get": (
        "GET",
        LOCATOR + "/assets/catalog-objects/{object}/versions/{version}",
    ),
    "catalog_object_head": (
        "HEAD",
        LOCATOR + "/assets/catalog-objects/{object}/versions/{version}",
    ),
}
CASES = (
    "unsigned",
    "wrong_signature",
    "audience",
    "path",
    "body",
    "expired",
    "tenant",
    "workspace",
)
SNAPSHOT_TABLES = sorted(
    LEGACY_TABLES
    | {
        "agriculture_project_bindings",
        "agriculture_actor_bindings",
        "agriculture_actor_audit",
        "agriculture_legacy_plot_ownership",
        "agriculture_legacy_resource_references",
        "agriculture_private_objects",
        "agriculture_job_authorities",
        "agriculture_job_authority_checks",
        "fixture_job_leases",
        "fixture_job_resources",
        "agriculture_farms",
        "agriculture_farm_revisions",
        "agriculture_farm_commands",
        "agriculture_farm_outbox",
        "agriculture_farm_dictionaries",
        "agriculture_parcels",
        "agriculture_parcel_revisions",
        "agriculture_parcel_commands",
        "agriculture_parcel_outbox",
        "agriculture_asset_catalog",
        "agriculture_asset_catalog_versions",
        "agriculture_asset_catalog_commands",
        "agriculture_asset_catalog_outbox",
    }
)


def discovered(app):
    return {(method, route.path) for route in app.routes for method in route.methods}


def test_isolation_exact_production_route_manifest():
    assert discovered(create_app()) == set(ROUTES.values()) | {
        row[:2] for row in FARM_ROUTES.values()
    } | {row[:2] for row in PARCEL_ROUTES.values()} | {
        row[:2] for row in ASSET_CATALOG_ROUTES.values()
    } | set(ASSET_OBJECT_ROUTES.values()) | {("GET", "/health")}
    assert len(discovered(create_app())) == 39


async def domain_snapshot(owner):
    """Assertion-only owner, committed full rows; never runtime authorization.

    Nonce/security ledger is intentionally excluded. All domain/sidecar tables
    are included, along with explicit fixture ownership facts. No raw snapshot
    or credentials are emitted into test output/artifacts.
    """
    assert await owner.fetchval("SELECT current_user") == "flow_geo_migrate"
    result = {}
    async with owner.transaction():
        # Actor audit/bindings FORCE RLS also applies to the migration owner.
        # Explicitly include every seeded/attacked tenant, not an empty scope
        # (which would silently turn the audit comparison into an empty set).
        await owner.execute(
            "SELECT set_config('app.tenant_ids',"
            "'tenant_a,tenant_b,tenant_foreign',true)"
        )
        for table in SNAPSHOT_TABLES:
            # Names come solely from checked-in model inventory/literal list.
            rows = await owner.fetch(
                f'SELECT to_jsonb(t)::text AS row FROM public."{table}" t '
                "ORDER BY to_jsonb(t)::text"
            )
            result[table] = tuple(row["row"] for row in rows)
        result["fixture_ownership"] = tuple(
            await owner.fetch(
                "SELECT to_jsonb(t)::text FROM object_test_fixture.authority t "
                "ORDER BY to_jsonb(t)::text"
            )
        )
    return result


@pytest_asyncio.fixture(scope="module", loop_scope="module")
async def isolation_pg(tmp_path_factory):
    owner_dsn, _ = pg_config()
    jobs_owner = await prepare_jobs(owner_dsn)
    await jobs_owner.close()
    async with setup(tmp_path_factory.mktemp("isolation-pg")) as (fixture, owner, pool):
        _, services, private, claims, item, payload = fixture
        # Discard fixture probe routes and its in-memory authority/replay.
        services.authority = None
        services.replay = PostgresReplayLedger(pool)
        await services.replay.require_safe_runtime()
        app = create_app(services)
        assert discovered(app) == set(ROUTES.values()) | {
            row[:2] for row in FARM_ROUTES.values()
        } | {row[:2] for row in PARCEL_ROUTES.values()} | {
            row[:2] for row in ASSET_CATALOG_ROUTES.values()
        } | set(ASSET_OBJECT_ROUTES.values()) | {("GET", "/health")}
        farm_store = FarmStore(
            pool, FarmDictionary.from_document(dictionary_document())
        )
        # This mixed fixture deliberately has public job-lease test tables.
        # Use the store only to seed real nonempty domain canaries; production
        # readiness is verified separately in the fresh farm migration suite.
        # Never whitelist test tables in the actual domain boundary.
        farm_canaries = []
        for tenant, workspace in (
            ("tenant_a", "workspace_a"),
            ("tenant_a", "workspace_b"),
            ("tenant_b", "workspace_a"),
            ("tenant_foreign", "workspace_foreign"),
        ):
            farm_canaries.append(await create_canary(farm_store, tenant, workspace))
        services.farm_store = farm_store
        services.farm_authority = (
            None  # Real current authorization is exercised by the Go/GIS chain.
        )
        parcel_store = ParcelStore(pool)
        parcel_canaries = []
        for farm in farm_canaries:
            proof = fixtureproof(farm["tenant_id"], farm["workspace_id"])
            parcel_canaries.append(
                await parcel_store.execute(
                    proof, "parcel.create", parcel_command(), farm_id=farm["farm_id"]
                )
            )
        services.parcel_store = parcel_store
        services.parcel_authority = None
        rights = reviewed(registry())
        # Synthetic source admissions are explicit for every canary scope;
        # the actual W00 rights evaluator still decides each registration.
        source_scopes = {
            "fixture-source": [
                {
                    "tenant_id": farm["tenant_id"],
                    "workspace_id": farm["workspace_id"],
                    "source_kind": asset_metadata()["source"]["kind"],
                }
                for farm in farm_canaries
            ]
        }
        rights.scope_loader = lambda: source_scopes
        catalog_store = AssetCatalogStore(pool, rights)
        asset_canaries = []
        for farm in farm_canaries:
            proof, body = asset_command()
            proof = replace(
                proof,
                claims={
                    **proof.claims,
                    "tenant_id": farm["tenant_id"],
                    "workspace_id": farm["workspace_id"],
                },
            )
            asset_canaries.append(
                await catalog_store.execute(proof, "asset.create", body)
            )
        services.asset_catalog_store = catalog_store
        services.asset_catalog_authority = None
        services.asset_catalog_ownership = AssetCatalogOwnership(pool)
        for table in (
            "agriculture_farms",
            "agriculture_farm_revisions",
            "agriculture_farm_commands",
            "agriculture_farm_outbox",
            "agriculture_farm_dictionaries",
            "agriculture_parcels",
            "agriculture_parcel_revisions",
            "agriculture_parcel_commands",
            "agriculture_parcel_outbox",
            "agriculture_asset_catalog",
            "agriculture_asset_catalog_versions",
            "agriculture_asset_catalog_commands",
            "agriculture_asset_catalog_outbox",
        ):
            assert await owner.fetchval(f'SELECT count(*) FROM "{table}"') > 0
        report = replace(
            item,
            object_id="report_" + uuid4().hex,
            resource_kind="report",
            resource_id="report_fixture",
        )
        report = replace(
            report,
            object_key=object_key(
                report.tenant_id, report.workspace_id, report.object_id, 1
            ),
        )
        await insert(owner, report)
        await owner.execute(
            "INSERT INTO object_test_fixture.authority VALUES "
            "('tenant_a','workspace_a','report','report_fixture',"
            "'[\"farm_a\"]',1,'active') "
            "ON CONFLICT(tenant,workspace,kind,id) "
            "DO UPDATE SET revision=1,state='active'"
        )
        snapshot, verified = job_fixture("tenant_a", "workspace_a")
        snapshot["job_id"] = "job_" + uuid4().hex
        store = PostgresJobAuthorityStore(pool)
        await store.require_ready()
        await store.insert(
            verified,
            snapshot,
            hashlib.sha256(snapshot_bytes(snapshot)).hexdigest(),
            verified.expires_at,
        )
        app.state.job_authority = JobAuthority(store, callback=None)
        # Synthetic audit canaries prove the scoped owner snapshot is not
        # vacuously empty. They are fixture observations, never real activity.
        canaries = []
        async with owner.transaction():
            await owner.execute(
                "SELECT set_config('app.tenant_ids',"
                "'tenant_a,tenant_b,tenant_foreign',true)"
            )
            for tenant, workspace in (
                ("tenant_a", "workspace_a"),
                ("tenant_a", "workspace_b"),
                ("tenant_b", "workspace_a"),
                ("tenant_foreign", "workspace_foreign"),
            ):
                audit_id = str(uuid4())
                canaries.append(audit_id)
                await owner.execute(
                    "INSERT INTO agriculture_actor_audit "
                    "(id,tenant_id,workspace_id,actor_id,project_id,legacy_member_id,"
                    "legacy_user_code,legacy_role,policy_version,"
                    "authorization_version,request_id,action,resources,changes) "
                    "VALUES($1,$2,$3,'matrix_fixture',0,0,'matrix_fixture','fixture',"
                    "'agriculture-policy/1.0.0',"
                    "1,'matrix_fixture','fixture.canary','[]','[]')",
                    audit_id,
                    tenant,
                    workspace,
                )
        audit_snapshot = (await domain_snapshot(owner))["agriculture_actor_audit"]
        assert all(
            any(audit_id in row for row in audit_snapshot) for audit_id in canaries
        )
        async with pool.acquire() as connection:
            assert await connection.fetchval("SELECT current_user") == "flow_geo_app"
            assert not await connection.fetchval(
                "SELECT rolsuper OR rolbypassrls FROM pg_roles "
                "WHERE rolname=current_user"
            )
            assert (
                await connection.fetchval(
                    "SELECT count(*) FROM agriculture_private_objects"
                )
                == 0
            )
        yield (
            app,
            services,
            private,
            claims,
            item,
            report,
            snapshot,
            verified.body,
            owner,
            payload,
            farm_canaries[0]["farm_id"],
            parcel_canaries[0]["parcel_id"],
            asset_canaries[0]["asset_id"],
        )


def request_data(fixture, route):
    _, _, _, base, item, report, snapshot, job_body, _, _, _, _, _ = fixture
    method, path = ROUTES[route]
    current = report if route.startswith("report") else item
    path = path.format(
        tenant="tenant_a", workspace="workspace_a", object=current.object_id, version=1
    )
    claims = copy.deepcopy(base)
    typ = BUSINESS_TYP
    body = {}
    if route in {"ownership", "metadata"}:
        typ = SERVICE_TYP
        for name in (
            "actor",
            "resource_scope",
            "scopes",
            "authorization_version",
            "policy_version",
            "farm_ids",
            "resource_versions",
        ):
            claims.pop(name)
        claims["capability"] = (
            "ownership.resolve" if route == "ownership" else "object.resolve"
        )
        body = {"tenant_id": "tenant_a", "workspace_id": "workspace_a"}
        if route == "ownership":
            body["resources"] = [{"kind": "asset", "id": "asset_a"}]
        else:
            body.update(object_id=item.object_id, version=1)
    elif route.startswith("job"):
        for name in (
            "tenant_id",
            "workspace_id",
            "resource_versions",
            "farm_ids",
            "authorization_version",
            "policy_version",
        ):
            claims[name] = copy.deepcopy(snapshot[name])
        claims.update(
            actor={"kind": "user", "id": snapshot["actor_user_id"]},
            scopes=[snapshot["action"]],
            resource_scope=snapshot["resources"],
        )
        if route == "job_accept":
            body = {"snapshot": snapshot, "input": job_body["input"]}
        else:
            body = {
                "job_id": snapshot["job_id"],
                "snapshot_hash": hashlib.sha256(snapshot_bytes(snapshot)).hexdigest(),
                "input_hash": snapshot["input_hash"],
                "worker_id": "worker_fixture",
                "attempt": 1,
                "fencing": 1,
                "phase": "claim",
            }
    else:
        claims["resource_scope"] = [
            {"kind": current.resource_kind, "id": current.resource_id}
        ]
        claims["resource_versions"] = [
            {"resource": claims["resource_scope"][0], "revision": 1}
        ]
        claims["scopes"] = [
            "agri.report.read"
            if route.startswith("report")
            else "agri.asset_download.read"
        ]
    now = datetime.now(UTC).replace(microsecond=0)
    claims.update(
        issued_at=now.strftime("%Y-%m-%dT%H:%M:%SZ"),
        expires_at=(now + timedelta(seconds=30)).strftime("%Y-%m-%dT%H:%M:%SZ"),
        jti="matrix_" + uuid4().hex,
    )
    claims["request"] = {"method": method, "path": path}
    raw = json.dumps(body).encode() if method == "POST" else b""
    if method == "POST":
        claims["request"].update(
            canonicalization_version="rfc8785-safe-v1",
            payload_hash=hashlib.sha256(canonicalize(raw)).hexdigest(),
        )
        if typ == BUSINESS_TYP:
            claims["request"]["operation_id"] = snapshot["operation_id"]
    return method, path, raw, claims, typ


async def perform(fixture, route, case=None, app=None):
    if route in ASSET_OBJECT_ROUTES:
        method, path, raw, claims, typ = asset_object_request_data(fixture, route)
    elif route in ASSET_CATALOG_ROUTES:
        method, path, raw, claims, typ = catalog_request_data(fixture, route)
    elif route in PARCEL_ROUTES:
        method, path, raw, claims, typ = parcel_request_data(fixture, route)
    elif route in FARM_ROUTES:
        method, path, raw, claims, typ = farm_request_data(fixture, route)
    else:
        method, path, raw, claims, typ = request_data(fixture, route)
    key = fixture[2]
    expected = 401
    if case == "wrong_signature":
        key = Ed25519PrivateKey.generate()
    elif case == "audience":
        claims["audience"] = "flow-platform"
    elif case == "path":
        claims["request"]["path"] += "/other"
    elif case == "body":
        raw = b'{"unexpected":"private-marker"}'
    elif case == "expired":
        now = datetime.now(UTC)
        claims["issued_at"] = (now - timedelta(seconds=25)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
        claims["expires_at"] = (now - timedelta(seconds=1)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )
    elif case in {"tenant", "workspace"}:
        claims[case + "_id"] = case + "_foreign"
        expected = 404 if route == "job_checkpoint" else 403
    elif case == "epoch":
        claims["authorization_version"] += 1
        expected = 403
    token = sign(claims, key, {"alg": "EdDSA", "typ": typ, "kid": "fixture_key"})
    headers = {"Content-Type": "application/json"}
    if case != "unsigned":
        headers["Authorization"] = "Bearer " + token
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app or fixture[0]), base_url="http://fixture"
    ) as client:
        response = await client.request(method, path, content=raw, headers=headers)
    return response, expected


def farm_request_data(fixture, route):
    """Valid profile shapes; real current authorization lives in Go/GIS tests.

    These cases prove signature/path/body rejection BEFORE a missing provider,
    not cross-tenant authorization through a fake callback.
    """
    method, path, action = FARM_ROUTES[route]
    farm_id = fixture[10]
    path = path.format(farm_id=farm_id)
    base = fixture[3]
    now = datetime.now(UTC)
    claims = {
        name: base[name]
        for name in (
            "schema_version",
            "issuer",
            "subject",
            "audience",
            "actor",
            "tenant_id",
            "workspace_id",
            "authorization_version",
            "policy_version",
            "trace_id",
            "request_id",
        )
    }
    claims.update(
        issued_at=now.strftime("%Y-%m-%dT%H:%M:%SZ"),
        expires_at=(now + timedelta(seconds=25)).strftime("%Y-%m-%dT%H:%M:%SZ"),
        jti="jti_" + uuid4().hex,
    )
    operation = "op_" + uuid4().hex
    typ = WORKSPACE_TYP if not action.startswith("agri.") else BUSINESS_TYP
    body = {}
    if typ == WORKSPACE_TYP:
        claims.update(
            action=action,
            operation_id=operation,
            request_hash="0" * 64,
            grant_plan_hash="0" * 64,
            farm_ids=[farm_id] if action in {"farm.list", "farm.history.read"} else [],
        )
        if action in {"farm.create", "farm.preview.create", "farm.receipt"}:
            body = {
                "operation_id": operation,
                "request_hash": "0" * 64,
                "grant_plan_hash": "0" * 64,
            }
            if action != "farm.receipt":
                body["farm"] = farm_input()
        elif action == "farm.list":
            body = {
                "farm_ids": [farm_id],
                "status": "active",
                "limit": 50,
                "after_id": "",
                "search": "",
            }
        elif action == "farm.history.read":
            body = {"farm_id": farm_id}
    else:
        ref = {"kind": "farm", "id": farm_id}
        claims.update(
            scopes=[action],
            farm_ids=[farm_id],
            resource_scope=[ref],
            resource_versions=[{"resource": ref, "revision": 1}],
        )
        if method != "GET":
            body = {"expected_revision": 1}
            body.update(farm={"name": "Matrix new fixture name"}) if action.endswith(
                "update"
            ) else body.update(reason="Matrix fixture archive")
            if route in {"farm_update", "farm_archive"}:
                body.update(operation_id=operation, request_hash="0" * 64)
    raw = canonicalize(json.dumps(body).encode()) if method != "GET" else b""
    claims["request"] = {"method": method, "path": path}
    if method != "GET":
        claims["request"].update(
            canonicalization_version="rfc8785-safe-v1",
            payload_hash=hashlib.sha256(raw).hexdigest(),
            operation_id=operation,
        )
    return method, path, raw, claims, typ


def parcel_request_data(fixture, route):
    """Exact signed shapes; missing callbacks are never scope acceptance."""
    method, path, action = PARCEL_ROUTES[route]
    farm_id, parcel_id = fixture[10:12]
    path = path.format(farm_id=farm_id, parcel_id=parcel_id)
    _, _, _, claims, _ = farm_request_data(fixture, "farm_read")
    operation = "pop_" + uuid4().hex
    typ = BUSINESS_TYP
    if route == "parcel_receipt":
        typ = PARCEL_OPERATION_TYP
        for name in ("scopes", "farm_ids", "resource_scope", "resource_versions"):
            claims.pop(name)
        claims.update(
            action="parcel.receipt", operation_id=operation, request_hash="0" * 64
        )
        body = {"operation_id": operation, "request_hash": "0" * 64}
    else:
        parent = route in {"parcel_create", "parcel_preview_create", "parcel_list"}
        ref = {
            "kind": "farm" if parent else "parcel",
            "id": farm_id if parent else parcel_id,
        }
        claims.update(
            scopes=[action],
            farm_ids=[farm_id],
            resource_scope=[ref],
            resource_versions=[{"resource": ref, "revision": 1}],
        )
        if route == "parcel_list":
            body = {
                "access_mode": "farm",
                "parcel_ids": [],
                "after_id": "",
                "limit": 50,
            }
        elif route == "parcel_read":
            body = {"revision": 0, "at": ""}
        elif route == "parcel_history":
            body = {"after_revision": 0, "limit": 50}
        else:
            body = parcel_command(revise=not parent)
            body.update(operation_id=operation, request_hash="0" * 64)
            if "preview" in route:
                body.pop("operation_id")
                body.pop("request_hash")
    raw = canonicalize(json.dumps(body).encode())
    claims["request"] = {
        "method": method,
        "path": path,
        "canonicalization_version": "rfc8785-safe-v1",
        "payload_hash": hashlib.sha256(raw).hexdigest(),
        "operation_id": operation,
    }
    return method, path, raw, claims, typ


def catalog_request_data(fixture, route):
    method, path, action = ASSET_CATALOG_ROUTES[route]
    _, _, _, claims, _ = farm_request_data(fixture, "farm_read")
    for key in ("scopes", "farm_ids", "resource_scope", "resource_versions"):
        claims.pop(key)
    if route == "catalog_ownership":
        for key in ("actor", "authorization_version", "policy_version"):
            claims.pop(key)
        claims["capability"] = action
        body = {
            "tenant_id": claims["tenant_id"],
            "workspace_id": claims["workspace_id"],
            "asset_ids": [fixture[12]],
        }
        raw = canonicalize(json.dumps(body).encode())
        claims["request"] = {
            "method": method,
            "path": path,
            "canonicalization_version": "rfc8785-safe-v1",
            "payload_hash": hashlib.sha256(raw).hexdigest(),
        }
        return method, path, raw, claims, SERVICE_TYP
    operation, asset_id = "aop_" + uuid4().hex, fixture[12]
    ids = (
        []
        if action
        in {"asset.create", "asset.preview.create", "asset.receipt", "asset.context"}
        else [asset_id]
    )
    claims.update(
        action=action,
        operation_id=operation,
        request_hash="0" * 64,
        grant_plan_hash="0" * 64,
        asset_ids=ids,
    )
    if action == "asset.context":
        body = {}
    elif action == "asset.list":
        body = {"asset_ids": ids, "limit": 50, "after_id": ""}
    elif action == "asset.read":
        body = {"asset_id": asset_id, "version": 1}
    else:
        body = {
            "operation_id": operation,
            "request_hash": "0" * 64,
            "grant_plan_hash": "0" * 64,
        }
        if action != "asset.receipt":
            body.update(
                asset_id=asset_id if ids else None,
                expected_revision=1 if ids else 0,
                metadata=asset_metadata(),
            )
    raw = canonicalize(json.dumps(body).encode())
    claims["request"] = {
        "method": method,
        "path": path,
        "canonicalization_version": "rfc8785-safe-v1",
        "payload_hash": hashlib.sha256(raw).hexdigest(),
        "operation_id": operation,
    }
    return method, path, raw, claims, ASSET_TYP


def asset_object_request_data(fixture, route):
    """Signature grammar only; no fake ready state or download grant installed."""
    method, path = ASSET_OBJECT_ROUTES[route]
    object_id = fixture[4].object_id
    path = path.format(
        tenant="tenant_a", workspace="workspace_a", object=object_id, version=1
    )
    _, _, _, claims, _ = farm_request_data(fixture, "farm_read")
    for key in ("scopes", "farm_ids", "resource_scope", "resource_versions"):
        claims.pop(key)
    claims.update(
        action="agri.asset_download.read",
        asset_id=fixture[12],
        asset_revision=1,
        asset_version=1,
        ownership_hash="0" * 64,
        object_id=object_id,
        object_version=1,
    )
    claims["request"] = {"method": method, "path": path}
    return method, path, b"", claims, ASSET_OBJECT_TYP


def assert_private_denial(response, method):
    assert "no-store" in response.headers["cache-control"]
    assert not any(
        name in response.headers
        for name in ("location", "etag", "content-disposition", "content-range")
    )
    if method == "HEAD":
        assert response.content == b""
    else:
        if response.request.url.path.startswith(
            (PREFIX + "/farms", PREFIX + "/parcels", PREFIX + "/assets")
        ):
            assert set(response.json()) == {"error"}
            assert set(response.json()["error"]) == {"code"}
            assert isinstance(response.json()["error"]["code"], str)
        else:
            assert set(response.json()) == {"code"}
            assert isinstance(response.json()["code"], str)
        for secret in (
            "tenant_a",
            "workspace_a",
            "asset_a",
            "report_fixture",
            "object_key",
            "snapshot",
            "private-marker",
            "actual synthetic",
        ):
            assert secret not in response.text


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", ROUTES)
@pytest.mark.parametrize("case", CASES)
async def test_isolation_signed_route_negative_no_domain_effect(
    isolation_pg, route, case
):
    before = await domain_snapshot(isolation_pg[8])
    response, expected = await perform(isolation_pg, route, case)
    assert response.status_code == expected
    assert_private_denial(response, ROUTES[route][0])
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", FARM_ROUTES)
@pytest.mark.parametrize("case", FARM_SIGNATURE_CASES)
async def test_isolation_farm_signature_negative_no_domain_effect(
    isolation_pg, route, case
):
    before = await domain_snapshot(isolation_pg[8])
    response, expected = await perform(isolation_pg, route, case)
    assert response.status_code == expected == 401
    assert_private_denial(response, FARM_ROUTES[route][0])
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", FARM_ROUTES)
async def test_isolation_farm_missing_provider_closed(isolation_pg, route):
    services = copy.copy(isolation_pg[1])
    services.ownership = services.authority = services.farm_store = (
        services.farm_authority
    ) = None
    before = await domain_snapshot(isolation_pg[8])
    response, _ = await perform(isolation_pg, route, app=create_app(services))
    assert response.status_code == 503
    assert_private_denial(response, FARM_ROUTES[route][0])
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", PARCEL_ROUTES)
@pytest.mark.parametrize("case", FARM_SIGNATURE_CASES)
async def test_isolation_parcel_signature_negative_no_domain_effect(
    isolation_pg, route, case
):
    before = await domain_snapshot(isolation_pg[8])
    response, expected = await perform(isolation_pg, route, case)
    assert response.status_code == expected == 401
    assert_private_denial(response, PARCEL_ROUTES[route][0])
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", PARCEL_ROUTES)
async def test_isolation_parcel_missing_provider_closed(isolation_pg, route):
    services = copy.copy(isolation_pg[1])
    services.ownership = services.authority = services.parcel_store = (
        services.parcel_authority
    ) = None
    before = await domain_snapshot(isolation_pg[8])
    response, _ = await perform(isolation_pg, route, app=create_app(services))
    assert response.status_code == 503
    assert_private_denial(response, PARCEL_ROUTES[route][0])
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", ASSET_CATALOG_ROUTES)
@pytest.mark.parametrize("case", FARM_SIGNATURE_CASES)
async def test_isolation_catalog_signature_negative_no_domain_effect(
    isolation_pg, route, case
):
    before = await domain_snapshot(isolation_pg[8])
    response, expected = await perform(isolation_pg, route, case)
    assert response.status_code == expected == 401
    assert_private_denial(response, ASSET_CATALOG_ROUTES[route][0])
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", ASSET_CATALOG_ROUTES)
async def test_isolation_catalog_missing_provider_closed(isolation_pg, route):
    services = copy.copy(isolation_pg[1])
    services.asset_catalog_store = services.asset_catalog_authority = None
    services.asset_catalog_ownership = None
    before = await domain_snapshot(isolation_pg[8])
    response, _ = await perform(isolation_pg, route, app=create_app(services))
    assert response.status_code == 503
    assert_private_denial(response, ASSET_CATALOG_ROUTES[route][0])
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", ASSET_OBJECT_ROUTES)
@pytest.mark.parametrize("case", FARM_SIGNATURE_CASES)
async def test_isolation_asset_object_signature_negative_no_domain_effect(
    isolation_pg, route, case
):
    before = await domain_snapshot(isolation_pg[8])
    response, expected = await perform(isolation_pg, route, case)
    assert response.status_code == expected == 401
    assert_private_denial(response, ASSET_OBJECT_ROUTES[route][0])
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", ASSET_OBJECT_ROUTES)
async def test_isolation_asset_object_missing_provider_closed(isolation_pg, route):
    services = copy.copy(isolation_pg[1])
    services.asset_catalog_ownership = services.asset_object_authority = (
        services.object_store
    ) = services.object_storage = None
    before = await domain_snapshot(isolation_pg[8])
    response, _ = await perform(isolation_pg, route, app=create_app(services))
    assert response.status_code == 503
    assert_private_denial(response, ASSET_OBJECT_ROUTES[route][0])
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", ("job_accept", "job_checkpoint"))
async def test_isolation_job_snapshot_epoch_no_domain_effect(isolation_pg, route):
    before = await domain_snapshot(isolation_pg[8])
    response, expected = await perform(isolation_pg, route, "epoch")
    assert response.status_code == expected
    assert_private_denial(response, "POST")
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
@pytest.mark.parametrize("route", ROUTES)
async def test_isolation_production_missing_dependencies_closed(isolation_pg, route):
    services = copy.copy(isolation_pg[1])
    services.ownership = services.authority = services.object_store = (
        services.object_storage
    ) = None
    production = create_app(services)  # job authority intentionally None.
    before = await domain_snapshot(isolation_pg[8])
    response, _ = await perform(isolation_pg, route, app=production)
    assert response.status_code == 503
    assert_private_denial(response, ROUTES[route][0])
    assert await domain_snapshot(isolation_pg[8]) == before


@pytest.mark.asyncio(loop_scope="module")
async def test_isolation_public_health_legacy_absent_and_pg_controls(isolation_pg):
    before = await domain_snapshot(isolation_pg[8])
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=isolation_pg[0]), base_url="http://fixture"
    ) as client:
        response = await client.get("/health")
        assert response.status_code == 200
        assert response.json() == {"status": "ok", "agriculture_content_ready": False}
        for path in (
            "/docs",
            "/redoc",
            "/openapi.json",
            "/api/plots",
            "/api/tasks",
            "/internal/agri/v1/delegation/check",
        ):
            response = await client.get(path)
            assert response.status_code == 404 and response.json() == {
                "detail": "Not Found"
            }
    for route in ("ownership", "metadata"):
        response, _ = await perform(isolation_pg, route)
        assert response.status_code == 200
        if route == "ownership":
            assert response.json()["resources"][0]["resource"] == {
                "kind": "asset",
                "id": "asset_a",
            }
        else:
            assert response.json()["object_id"] == isolation_pg[4].object_id
    response, _ = await perform(isolation_pg, "catalog_ownership")
    assert response.status_code == 200
    fact = response.json()["resources"][0]
    assert fact["resource"] == {"kind": "asset", "id": isolation_pg[12]}
    assert fact["farm_ids"] == [] and fact["status"] == "validating"
    assert fact["revision"] == 1
    assert "metadata" not in fact and "object_key" not in fact
    assert await domain_snapshot(isolation_pg[8]) == before
