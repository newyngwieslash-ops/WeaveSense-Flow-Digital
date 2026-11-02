"""GIS-owned asset metadata/version ledger; no upload or storage-key API."""

import json
from contextlib import asynccontextmanager
from dataclasses import dataclass
from datetime import UTC, datetime
from uuid import uuid4

import asyncpg

from .asset_catalog_rules import (
    ASSET_ID,
    AssetCatalogRights,
    digest,
    validate_metadata,
    validate_successor,
)
from .identity.verify import IdentityError, require

TABLES = (
    "agriculture_asset_catalog",
    "agriculture_asset_catalog_versions",
    "agriculture_asset_catalog_commands",
    "agriculture_asset_catalog_outbox",
)


def document(value):
    return json.loads(value) if isinstance(value, str) else value


def safe_rights_projection(snapshot):
    return {
        key: snapshot[key]
        for key in (
            "status",
            "review_ref",
            "source_id",
            "declaration",
            "reviewed_sources",
            "snapshot_hash",
        )
    }


@dataclass(frozen=True)
class AssetCatalogOwnershipFact:
    asset_id: str
    tenant_id: str
    workspace_id: str
    farm_ids: tuple[str, ...]
    revision: int
    status: str


class AssetCatalogOwnership:
    def __init__(self, pool):
        self.pool = pool

    async def resolve_asset(self, tenant_id, workspace_id, asset_ids):
        require(
            isinstance(asset_ids, list)
            and 1 <= len(asset_ids) <= 200
            and all(isinstance(x, str) and ASSET_ID.fullmatch(x) for x in asset_ids)
            and len(set(asset_ids)) == len(asset_ids),
            "INVALID_ARGUMENT",
            400,
        )
        async with self.pool.acquire(timeout=3) as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.tenant_ids',$1,true),set_config('ap"
                    "p.workspace_id',$2,true)",
                    tenant_id,
                    workspace_id,
                )
                rows = await conn.fetch(
                    "SELECT asset_id,farm_id,current_revision,status FROM agric"
                    "ulture_asset_catalog WHERE tenant_id=$1 AND workspace_id=$"
                    "2 AND asset_id=ANY($3::text[]) AND status<>'revoked'",
                    tenant_id,
                    workspace_id,
                    asset_ids,
                )
                return [
                    AssetCatalogOwnershipFact(
                        r["asset_id"],
                        tenant_id,
                        workspace_id,
                        (r["farm_id"],) if r["farm_id"] else (),
                        r["current_revision"],
                        r["status"],
                    )
                    for r in rows
                ]


class AssetCatalogStore:
    def __init__(self, pool, rights=None):
        self.pool = pool
        self.rights = rights or AssetCatalogRights()

    async def require_ready(self):
        async with self.pool.acquire(timeout=3) as conn:
            for name in TABLES:
                ready = await conn.fetchval(
                    """SELECT current_user='flow_geo_app'
 AND c.relrowsecurity AND c.relforcerowsecurity
 AND pg_get_userbyid(c.relowner)='flow_geo_migrate' AND
has_table_privilege(current_user,c.oid,'SELECT')
 AND has_table_privilege(current_user,c.oid,'INSERT')
 AND NOT has_table_privilege(current_user,c.oid,'DELETE,TRUNCATE')
 AND EXISTS(SELECT 1 FROM pg_policy p WHERE p.polrelid=c.oid AND
p.polname='agriculture_asset_scope' AND NOT p.polpermissive)
 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE
n.nspname='public' AND c.relname=$1""",
                    name,
                )
                require(ready is True, "CATALOG_DEPENDENCY_UNAVAILABLE", 503)

    @asynccontextmanager
    async def transaction(self, tenant, workspace, *, expires_at=None):
        require(
            isinstance(tenant, str)
            and tenant
            and isinstance(workspace, str)
            and workspace,
            "INVALID_SCOPE",
            403,
        )
        async with self.pool.acquire(timeout=3) as conn:
            async with conn.transaction():
                await conn.execute(
                    "SET LOCAL statement_timeout='3s'; SET LOCAL lock_timeout='2s'"
                )
                await conn.execute(
                    "SELECT set_config('app.tenant_ids',$1,true),set_config('ap"
                    "p.workspace_id',$2,true)",
                    tenant,
                    workspace,
                )
                yield conn
                if expires_at is not None:
                    await self.require_unexpired(conn, expires_at)

    @staticmethod
    async def require_unexpired(conn, expires_at):
        require(
            await conn.fetchval("SELECT clock_timestamp()<$1", expires_at),
            "delegation_expired",
            401,
        )

    async def get(self, tenant, workspace, asset_id, version=None):
        require(
            isinstance(asset_id, str) and ASSET_ID.fullmatch(asset_id),
            "ASSET_NOT_FOUND",
            404,
        )
        require(
            version is None
            or (type(version) is int and 1 <= version <= 9007199254740991),
            "INVALID_ARGUMENT",
            400,
        )
        async with self.transaction(tenant, workspace) as conn:
            row = await conn.fetchrow(
                """SELECT a.asset_id,a.farm_id,a.kind,a.data_class,
 a.current_revision,a.status,
 v.version,v.asset_version_id,v.metadata_json,v.metadata_hash,v.rights_snapshot,v.object_id,v.object_version,v.created_at
 FROM agriculture_asset_catalog a JOIN agriculture_asset_catalog_versions v
 USING(tenant_id,workspace_id,asset_id) WHERE a.tenant_id=$1
 AND a.workspace_id=$2 AND a.asset_id=$3
 AND v.version=coalesce($4::bigint,a.current_revision)""",
                tenant,
                workspace,
                asset_id,
                version,
            )
        require(row is not None, "ASSET_NOT_FOUND", 404)
        return self.view(row, workspace)

    @staticmethod
    def view(row, workspace):
        metadata, rights = (
            document(row["metadata_json"]),
            document(row["rights_snapshot"]),
        )
        return {
            "asset_id": row["asset_id"],
            "workspace_id": workspace,
            "farm_id": row["farm_id"],
            "revision": row["current_revision"],
            "version": row["version"],
            "asset_version_id": row["asset_version_id"],
            "kind": row["kind"],
            "data_class": row["data_class"],
            "name": metadata["name"],
            "status": row["status"],
            "metadata": metadata,
            "metadata_hash": row["metadata_hash"],
            "rights": safe_rights_projection(rights),
            "entity": None
            if row["object_id"] is None
            else {
                "object_id": row["object_id"],
                "object_version": row["object_version"],
            },
            "created_at": row["created_at"].isoformat(),
        }

    async def list(self, tenant, workspace, asset_ids, *, limit=50, after_id=""):
        require(
            isinstance(asset_ids, list)
            and 1 <= len(asset_ids) <= 200
            and all(isinstance(x, str) and ASSET_ID.fullmatch(x) for x in asset_ids)
            and len(set(asset_ids)) == len(asset_ids)
            and type(limit) is int
            and 1 <= limit <= 200
            and (
                after_id == ""
                or (isinstance(after_id, str) and ASSET_ID.fullmatch(after_id))
            ),
            "INVALID_ARGUMENT",
            400,
        )
        async with self.transaction(tenant, workspace) as conn:
            rows = await conn.fetch(
                """SELECT a.asset_id,a.farm_id,a.kind,a.data_class,
 a.current_revision,a.status,
 v.version,v.asset_version_id,v.metadata_json,v.metadata_hash,v.rights_snapshot,v.object_id,v.object_version,v.created_at
 FROM agriculture_asset_catalog a JOIN agriculture_asset_catalog_versions v
USING(tenant_id,workspace_id,asset_id)
 WHERE a.tenant_id=$1 AND a.workspace_id=$2
 AND a.asset_id=ANY($3::text[]) AND a.asset_id>$4
 AND v.version=a.current_revision ORDER BY a.asset_id LIMIT $5""",
                tenant,
                workspace,
                asset_ids,
                after_id,
                limit + 1,
            )
        items = [self.view(row, workspace) for row in rows[:limit]]
        return {
            "items": items,
            "has_more": len(rows) > limit,
            "next_cursor": items[-1]["asset_id"] if len(rows) > limit else "",
        }

    async def receipt(
        self, tenant, workspace, operation_id, actor_id, request_hash, grant_plan_hash
    ):
        async with self.transaction(tenant, workspace) as conn:
            row = await conn.fetchrow(
                "SELECT actor_id,request_hash,grant_plan_hash,receipt_json "
                "FROM agriculture_asset_catalog_commands WHERE tenant_id=$1"
                " AND workspace_id=$2 AND operation_id=$3",
                tenant,
                workspace,
                operation_id,
            )
        require(row is not None, "RECEIPT_NOT_FOUND", 404)
        require(
            (row["actor_id"], row["request_hash"], row["grant_plan_hash"])
            == (actor_id, request_hash, grant_plan_hash),
            "OPERATION_CONFLICT",
            409,
        )
        return document(row["receipt_json"])

    async def preview(
        self, tenant, workspace, action, metadata, asset_id=None, expected_revision=0
    ):
        prepared = await self._prepare(
            tenant, workspace, action, metadata, asset_id, expected_revision
        )
        return {**prepared, "rights": safe_rights_projection(prepared["rights"])}

    async def _prepare(
        self, tenant, workspace, action, metadata, asset_id=None, expected_revision=0
    ):
        require(action in {"asset.create", "asset.version"}, "INVALID_ARGUMENT", 400)
        if action == "asset.create":
            require(
                expected_revision == 0
                and type(expected_revision) is int
                and asset_id is None,
                "REVISION_CONFLICT",
                409,
            )
            metadata = validate_metadata(metadata)
        else:
            require(
                type(expected_revision) is int and expected_revision >= 1,
                "PRECONDITION_REQUIRED",
                428,
            )
            current = await self.get(tenant, workspace, asset_id)
            require(
                current["revision"] == expected_revision
                and current["status"] not in {"revoked", "rejected"},
                "REVISION_CONFLICT",
                409,
            )
            metadata = validate_successor(current["metadata"], metadata)
        if metadata["farm_id"] is not None:
            async with self.transaction(tenant, workspace) as conn:
                found = await conn.fetchval(
                    "SELECT EXISTS(SELECT 1 FROM agriculture_farms WHERE tenant"
                    "_id=$1 AND workspace_id=$2 AND farm_id=$3 AND status='acti"
                    "ve')",
                    tenant,
                    workspace,
                    metadata["farm_id"],
                )
            require(found, "RESOURCE_NOT_FOUND", 404)
        return {
            "metadata": metadata,
            "rights": self.rights.snapshot(tenant, workspace, metadata),
            "entity": None,
            "status": "validating",
        }

    async def execute(self, verified, action, body, *, before_write=None):
        c = verified.claims
        tenant, workspace, actor = c["tenant_id"], c["workspace_id"], c["actor"]["id"]
        require(
            isinstance(body, dict)
            and set(body)
            == {
                "operation_id",
                "request_hash",
                "grant_plan_hash",
                "asset_id",
                "expected_revision",
                "metadata",
            },
            "INVALID_ARGUMENT",
            400,
        )
        operation, request_hash, grant_hash = (
            body["operation_id"],
            body["request_hash"],
            body["grant_plan_hash"],
        )
        require(
            all(
                body[x] == c[x]
                for x in ("operation_id", "request_hash", "grant_plan_hash")
            )
            and c["action"] == action
            and c["asset_ids"]
            == ([] if action == "asset.create" else [body["asset_id"]]),
            "REQUEST_SCOPE_MISMATCH",
            403,
        )
        try:
            return await self.receipt(
                tenant, workspace, operation, actor, request_hash, grant_hash
            )
        except IdentityError as exc:
            if exc.status != 404:
                raise
        prepared = await self._prepare(
            tenant,
            workspace,
            action,
            body["metadata"],
            body["asset_id"],
            body["expected_revision"],
        )
        if before_write is not None:
            await before_write()
        require(datetime.now(UTC) < verified.expires_at, "delegation_expired", 401)
        try:
            async with self.transaction(
                tenant, workspace, expires_at=verified.expires_at
            ) as conn:
                await conn.execute(
                    "SELECT pg_advisory_xact_lock(hashtextextended($1,0))",
                    tenant + ":" + workspace + ":" + operation,
                )
                await self.require_unexpired(conn, verified.expires_at)
                old = await conn.fetchrow(
                    "SELECT actor_id,request_hash,grant_plan_hash,receipt_json "
                    "FROM agriculture_asset_catalog_commands WHERE tenant_id=$1"
                    " AND workspace_id=$2 AND operation_id=$3",
                    tenant,
                    workspace,
                    operation,
                )
                if old is not None:
                    require(
                        (old["actor_id"], old["request_hash"], old["grant_plan_hash"])
                        == (actor, request_hash, grant_hash),
                        "OPERATION_CONFLICT",
                        409,
                    )
                    return document(old["receipt_json"])
                asset = body["asset_id"] or "asset_" + uuid4().hex
                revision = body["expected_revision"] + 1
                version_id = "av_" + uuid4().hex
                if action == "asset.create":
                    await conn.execute(
                        "INSERT INTO agriculture_asset_catalog(tenant_id,workspace_"
                        "id,asset_id,farm_id,kind,data_class,current_revision,curre"
                        "nt_version_id,status) VALUES($1,$2,$3,$4,$5,$6,1,$7,'valid"
                        "ating')",
                        tenant,
                        workspace,
                        asset,
                        prepared["metadata"]["farm_id"],
                        prepared["metadata"]["kind"],
                        prepared["metadata"]["data_class"],
                        version_id,
                    )
                else:
                    current = await conn.fetchrow(
                        "SELECT current_revision,status FROM agriculture_asset_cata"
                        "log WHERE tenant_id=$1 AND workspace_id=$2 AND asset_id=$3"
                        " FOR UPDATE",
                        tenant,
                        workspace,
                        asset,
                    )
                    await self.require_unexpired(conn, verified.expires_at)
                    require(current is not None, "ASSET_NOT_FOUND", 404)
                    require(
                        current["current_revision"] == body["expected_revision"]
                        and current["status"] not in {"revoked", "rejected"},
                        "REVISION_CONFLICT",
                        409,
                    )
                    await conn.execute(
                        "UPDATE agriculture_asset_catalog SET current_revision=$4,c"
                        "urrent_version_id=$5,status='validating',updated_at=clock_"
                        "timestamp() WHERE tenant_id=$1 AND workspace_id=$2 AND ass"
                        "et_id=$3",
                        tenant,
                        workspace,
                        asset,
                        revision,
                        version_id,
                    )
                metadata = prepared["metadata"]
                await conn.execute(
                    "INSERT INTO agriculture_asset_catalog_versions(tenant_id,w"
                    "orkspace_id,asset_id,version,asset_version_id,metadata_jso"
                    "n,metadata_hash,rights_snapshot,created_by) VALUES($1,$2,$"
                    "3,$4,$5,$6::jsonb,$7,$8::jsonb,$9)",
                    tenant,
                    workspace,
                    asset,
                    revision,
                    version_id,
                    json.dumps(metadata),
                    digest(metadata),
                    json.dumps(prepared["rights"]),
                    actor,
                )
                event = (
                    "agri.asset.registered"
                    if action == "asset.create"
                    else "agri.asset.version_registered"
                )
                receipt = {
                    "schema_version": "1.0",
                    "producer": "agri-gis",
                    "operation_id": operation,
                    "tenant_id": tenant,
                    "workspace_id": workspace,
                    "request_hash": request_hash,
                    "grant_plan_hash": grant_hash,
                    "asset_id": asset,
                    "revision": revision,
                    "asset_version_id": version_id,
                    "aggregate_revision": revision,
                    "event_id": "evt_" + uuid4().hex,
                    "event_type": event,
                    "state": "applied",
                }
                receipt["payload_hash"] = digest(receipt)
                encoded = json.dumps(receipt)
                await conn.execute(
                    "INSERT INTO agriculture_asset_catalog_commands(tenant_id,w"
                    "orkspace_id,operation_id,actor_id,action,request_hash,gran"
                    "t_plan_hash,receipt_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8::"
                    "jsonb)",
                    tenant,
                    workspace,
                    operation,
                    actor,
                    action,
                    request_hash,
                    grant_hash,
                    encoded,
                )
                await conn.execute(
                    "INSERT INTO agriculture_asset_catalog_outbox(tenant_id,wor"
                    "kspace_id,event_id,operation_id,asset_id,revision,event_ty"
                    "pe,payload_json,payload_hash) VALUES($1,$2,$3,$4,$5,$6,$7,"
                    "$8::jsonb,$9)",
                    tenant,
                    workspace,
                    receipt["event_id"],
                    operation,
                    asset,
                    revision,
                    event,
                    encoded,
                    receipt["payload_hash"],
                )
                return receipt
        except (
            asyncpg.UniqueViolationError,
            asyncpg.CheckViolationError,
            asyncpg.ForeignKeyViolationError,
        ):
            raise IdentityError("STATE_CONFLICT", 409) from None

    async def authorize_use(
        self,
        tenant,
        workspace,
        asset_id,
        version,
        *,
        purpose,
        profile,
        require_entity=True,
    ):
        require(require_entity is True, "ENTITY_CHECK_REQUIRED", 400)
        value = await self.get(tenant, workspace, asset_id, version)
        require(
            value["status"] not in {"revoked", "quarantined", "rejected"},
            "ASSET_NOT_AVAILABLE",
            403,
        )
        result = self.rights.check(
            tenant, workspace, value["metadata"], purpose=purpose, profile=profile
        )
        if value["version"] != value["revision"]:
            current = await self.get(tenant, workspace, asset_id)
            current_result = self.rights.check(
                tenant, workspace, current["metadata"], purpose=purpose, profile=profile
            )
            result["checked_source_ids"] = sorted(
                set(result["checked_source_ids"])
                | set(current_result["checked_source_ids"])
            )
            obligations = {
                (x["source_id"], x["text"])
                for x in result["attributions"] + current_result["attributions"]
            }
            result["attributions"] = [
                {"source_id": source, "text": text}
                for source, text in sorted(obligations)
            ]
        if require_entity:
            require(
                value["status"] == "ready" and value["entity"] is not None,
                "ENTITY_NOT_READY",
                409,
            )
            async with self.transaction(tenant, workspace) as conn:
                allowed = await conn.fetchval(
                    "SELECT EXISTS(SELECT 1 FROM agriculture_private_objects WH"
                    "ERE tenant_id=$1 AND workspace_id=$2 AND object_id=$3 AND "
                    "version=$4 AND resource_kind='asset' AND resource_id=$5 AN"
                    "D state='active' AND distribution_allowed AND data_class=$"
                    "6 AND resource_revision=$7)",
                    tenant,
                    workspace,
                    value["entity"]["object_id"],
                    value["entity"]["object_version"],
                    asset_id,
                    profile,
                    value["version"],
                )
            require(allowed, "ENTITY_NOT_AVAILABLE", 403)
        return result
