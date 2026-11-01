"""Stable parcel identities and append-only geometry revisions in flow_geo."""

import re
from datetime import UTC, datetime
from decimal import Decimal
from uuid import uuid4

import asyncpg

from .farms_geometry import measure_geometry
from .farms_store import (
    FARM_ID,
    HASH,
    FarmStore,
    canonical,
    digest,
    document,
    text_field,
)
from .identity.providers import Ownership
from .identity.verify import ID, require

PARCEL_ID = re.compile(r"parcel_[0-9a-f]{32}\Z")
GEOMETRY_FIELDS = frozenset({"geometry", "source_crs", "coordinate_order"})
PARCEL_FIELDS = GEOMETRY_FIELDS | {"external_code", "name"}


def iso(value):
    return value.isoformat().replace("+00:00", "Z") if value is not None else None


def revision(value, *, zero=False):
    require(
        type(value) is int and (0 if zero else 1) <= value <= 9007199254740991,
        "INVALID_ARGUMENT",
        400,
    )
    return value


class ParcelStore:
    def __init__(self, pool):
        self.pool = pool
        # Reuse the existing short runtime transaction/scope and expiry boundary.
        self.scope = FarmStore(pool, None)

    def transaction(self, verified):
        return self.scope.transaction(verified)

    async def require_ready(self):
        await self.scope.require_ready()

    async def prepare(self, value, *, create=False):
        require(
            isinstance(value, dict)
            and set(value) == (PARCEL_FIELDS if create else GEOMETRY_FIELDS),
            "INVALID_ARGUMENT",
            400,
        )
        if create:
            require(
                isinstance(value["external_code"], str)
                and re.fullmatch(
                    r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", value["external_code"]
                ),
                "INVALID_ARGUMENT",
                400,
            )
            text_field(value["name"], 200)
        async with self.pool.acquire(timeout=3) as connection:
            return await measure_geometry(
                connection,
                value["geometry"],
                value["source_crs"],
                value["coordinate_order"],
            )

    async def _identity(self, conn, parcel_id, *, lock=False):
        require(
            isinstance(parcel_id, str) and PARCEL_ID.fullmatch(parcel_id),
            "RESOURCE_NOT_FOUND",
            404,
        )
        row = await conn.fetchrow(
            "SELECT * FROM agriculture_parcels WHERE parcel_id=$1 AND status='active'"
            + (" FOR UPDATE" if lock else ""),
            parcel_id,
        )
        require(row is not None, "RESOURCE_NOT_FOUND", 404)
        return row

    async def _farm(self, conn, farm_id, geometry=None, *, expected=None, lock=False):
        require(
            isinstance(farm_id, str) and FARM_ID.fullmatch(farm_id),
            "RESOURCE_NOT_FOUND",
            404,
        )
        row = await conn.fetchrow(
            "SELECT farm_id,revision,data_class FROM agriculture_farms "
            "WHERE farm_id=$1 AND status='active'" + (" FOR SHARE" if lock else ""),
            farm_id,
        )
        require(row is not None, "RESOURCE_NOT_FOUND", 404)
        if expected is not None:
            require(row["revision"] == expected, "REVISION_CONFLICT", 409)
        if geometry is not None:
            covered = await conn.fetchval(
                "SELECT ST_CoveredBy("
                "ST_SetSRID(ST_GeomFromGeoJSON($2::text),4326),geom) "
                "FROM agriculture_farms WHERE farm_id=$1",
                farm_id,
                canonical(geometry["geometry"]).decode(),
            )
            require(covered is True, "PARCEL_OUTSIDE_FARM", 422)
        return row

    def dto(self, identity, row):
        provenance = document(row["provenance"])
        area = row["area_m2"]
        return {
            "id": identity["parcel_id"],
            "farm_id": identity["farm_id"],
            "workspace_id": identity["workspace_id"],
            "external_code": identity["external_code"],
            "name": identity["name"],
            "data_class": identity["data_class"],
            "status": identity["status"],
            "geometry_revision": row["revision"],
            "geometry": document(row["geometry_json"]),
            "current_geometry_revision": identity["current_revision"],
            "geometry_hash": row["geometry_hash"],
            "source_crs": provenance["source_crs"],
            "coordinate_order": provenance["coordinate_order"],
            "provenance": provenance,
            "area_m2": format(area, "f"),
            "area_ha": format(area / Decimal(10000), "f"),
            "area_mu": format(area * Decimal(3) / Decimal(2000), "f"),
            "valid_from": iso(row["effective_at"]),
            "valid_to": iso(row["valid_to"]),
            "reason": row["reason"],
            "actor_user_id": row["actor_id"],
            "created_at": iso(identity["created_at"]),
            "allowed_actions": [],
            "affected_dependencies": {
                "availability": "not_yet_integrated",
                "count": None,
            },
        }

    async def read(self, verified, parcel_id, *, selected_revision=0, at=""):
        revision(selected_revision, zero=True)
        require(
            isinstance(at, str) and not (selected_revision and at),
            "INVALID_ARGUMENT",
            400,
        )
        instant = None
        if at:
            try:
                require(len(at) <= 40 and at.endswith("Z"), "INVALID_ARGUMENT", 400)
                instant = datetime.fromisoformat(at.replace("Z", "+00:00"))
                require(
                    instant.tzinfo is not None and instant <= datetime.now(UTC),
                    "INVALID_ARGUMENT",
                    400,
                )
            except ValueError:
                require(False, "INVALID_ARGUMENT", 400)
        async with self.transaction(verified) as conn:
            identity = await self._identity(conn, parcel_id)
            await self._farm(conn, identity["farm_id"])
            row = await conn.fetchrow(
                "WITH history AS (SELECT *,"
                "LEAD(effective_at) OVER(ORDER BY revision) AS valid_to "
                "FROM agriculture_parcel_revisions WHERE parcel_id=$1) "
                "SELECT *,ST_AsGeoJSON(geom,15)::jsonb AS geometry_json FROM history "
                "WHERE ($2::timestamptz IS NOT NULL AND effective_at<=$2 "
                "AND (valid_to IS NULL OR $2<valid_to)) "
                "OR ($2::timestamptz IS NULL AND revision=$3)",
                parcel_id,
                instant,
                selected_revision or identity["current_revision"],
            )
            require(row is not None, "RESOURCE_NOT_FOUND", 404)
            return self.dto(identity, row)

    async def history(self, verified, parcel_id, *, after_revision=0, limit=50):
        revision(after_revision, zero=True)
        require(type(limit) is int and 1 <= limit <= 200, "INVALID_ARGUMENT", 400)
        async with self.transaction(verified) as conn:
            identity = await self._identity(conn, parcel_id)
            await self._farm(conn, identity["farm_id"])
            rows = await conn.fetch(
                "WITH history AS (SELECT *,"
                "LEAD(effective_at) OVER(ORDER BY revision) AS valid_to "
                "FROM agriculture_parcel_revisions WHERE parcel_id=$1) "
                "SELECT *,ST_AsGeoJSON(geom,15)::jsonb AS geometry_json FROM history "
                "WHERE revision>$2 ORDER BY revision LIMIT $3",
                parcel_id,
                after_revision,
                limit + 1,
            )
            more = len(rows) > limit
            fields = {
                "id",
                "workspace_id",
                "farm_id",
                "geometry_revision",
                "current_geometry_revision",
                "valid_from",
                "valid_to",
                "created_at",
                "reason",
                "actor_user_id",
                "geometry_hash",
                "area_m2",
                "area_ha",
                "area_mu",
            }
            return {
                "items": [
                    {k: v for k, v in self.dto(identity, row).items() if k in fields}
                    for row in rows[:limit]
                ],
                "has_more": more,
                "next_after_revision": rows[limit - 1]["revision"] if more else 0,
            }

    async def list(self, verified, farm_id, *, parcel_ids=None, after_id="", limit=50):
        require(
            type(limit) is int
            and 1 <= limit <= 200
            and isinstance(after_id, str)
            and (not after_id or PARCEL_ID.fullmatch(after_id)),
            "INVALID_ARGUMENT",
            400,
        )
        if parcel_ids is not None:
            require(
                isinstance(parcel_ids, list)
                and 1 <= len(parcel_ids) <= 200
                and all(
                    isinstance(v, str) and PARCEL_ID.fullmatch(v) for v in parcel_ids
                )
                and parcel_ids == sorted(set(parcel_ids)),
                "INVALID_ARGUMENT",
                400,
            )
        async with self.transaction(verified) as conn:
            await self._farm(conn, farm_id)
            rows = await conn.fetch(
                "SELECT p.*,r.revision,r.area_m2,r.geometry_hash,r.effective_at "
                "FROM agriculture_parcels p JOIN agriculture_parcel_revisions r "
                "USING(tenant_id,workspace_id,parcel_id) "
                "WHERE p.farm_id=$1 AND p.status='active' "
                "AND r.revision=p.current_revision "
                "AND ($2::text[] IS NULL OR p.parcel_id=ANY($2)) AND p.parcel_id>$3 "
                "ORDER BY p.parcel_id LIMIT $4",
                farm_id,
                parcel_ids,
                after_id,
                limit + 1,
            )
            items = [
                {
                    "id": row["parcel_id"],
                    "workspace_id": row["workspace_id"],
                    "farm_id": row["farm_id"],
                    "name": row["name"],
                    "external_code": row["external_code"],
                    "status": row["status"],
                    "data_class": row["data_class"],
                    "geometry_revision": row["revision"],
                    "current_geometry_revision": row["current_revision"],
                    "geometry_hash": row["geometry_hash"],
                    "area_m2": format(row["area_m2"], "f"),
                    "area_ha": format(row["area_m2"] / Decimal(10000), "f"),
                    "area_mu": format(row["area_m2"] * Decimal(3) / Decimal(2000), "f"),
                    "valid_from": iso(row["effective_at"]),
                }
                for row in rows[:limit]
            ]
            return {
                "items": items,
                "has_more": len(rows) > limit,
                "next_after_id": rows[limit - 1]["parcel_id"]
                if len(rows) > limit
                else "",
            }

    async def _existing(
        self,
        conn,
        verified,
        operation,
        request_hash,
        *,
        kind=None,
        farm_id=None,
        parcel_id=None,
    ):
        row = await conn.fetchrow(
            "SELECT * FROM agriculture_parcel_commands WHERE operation_id=$1", operation
        )
        if row is None:
            return None
        require(
            row["actor_id"] == verified.claims["actor"]["id"]
            and row["request_hash"] == request_hash
            and (kind is None or row["operation_kind"] == kind)
            and (farm_id is None or row["farm_id"] == farm_id)
            and (parcel_id is None or row["parcel_id"] == parcel_id),
            "IDEMPOTENCY_CONFLICT",
            409,
        )
        return document(row["receipt_json"])

    async def receipt(self, verified, operation, request_hash):
        async with self.transaction(verified) as conn:
            result = await self._existing(conn, verified, operation, request_hash)
            require(result is not None, "RESOURCE_NOT_FOUND", 404)
            return result

    async def preview(self, verified, kind, body, *, farm_id=None, parcel_id=None):
        require(kind in ("parcel.create", "parcel.revise"), "INVALID_ARGUMENT", 400)
        require("expected_revision" in body, "PRECONDITION_REQUIRED", 428)
        create = kind == "parcel.create"
        expected = revision(body["expected_revision"], zero=create)
        require(not create or expected == 0, "INVALID_ARGUMENT", 400)
        if create:
            require("expected_farm_revision" in body, "PRECONDITION_REQUIRED", 428)
            revision(body["expected_farm_revision"])
        text_field(body.get("reason"), 500)
        geometry = await self.prepare(
            body.get("parcel")
            if create
            else {k: body[k] for k in GEOMETRY_FIELDS if k in body},
            create=create,
        )
        async with self.transaction(verified) as conn:
            current = None if create else await self._identity(conn, parcel_id)
            if current is not None:
                require(
                    current["current_revision"] == expected, "REVISION_CONFLICT", 409
                )
                farm_id = current["farm_id"]
            await self._farm(
                conn,
                farm_id,
                geometry,
                expected=body.get("expected_farm_revision") if create else None,
            )
        return geometry

    async def execute(
        self, verified, kind, body, *, farm_id=None, parcel_id=None, before_write=None
    ):
        operation, request_hash = body.get("operation_id"), body.get("request_hash")
        require(
            isinstance(operation, str)
            and ID.fullmatch(operation)
            and isinstance(request_hash, str)
            and HASH.fullmatch(request_hash),
            "INVALID_ARGUMENT",
            400,
        )
        async with self.transaction(verified) as conn:
            old = await self._existing(
                conn,
                verified,
                operation,
                request_hash,
                kind=kind,
                farm_id=farm_id,
                parcel_id=parcel_id,
            )
            if old is not None:
                return old
        geometry = await self.preview(
            verified, kind, body, farm_id=farm_id, parcel_id=parcel_id
        )
        if before_write is not None:
            await before_write()
        c = verified.claims
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
                    kind=kind,
                    farm_id=farm_id,
                    parcel_id=parcel_id,
                )
                if old is not None:
                    return old
                current = (
                    None
                    if kind == "parcel.create"
                    else await self._identity(conn, parcel_id)
                )
                if current is not None:
                    require(
                        current["current_revision"] == body["expected_revision"],
                        "REVISION_CONFLICT",
                        409,
                    )
                    farm_id = current["farm_id"]
                farm = await self._farm(
                    conn,
                    farm_id,
                    geometry,
                    expected=body.get("expected_farm_revision")
                    if current is None
                    else None,
                    lock=True,
                )
                if current is not None:
                    # All domain mutations lock parent before child. Farm geometry
                    # changes hold the opposite (exclusive) parent lock.
                    current = await self._identity(conn, parcel_id, lock=True)
                    require(
                        current["farm_id"] == farm_id
                        and current["current_revision"] == body["expected_revision"],
                        "REVISION_CONFLICT",
                        409,
                    )
                effective = await conn.fetchval("SELECT clock_timestamp()")
                prior_hash = ""
                if current is None:
                    parcel_id = "parcel_" + uuid4().hex
                    new_revision = 1
                    await conn.execute(
                        "INSERT INTO agriculture_parcels "
                        "(tenant_id,workspace_id,parcel_id,farm_id,external_code,"
                        "name,data_class,status,current_revision,"
                        "created_at,updated_at) "
                        "VALUES($1,$2,$3,$4,$5,$6,$7,'active',1,$8,$8)",
                        c["tenant_id"],
                        c["workspace_id"],
                        parcel_id,
                        farm_id,
                        body["parcel"]["external_code"],
                        body["parcel"]["name"],
                        farm["data_class"],
                        effective,
                    )
                else:
                    new_revision = current["current_revision"] + 1
                    prior = await conn.fetchrow(
                        "SELECT geometry_hash,effective_at "
                        "FROM agriculture_parcel_revisions "
                        "WHERE parcel_id=$1 AND revision=$2",
                        parcel_id,
                        current["current_revision"],
                    )
                    require(
                        prior is not None and effective > prior["effective_at"],
                        "DEPENDENCY_UNAVAILABLE",
                        503,
                    )
                    prior_hash = prior["geometry_hash"]
                    await conn.execute(
                        "UPDATE agriculture_parcels SET "
                        "current_revision=current_revision+1,updated_at=$2 "
                        "WHERE parcel_id=$1",
                        parcel_id,
                        effective,
                    )
                provenance = {
                    k: v
                    for k, v in geometry.items()
                    if k not in ("geometry", "area_m2")
                }
                await conn.execute(
                    "INSERT INTO agriculture_parcel_revisions "
                    "(tenant_id,workspace_id,parcel_id,revision,geom,geometry_hash,"
                    "provenance,effective_at,recorded_at,actor_id,reason,operation_id) "
                    "VALUES($1,$2,$3,$4,ST_Multi(ST_SetSRID("
                    "ST_GeomFromGeoJSON($5::text),4326)),$6,$7::jsonb,$8,$8,$9,$10,$11)",
                    c["tenant_id"],
                    c["workspace_id"],
                    parcel_id,
                    new_revision,
                    canonical(geometry["geometry"]).decode(),
                    geometry["geometry_hash"],
                    canonical(provenance).decode(),
                    effective,
                    c["actor"]["id"],
                    body["reason"],
                    operation,
                )
                receipt = {
                    "schema_version": "1.0",
                    "operation_id": operation,
                    "tenant_id": c["tenant_id"],
                    "workspace_id": c["workspace_id"],
                    "request_hash": request_hash,
                    "farm_id": farm_id,
                    "parcel_id": parcel_id,
                    "geometry_revision": new_revision,
                    "previous_geometry_revision": new_revision - 1,
                    "previous_geometry_hash": prior_hash,
                    "geometry_hash": geometry["geometry_hash"],
                    "event_id": "evt_" + uuid4().hex,
                    "event_type": "agri.parcel.created"
                    if current is None
                    else "agri.parcel.revised",
                    "state": "applied",
                    "producer": "agri-gis",
                    "aggregate_revision": new_revision,
                }
                receipt["payload_hash"] = digest(receipt)
                await conn.execute(
                    "INSERT INTO agriculture_parcel_commands "
                    "(tenant_id,workspace_id,operation_id,actor_id,operation_kind,"
                    "request_hash,parcel_id,farm_id,receipt_json) "
                    "VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb)",
                    c["tenant_id"],
                    c["workspace_id"],
                    operation,
                    c["actor"]["id"],
                    kind,
                    request_hash,
                    parcel_id,
                    farm_id,
                    canonical(receipt).decode(),
                )
                await conn.execute(
                    "INSERT INTO agriculture_parcel_outbox "
                    "(tenant_id,workspace_id,event_id,operation_id,event_type,"
                    "parcel_id,farm_id,aggregate_revision,payload_json,payload_hash) "
                    "VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)",
                    c["tenant_id"],
                    c["workspace_id"],
                    receipt["event_id"],
                    operation,
                    receipt["event_type"],
                    parcel_id,
                    farm_id,
                    new_revision,
                    canonical(receipt).decode(),
                    receipt["payload_hash"],
                )
                return receipt
        except asyncpg.UniqueViolationError:
            require(False, "STATE_CONFLICT", 409)


class ParcelOwnership:
    """Current real parcel facts only, never historical proof substitution."""

    def __init__(self, pool):
        self.pool = pool

    async def resolve(self, tenant_id, workspace_id, resources):
        require(
            all(
                isinstance(x, str) and ID.fullmatch(x)
                for x in (tenant_id, workspace_id)
            ),
            "resource_not_found",
            404,
        )
        require(
            isinstance(resources, list)
            and 1 <= len(resources) <= 200
            and all(
                isinstance(r, dict)
                and set(r) == {"kind", "id"}
                and r["kind"] == "parcel"
                and isinstance(r["id"], str)
                and PARCEL_ID.fullmatch(r["id"])
                for r in resources
            ),
            "resource_not_found",
            404,
        )
        async with self.pool.acquire(timeout=3) as conn:
            async with conn.transaction(readonly=True):
                await conn.execute(
                    "SELECT set_config('app.tenant_ids',$1,true),"
                    "set_config('app.workspace_id',$2,true)",
                    tenant_id,
                    workspace_id,
                )
                await conn.execute("SET LOCAL statement_timeout='2s'")
                rows = await conn.fetch(
                    "SELECT p.parcel_id,p.farm_id,p.current_revision "
                    "FROM agriculture_parcels p "
                    "JOIN agriculture_farms f USING(tenant_id,workspace_id,farm_id) "
                    "JOIN agriculture_parcel_revisions r "
                    "USING(tenant_id,workspace_id,parcel_id) "
                    "WHERE p.parcel_id=ANY($1::text[]) AND p.status='active' "
                    "AND f.status='active' AND r.revision=p.current_revision",
                    [r["id"] for r in resources],
                )
                return [
                    Ownership(
                        "parcel",
                        row["parcel_id"],
                        tenant_id,
                        workspace_id,
                        (row["farm_id"],),
                        row["current_revision"],
                    )
                    for row in rows
                ]
