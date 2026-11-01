"""Bounded WGS84 geometry and PostGIS spheroid area; no implicit reprojection."""

import hashlib
import json
import math
from decimal import Decimal

import asyncpg
import rfc8785

from .identity.verify import IdentityError

MAX_VERTICES = 10000
# Explicit first-version algorithm support, not an administrative-area default.
# Larger/polar/antimeridian inputs need a separately verified geometry profile.
MAX_AREA_M2 = Decimal("10000000000")
MAX_ABS_LATITUDE = 85
SUPPORTED_CRS = frozenset({"EPSG:4326", "OGC:CRS84"})
AREA_ALGORITHM = "postgis-geography-spheroid/1"
MEASURE_SQL = """
WITH candidate AS (
 SELECT ST_SetSRID(ST_GeomFromGeoJSON($1::text),4326) AS geom
), checked AS (
 SELECT geom,ST_IsValid(geom,0) AND NOT ST_IsEmpty(geom) AS valid FROM candidate
)
SELECT valid,
 CASE WHEN valid THEN round(ST_Area(geom::geography,true)::numeric,4) END AS area_m2,
 postgis_lib_version() AS postgis_version
FROM checked
"""


def invalid(code="farm_geometry_invalid"):
    return IdentityError(code, 422)


def validate_geometry(geometry, source_crs, coordinate_order):
    """Return canonical 2D GeoJSON bytes after bounded structural validation.

    Topology is deliberately NOT asserted here: measure_geometry must execute
    the actual PostGIS validation. EPSG identifiers are not an axis-order guess;
    the API separately requires longitude_latitude even for EPSG:4326.
    """
    if source_crs is None or source_crs == "":
        raise invalid("farm_crs_required")
    if not isinstance(source_crs, str) or source_crs not in SUPPORTED_CRS:
        raise invalid("farm_crs_unsupported")
    if coordinate_order != "longitude_latitude":
        raise invalid("farm_coordinate_order_invalid")
    if not isinstance(geometry, dict) or set(geometry) != {"type", "coordinates"}:
        raise invalid()
    kind, coordinates = geometry["type"], geometry["coordinates"]
    if (
        not isinstance(kind, str)
        or kind not in {"Polygon", "MultiPolygon"}
        or not isinstance(coordinates, list)
    ):
        raise invalid()
    polygons = [coordinates] if kind == "Polygon" else coordinates
    if not polygons or len(polygons) > MAX_VERTICES // 4:
        raise invalid()
    count = 0
    min_lon, max_lon = 180, -180
    for polygon in polygons:
        if not isinstance(polygon, list) or not polygon:
            raise invalid()
        for ring in polygon:
            if not isinstance(ring, list) or len(ring) < 4:
                raise invalid()
            count += len(ring)
            if count > MAX_VERTICES:
                raise invalid("farm_geometry_limit")
            previous = None
            for point in ring:
                if not isinstance(point, list) or len(point) != 2:
                    raise invalid()
                if any(
                    type(value) not in {int, float}
                    or (isinstance(value, float) and not math.isfinite(value))
                    for value in point
                ):
                    raise invalid()
                lon, lat = point
                if not -180 <= lon <= 180 or not -90 <= lat <= 90:
                    raise invalid()
                if abs(lat) > MAX_ABS_LATITUDE:
                    raise invalid("geometry_profile_unsupported")
                if previous is not None and abs(lon - previous) > 180:
                    raise invalid("geometry_profile_unsupported")
                previous = lon
                min_lon, max_lon = min(min_lon, lon), max(max_lon, lon)
            if ring[0] != ring[-1] or len({tuple(point) for point in ring}) < 3:
                raise invalid()
    if max_lon - min_lon > 180:
        raise invalid("geometry_profile_unsupported")
    # Structure/number bounds above make direct JCS safe; the general identity
    # canonicalizer's 10k-node bound is intentionally not a 10k-vertex bound.
    try:
        return rfc8785.dumps(geometry)
    except (ValueError, TypeError):
        raise invalid() from None


async def measure_geometry(connection, geometry, source_crs, coordinate_order):
    raw = validate_geometry(geometry, source_crs, coordinate_order)
    try:
        result = await connection.fetchrow(MEASURE_SQL, raw.decode(), timeout=5)
    except asyncpg.DataError:
        raise invalid() from None
    except Exception:
        raise IdentityError("farm_geometry_dependency_unavailable", 503) from None
    if result is None or result["valid"] is not True:
        raise invalid()
    area = result["area_m2"]
    if not isinstance(area, Decimal) or not area.is_finite() or area <= 0:
        raise invalid()
    if area > MAX_AREA_M2:
        raise invalid("geometry_profile_unsupported")
    return {
        "geometry": json.loads(raw),
        "geometry_hash": hashlib.sha256(raw).hexdigest(),
        "source_crs": source_crs,
        "coordinate_order": coordinate_order,
        "transform": {
            "operation": "identity",
            "version": "farm-wgs84-identity/1",
            "target_crs": "OGC:CRS84",
            "source_axis_order": coordinate_order,
            "source_unit": "degree",
        },
        "area_m2": area,
        "area_algorithm": AREA_ALGORITHM,
        "algorithm_version": result["postgis_version"],
    }
