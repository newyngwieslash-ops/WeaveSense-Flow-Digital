"""Synthetic geometry goldens; PostgreSQL cases never use a mocked calculator."""

import copy
import json
from decimal import Decimal

import pytest
from test_agriculture_actors_postgres import pg_config

from app.agriculture.database import create_runtime_pool
from app.agriculture.farms_geometry import measure_geometry, validate_geometry
from app.agriculture.identity.verify import IdentityError


def rectangle(lon=110, lat=20, width=0.01):
    return {
        "type": "Polygon",
        "coordinates": [
            [
                [lon, lat],
                [lon + width, lat],
                [lon + width, lat + width],
                [lon, lat + width],
                [lon, lat],
            ]
        ],
    }


def validate(value, source="EPSG:4326", order="longitude_latitude"):
    return validate_geometry(value, source, order)


def test_farm_geometry_structure_preserves_holes_multipolygons_and_source():
    value = rectangle()
    value["coordinates"].append(rectangle(110.002, 20.002, 0.006)["coordinates"][0])
    assert json.loads(validate(value)) == value
    multi = {
        "type": "MultiPolygon",
        "coordinates": [value["coordinates"], rectangle(111, 21)["coordinates"]],
    }
    assert json.loads(validate(multi, "OGC:CRS84")) == multi
    assert validate(rectangle()) == validate(copy.deepcopy(rectangle()))


@pytest.mark.parametrize(
    "mutation",
    [
        lambda x: x.update(crs={"name": "EPSG:3857"}),
        lambda x: x.update(type="Point"),
        lambda x: x.update(type=[]),
        lambda x: x.update(coordinates=[]),
        lambda x: x["coordinates"][0].pop(),
        lambda x: x["coordinates"][0].__setitem__(1, [float("nan"), 20]),
        lambda x: x["coordinates"][0].__setitem__(1, [float("inf"), 20]),
        lambda x: x["coordinates"][0].__setitem__(1, [True, 20]),
        lambda x: x["coordinates"][0].__setitem__(1, [10**1000, 20]),
        lambda x: x["coordinates"][0].__setitem__(1, [110, 91]),
        lambda x: x["coordinates"][0].__setitem__(1, [181, 20]),
        lambda x: x["coordinates"][0].__setitem__(1, [110, 20, 1]),
        lambda x: x.update(coordinates=[[[110, 20]] * 5]),
        lambda x: x.update(coordinates=[x["coordinates"][0] * 2001]),
    ],
)
def test_farm_geometry_rejects_malformed_unbounded_input(mutation):
    value = rectangle()
    mutation(value)
    with pytest.raises(IdentityError) as error:
        validate(value)
    assert error.value.status == 422


@pytest.mark.parametrize(
    "source,order",
    [
        (None, "longitude_latitude"),
        ("", "longitude_latitude"),
        ("EPSG:3857", "longitude_latitude"),
        ([], "longitude_latitude"),
        ("EPSG:4326", "latitude_longitude"),
    ],
)
def test_farm_geometry_never_guesses_crs_or_axis(source, order):
    with pytest.raises(IdentityError) as error:
        validate(rectangle(), source, order)
    assert error.value.status == 422


@pytest.mark.parametrize(
    "value",
    [
        rectangle(0, 86),
        {
            "type": "Polygon",
            "coordinates": [[[179, 20], [-179, 20], [-179, 21], [179, 21], [179, 20]]],
        },
    ],
)
def test_farm_geometry_explicit_polar_antimeridian_profile_boundary(value):
    with pytest.raises(IdentityError) as error:
        validate(value)
    assert (error.value.code, error.value.status) == (
        "geometry_profile_unsupported",
        422,
    )


@pytest.mark.asyncio
async def test_farm_geometry_real_postgis_golden_spheroid_holes_multi():
    _, runtime = pg_config()
    pool = await create_runtime_pool(runtime)
    try:
        async with pool.acquire() as connection:
            north = await measure_geometry(
                connection, rectangle(126, 45), "EPSG:4326", "longitude_latitude"
            )
            south = await measure_geometry(
                connection, rectangle(), "OGC:CRS84", "longitude_latitude"
            )
            assert abs(north["area_m2"] - Decimal("876163.4480")) <= Decimal("0.0010")
            assert abs(south["area_m2"] - Decimal("1158452.1897")) <= Decimal("0.0010")
            assert north["area_m2"] != south["area_m2"]
            assert north["transform"]["operation"] == "identity"
            assert north["source_crs"] == "EPSG:4326"
            assert len(north["geometry_hash"]) == 64
            hole = rectangle()
            hole["coordinates"].append(
                rectangle(110.002, 20.002, 0.006)["coordinates"][0]
            )
            measured = await measure_geometry(
                connection, hole, "EPSG:4326", "longitude_latitude"
            )
            assert abs(measured["area_m2"] - Decimal("741409.4015")) <= Decimal(
                "0.0010"
            )
            multi = {
                "type": "MultiPolygon",
                "coordinates": [
                    rectangle()["coordinates"],
                    rectangle(111, 21)["coordinates"],
                ],
            }
            measured = await measure_geometry(
                connection, multi, "EPSG:4326", "longitude_latitude"
            )
            assert measured["area_m2"] > south["area_m2"]
    finally:
        await pool.close()


@pytest.mark.asyncio
async def test_farm_geometry_real_postgis_topology_and_area_denials():
    _, runtime = pg_config()
    pool = await create_runtime_pool(runtime)
    bowtie = {
        "type": "Polygon",
        "coordinates": [
            [[110, 20], [110.01, 20.01], [110, 20.01], [110.01, 20], [110, 20]]
        ],
    }
    collinear = {
        "type": "Polygon",
        "coordinates": [[[110, 20], [110.01, 20], [110.02, 20], [110, 20]]],
    }
    outside_hole = rectangle()
    outside_hole["coordinates"].append(rectangle(111, 21)["coordinates"][0])
    overlap = {
        "type": "MultiPolygon",
        "coordinates": [
            rectangle()["coordinates"],
            rectangle(110.005, 20.005)["coordinates"],
        ],
    }
    try:
        async with pool.acquire() as connection:
            for geometry in (
                bowtie,
                collinear,
                outside_hole,
                overlap,
                rectangle(110, 20, 2),
            ):
                with pytest.raises(IdentityError) as error:
                    await measure_geometry(
                        connection, geometry, "EPSG:4326", "longitude_latitude"
                    )
                assert error.value.status == 422
    finally:
        await pool.close()
