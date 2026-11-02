"""GIS farm authority; scoped short transactions and immutable command receipts."""

import hashlib
import json
import re
from contextlib import asynccontextmanager
from datetime import UTC, datetime
from decimal import Decimal
from uuid import uuid4

import asyncpg

from .farms_config import FarmDictionary
from .farms_geometry import measure_geometry
from .identity.canonical import canonicalize
from .identity.providers import Ownership
from .identity.verify import ID, IdentityError, Verified, require

ZERO_HASH = "0" * 64
FARM_FIELDS = frozenset(
    {
        "external_code",
        "name",
        "operator_name",
        "contact",
        "region_code",
        "dictionary_version",
        "timezone",
        "area_unit",
        "geometry",
        "source_crs",
        "coordinate_order",
        "data_class",
    }
)
FARM_ID = re.compile(r"farm_[0-9a-f]{32}\Z")
HASH = re.compile(r"[0-9a-f]{64}\Z")


def canonical(value):
    return canonicalize(json.dumps(value, ensure_ascii=False, allow_nan=False).encode())


def digest(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def document(value):
    return json.loads(value) if isinstance(value, str) else value


def text_field(value, limit, *, empty=False):
    require(
        isinstance(value, str)
        and len(value) <= limit
        and (empty or bool(value.strip()))
        and not any(ord(c) < 32 for c in value),
        "INVALID_ARGUMENT",
        400,
    )
    return value


def validate_fields(value):
    require(
        isinstance(value, dict)
        and set(value) <= FARM_FIELDS
        and FARM_FIELDS - {"contact"} <= set(value),
        "INVALID_ARGUMENT",
        400,
    )
    result = dict(value)
    require(
        isinstance(value["external_code"], str)
        and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", value["external_code"]),
        "INVALID_ARGUMENT",
        400,
    )
    for name, limit in (
        ("name", 200),
        ("operator_name", 200),
        ("region_code", 128),
        ("dictionary_version", 128),
        ("timezone", 128),
    ):
        text_field(value[name], limit)
    contact = value.get("contact", {})
    require(
        isinstance(contact, dict) and set(contact) <= {"name", "email", "phone"},
        "INVALID_ARGUMENT",
        400,
    )
    for name, field in contact.items():
        text_field(field, {"name": 120, "email": 254, "phone": 40}[name], empty=True)
    require(value["data_class"] in ("demo", "operational"), "INVALID_ARGUMENT", 400)
    result["contact"] = dict(contact)
    return result


class FarmStore:
    def __init__(self, pool, dictionary: FarmDictionary | None):
        self.pool, self.dictionary = pool, dictionary

    async def require_ready(self):
        from .domain_boundary import require_domain_boundary

        async with self.pool.acquire(timeout=3) as conn:
            await require_domain_boundary(conn)

    def configuration(self):
        require(self.dictionary is not None, "DEPENDENCY_UNAVAILABLE", 503)
        return self.dictionary

    async def prepare(self, value):
        value = validate_fields(value)
        config = self.configuration()
        config.validate_selection(
            value["dictionary_version"],
            value["region_code"],
            value["timezone"],
            value["area_unit"],
        )
        # Stateless bounded geometry work; no long domain transaction is held.
        async with self.pool.acquire(timeout=3) as conn:
            geometry = await measure_geometry(
                conn, value["geometry"], value["source_crs"], value["coordinate_order"]
            )
        return value, geometry

    @asynccontextmanager
    async def transaction(self, verified):
        require(isinstance(verified, Verified), "trusted_database_scope_required", 403)
        c = verified.claims
        require(
            all(
                isinstance(c.get(k), str) and ID.fullmatch(c[k])
                for k in ("tenant_id", "workspace_id")
            ),
            "trusted_database_scope_required",
            403,
        )
        require(datetime.now(UTC) < verified.expires_at, "delegation_expired", 401)
        async with self.pool.acquire(timeout=3) as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.tenant_ids',$1,true),"
                    "set_config('app.workspace_id',$2,true)",
                    c["tenant_id"],
                    c["workspace_id"],
                )
                await conn.execute(
                    "SET LOCAL statement_timeout='3s'; SET LOCAL lock_timeout='2s'"
                )
                yield conn
                require(
                    await conn.fetchval(
                        "SELECT clock_timestamp()<$1", verified.expires_at
                    ),
                    "delegation_expired",
                    401,
                )

    def dto(self, row):
        provenance = document(row["provenance"])
        area = row["area_m2"]
        return {
            "id": row["farm_id"],
            "workspace_id": row["workspace_id"],
            **{
                k: row[k]
                for k in (
                    "external_code",
                    "name",
                    "operator_name",
                    "region_code",
                    "dictionary_version",
                    "timezone",
                    "area_unit",
                    "data_class",
                    "status",
                    "revision",
                )
            },
            "contact": document(row["contact"]),
            "geometry": document(row["geometry_json"]),
            "source_crs": provenance["source_crs"],
            "coordinate_order": provenance["coordinate_order"],
            "area_m2": format(area, "f"),
            "area_ha": format(area / Decimal(10000), "f"),
            "area_mu": format(area * Decimal(3) / Decimal(2000), "f"),
            "created_at": row["created_at"].isoformat().replace("+00:00", "Z"),
            "updated_at": row["updated_at"].isoformat().replace("+00:00", "Z"),
            "allowed_actions": [],
        }

    async def _row(self, conn, farm_id, *, lock=False, archived=False):
        require(
            isinstance(farm_id, str) and FARM_ID.fullmatch(farm_id),
            "RESOURCE_NOT_FOUND",
            404,
        )
        row = await conn.fetchrow(
            "SELECT *,ST_AsGeoJSON(geom,15)::jsonb AS geometry_json "
            "FROM agriculture_farms WHERE farm_id=$1"
            + ("" if archived else " AND status='active'")
            + (" FOR UPDATE" if lock else ""),
            farm_id,
        )
        require(row is not None, "RESOURCE_NOT_FOUND", 404)
        return row

    async def get(self, verified, farm_id, *, archived=False):
        async with self.transaction(verified) as conn:
            return self.dto(await self._row(conn, farm_id, archived=archived))

    async def list(self, verified, ids, status, limit, after_id, search):
        require(
            isinstance(ids, list)
            and 1 <= len(ids) <= 200
            and ids == sorted(set(ids))
            and all(isinstance(x, str) and FARM_ID.fullmatch(x) for x in ids),
            "INVALID_ARGUMENT",
            400,
        )
        require(
            status in ("active", "archived")
            and type(limit) is int
            and 1 <= limit <= 200
            and isinstance(after_id, str)
            and (not after_id or FARM_ID.fullmatch(after_id)),
            "INVALID_ARGUMENT",
            400,
        )
        text_field(search, 200, empty=True)
        async with self.transaction(verified) as conn:
            rows = await conn.fetch(
                "SELECT *,ST_AsGeoJSON(geom,15)::jsonb AS geometry_json "
                "FROM agriculture_farms "
                "WHERE farm_id=ANY($1::text[]) AND status=$2 AND farm_id>$3 AND "
                "($4='' OR position(lower($4) in lower(name))>0 "
                "OR position(lower($4) in lower(external_code))>0) "
                "ORDER BY farm_id LIMIT $5",
                ids,
                status,
                after_id,
                search,
                limit + 1,
            )
            more = len(rows) > limit
            return {
                "items": [self.dto(r) for r in rows[:limit]],
                "has_more": more,
                "next_after_id": rows[limit - 1]["farm_id"] if more else "",
            }

    async def _existing(
        self,
        conn,
        verified,
        operation,
        request_hash,
        grant_hash,
        *,
        kind=None,
        farm_id=None,
    ):
        c = verified.claims
        row = await conn.fetchrow(
            "SELECT * FROM agriculture_farm_commands WHERE operation_id=$1", operation
        )
        if row is not None:
            require(
                row["request_hash"] == request_hash
                and row["grant_plan_hash"] == grant_hash
                and row["actor_id"] == c["actor"]["id"],
                "IDEMPOTENCY_CONFLICT",
                409,
            )
            require(
                (kind is None or row["operation_kind"] == kind)
                and (farm_id is None or row["farm_id"] == farm_id),
                "IDEMPOTENCY_CONFLICT",
                409,
            )
            return document(row["receipt_json"])
        return None

    async def receipt(self, verified, operation, request_hash, grant_hash):
        async with self.transaction(verified) as conn:
            result = await self._existing(
                conn, verified, operation, request_hash, grant_hash
            )
            require(result is not None, "RESOURCE_NOT_FOUND", 404)
            return result

    async def _record(
        self, conn, verified, operation, request_hash, grant_hash, kind, row, reason=""
    ):
        c = verified.claims
        farm_id = row["farm_id"]
        revision = row["revision"]
        event_type = {
            "farm.create": "agri.farm.created",
            "farm.update": "agri.farm.updated",
            "farm.archive": "agri.farm.archived",
        }[kind]
        receipt = {
            "schema_version": "1.0",
            "operation_id": operation,
            "tenant_id": c["tenant_id"],
            "workspace_id": c["workspace_id"],
            "request_hash": request_hash,
            "grant_plan_hash": grant_hash,
            "farm_id": farm_id,
            "revision": revision,
            "event_id": "evt_" + uuid4().hex,
            "event_type": event_type,
            "state": "applied",
            "producer": "agri-gis",
            "aggregate_revision": revision,
        }
        receipt["payload_hash"] = digest(receipt)
        snapshot = self.dto(row)
        snapshot["change"] = {"operation_kind": kind, "reason": reason}
        snapshot["geometry_hash"] = row["geometry_hash"]
        snapshot["provenance"] = document(row["provenance"])
        await conn.execute(
            "INSERT INTO agriculture_farm_revisions "
            "(tenant_id,workspace_id,farm_id,revision,actor_id,operation_id,"
            "snapshot_json,snapshot_hash) "
            "VALUES($1,$2,$3,$4,$5,$6,$7::jsonb,$8)",
            c["tenant_id"],
            c["workspace_id"],
            farm_id,
            revision,
            c["actor"]["id"],
            operation,
            canonical(snapshot).decode(),
            digest(snapshot),
        )
        await conn.execute(
            "INSERT INTO agriculture_farm_commands "
            "(tenant_id,workspace_id,operation_id,actor_id,operation_kind,"
            "idempotency_key_hash,request_hash,grant_plan_hash,farm_id,receipt_json) "
            "VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb)",
            c["tenant_id"],
            c["workspace_id"],
            operation,
            c["actor"]["id"],
            kind,
            hashlib.sha256(operation.encode()).hexdigest(),
            request_hash,
            grant_hash,
            farm_id,
            canonical(receipt).decode(),
        )
        await conn.execute(
            "INSERT INTO agriculture_farm_outbox "
            "(tenant_id,workspace_id,event_id,operation_id,event_type,farm_id,"
            "aggregate_revision,payload_json,payload_hash) "
            "VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9)",
            c["tenant_id"],
            c["workspace_id"],
            receipt["event_id"],
            operation,
            event_type,
            farm_id,
            revision,
            canonical(receipt).decode(),
            receipt["payload_hash"],
        )
        return receipt

    async def _dictionary(self, conn, verified):
        c = verified.claims
        config = self.configuration()
        snapshot = config.snapshot()
        version = snapshot["dictionary_version"]
        checksum = digest(snapshot)
        await conn.execute(
            "INSERT INTO agriculture_farm_dictionaries "
            "(tenant_id,workspace_id,version,document,document_hash) "
            "VALUES($1,$2,$3,$4::jsonb,$5) "
            "ON CONFLICT DO NOTHING",
            c["tenant_id"],
            c["workspace_id"],
            version,
            canonical(snapshot).decode(),
            checksum,
        )
        stored = await conn.fetchval(
            "SELECT document_hash FROM agriculture_farm_dictionaries WHERE version=$1",
            version,
        )
        require(stored == checksum, "DICTIONARY_VERSION_CONFLICT", 409)

    async def execute(self, verified, kind, body, *, farm_id=None, before_write=None):
        c = verified.claims
        operation = body.get("operation_id")
        request_hash = body.get("request_hash")
        grant_hash = body.get("grant_plan_hash", ZERO_HASH)
        require(
            isinstance(operation, str)
            and ID.fullmatch(operation)
            and all(
                isinstance(x, str) and HASH.fullmatch(x)
                for x in (request_hash, grant_hash)
            ),
            "INVALID_ARGUMENT",
            400,
        )
        require(
            kind in ("farm.create", "farm.update", "farm.archive"),
            "INVALID_ARGUMENT",
            400,
        )
        # Re-authentication is performed by the route before even a receipt replay.
        async with self.transaction(verified) as conn:
            old = await self._existing(
                conn,
                verified,
                operation,
                request_hash,
                grant_hash,
                kind=kind,
                farm_id=farm_id,
            )
            if old is not None:
                return old
        prepared = None
        if kind == "farm.create":
            prepared = await self.prepare(body.get("farm"))
        elif kind == "farm.update":
            current = await self.get(verified, farm_id)
            changes = body.get("farm")
            require(
                isinstance(changes, dict)
                and changes
                and set(changes) <= FARM_FIELDS - {"external_code", "data_class"},
                "INVALID_ARGUMENT",
                400,
            )
            value = {k: current[k] for k in FARM_FIELDS}
            value.update(changes)
            prepared = await self.prepare(value)
        else:
            text_field(body.get("reason"), 500)
        if kind != "farm.create":
            require("expected_revision" in body, "PRECONDITION_REQUIRED", 428)
            require(
                type(body["expected_revision"]) is int
                and body["expected_revision"] >= 1,
                "INVALID_ARGUMENT",
                400,
            )
        if before_write is not None:
            await before_write()
        try:
            async with self.transaction(verified) as conn:
                await conn.execute(
                    "SELECT pg_advisory_xact_lock(hashtextextended($1,0))",
                    c["tenant_id"] + ":" + operation,
                )
                old = await self._existing(
                    conn,
                    verified,
                    operation,
                    request_hash,
                    grant_hash,
                    kind=kind,
                    farm_id=farm_id,
                )
                if old is not None:
                    return old
                if kind != "farm.create":
                    current = await self._row(conn, farm_id, lock=True)
                    require(
                        current["revision"] == body["expected_revision"],
                        "REVISION_CONFLICT",
                        409,
                    )
                if kind == "farm.archive":
                    await conn.execute(
                        "UPDATE agriculture_farms SET status='archived',"
                        "revision=revision+1,state_revision=state_revision+1,"
                        "updated_at=clock_timestamp() WHERE farm_id=$1",
                        farm_id,
                    )
                else:
                    value, geometry = prepared
                    if kind == "farm.update":
                        # Parcel mutations hold a shared parent lock before any
                        # child lock. Our exclusive farm lock therefore keeps
                        # this current-revision containment snapshot stable.
                        covered = await conn.fetchval(
                            "SELECT NOT EXISTS (SELECT 1 FROM agriculture_parcels p "
                            "LEFT JOIN agriculture_parcel_revisions r "
                            "ON r.tenant_id=p.tenant_id "
                            "AND r.workspace_id=p.workspace_id "
                            "AND r.parcel_id=p.parcel_id "
                            "AND r.revision=p.current_revision "
                            "WHERE p.tenant_id=$1 AND p.workspace_id=$2 "
                            "AND p.farm_id=$3 "
                            "AND p.status='active' AND "
                            "ST_CoveredBy(r.geom,ST_SetSRID("
                            "ST_GeomFromGeoJSON($4::text),4326)) IS NOT TRUE)",
                            c["tenant_id"],
                            c["workspace_id"],
                            farm_id,
                            canonical(value["geometry"]).decode(),
                        )
                        require(covered is True, "STATE_CONFLICT", 409)
                    await self._dictionary(conn, verified)
                    provenance = {
                        k: v
                        for k, v in geometry.items()
                        if k not in ("geometry", "area_m2")
                    }
                    args = [
                        value[k] for k in ("external_code", "name", "operator_name")
                    ]
                    args += [canonical(value["contact"]).decode()]
                    args += [
                        value[k]
                        for k in (
                            "region_code",
                            "dictionary_version",
                            "timezone",
                            "area_unit",
                        )
                    ]
                    args += [
                        canonical(value["geometry"]).decode(),
                        geometry["geometry_hash"],
                        canonical(provenance).decode(),
                    ]
                    if kind == "farm.create":
                        farm_id = "farm_" + uuid4().hex
                        await conn.execute(
                            "INSERT INTO agriculture_farms (external_code,name,"
                            "operator_name,contact,region_code,dictionary_version,"
                            "timezone,area_unit,geom,geometry_hash,provenance,"
                            "tenant_id,workspace_id,farm_id,data_class,status,"
                            "revision,state_revision) "
                            "VALUES($1,$2,$3,$4::jsonb,$5,$6,$7,$8,"
                            "ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($9),4326)),"
                            "$10,$11::jsonb,$12,$13,$14,$15,'active',1,1)",
                            *args,
                            c["tenant_id"],
                            c["workspace_id"],
                            farm_id,
                            value["data_class"],
                        )
                    else:
                        await conn.execute(
                            "UPDATE agriculture_farms SET external_code=$1,name=$2,"
                            "operator_name=$3,contact=$4::jsonb,"
                            "region_code=$5,dictionary_version=$6,timezone=$7,area_unit=$8,"
                            "geom=ST_Multi(ST_SetSRID(ST_GeomFromGeoJSON($9),4326)),"
                            "geometry_hash=$10,provenance=$11::jsonb,"
                            "revision=revision+1,state_revision=state_revision+1,"
                            "updated_at=clock_timestamp() WHERE farm_id=$12",
                            *args,
                            farm_id,
                        )
                row = await self._row(conn, farm_id, archived=True)
                return await self._record(
                    conn,
                    verified,
                    operation,
                    request_hash,
                    grant_hash,
                    kind,
                    row,
                    body.get("reason", ""),
                )
        except asyncpg.UniqueViolationError:
            raise IdentityError("IDEMPOTENCY_OR_EXTERNAL_CODE_CONFLICT", 409) from None


class FarmOwnership:
    def __init__(self, pool):
        self.pool = pool

    async def resolve(self, tenant_id, workspace_id, resources):
        require(
            all(r.get("kind") == "farm" for r in resources), "resource_not_found", 404
        )
        async with self.pool.acquire(timeout=3) as conn:
            async with conn.transaction():
                await conn.execute(
                    "SELECT set_config('app.tenant_ids',$1,true),"
                    "set_config('app.workspace_id',$2,true)",
                    tenant_id,
                    workspace_id,
                )
                rows = await conn.fetch(
                    "SELECT farm_id,revision FROM agriculture_farms "
                    "WHERE farm_id=ANY($1::text[]) AND status='active'",
                    [r["id"] for r in resources],
                )
                return [
                    Ownership(
                        "farm",
                        r["farm_id"],
                        tenant_id,
                        workspace_id,
                        (r["farm_id"],),
                        r["revision"],
                    )
                    for r in rows
                ]
