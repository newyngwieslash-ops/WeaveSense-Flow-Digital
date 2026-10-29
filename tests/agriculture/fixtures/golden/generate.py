"""Deterministic, self-authored synthetic DT-006 recovery fixtures; never live data."""

import hashlib
import json
from pathlib import Path

import numpy as np
import rasterio
from rasterio.transform import from_origin
from rasterio.warp import transform


ROOT = Path(__file__).resolve().parent
RED = [[2000, 3000, -9999], [2000, 2000, 1000], [3000, 4000, 2000]]
NIR = [
    [[6000, 5000, 5000], [6000, 6000, 1000], [7000, 4000, 8000]],
    [[4000, 7000, 5000], [6000, 6000, 1000], [5000, 6000, 4000]],
]
QA = [[0, 0, 0], [1, 2, 0], [0, 0, 0]]


def write_json(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def generate(destination=ROOT):
    destination = Path(destination)
    destination.mkdir(parents=True, exist_ok=True)
    ring = [[1000000, 3000000], [1000100, 3000000], [1000100, 3000100],
            [1000000, 3000100], [1000000, 3000000]]
    lon, lat = transform("EPSG:6933", "EPSG:4326", *zip(*ring))
    wgs84 = [[x, y] for x, y in zip(lon, lat)]
    records = []
    for tenant, crs, coordinates in [
        ("fixture-tenant-a", "EPSG:6933", ring),
        ("fixture-tenant-b", "EPSG:4326", wgs84),
    ]:
        records.append({
            "tenant_id": tenant, "parcel_id": "fixture-parcel-001", "revision": 1,
            "synthetic": True, "source_crs": crs,
            "source_axis_order": "x,y" if crs == "EPSG:6933" else "longitude,latitude",
            "source_coordinates": coordinates,
            "geometry": {"type": "Polygon", "coordinates": [wgs84]},
            "output_crs": "EPSG:4326", "output_axis_order": "longitude,latitude",
            "transform": {"engine": "GDAL/PROJ via rasterio.warp.transform",
                          "source": crs, "target": "EPSG:4326", "always_xy": True},
            "area_reference": {"algorithm": "fixture-equal-area-shoelace-v1",
                               "crs": "EPSG:6933", "area_m2": 10000,
                               "area_hectares": 1, "area_mu": 15, "tolerance_m2": 0.01},
        })
    write_json(destination / "parcels.json", records)
    assets = []
    for index, nir in enumerate(NIR):
        name = f"reflectance-202606{1 + index * 10:02d}.tif"
        with rasterio.open(destination / name, "w", driver="GTiff", width=3, height=3,
                           count=3, dtype="int16", crs="EPSG:6933",
                           transform=from_origin(1000000, 3000100, 10, 10), nodata=-9999,
                           compress="NONE", interleave="band") as dataset:
            dataset.write(np.array([RED, nir, QA], dtype=np.int16))
            dataset.descriptions = ("red", "nir", "pixel_quality")
            dataset.scales = (0.0001, 0.0001, 1)
            dataset.offsets = (-0.1, -0.1, 0)
            dataset.units = ("reflectance", "reflectance", "quality_code")
            dataset.update_tags(SYNTHETIC="true", SOURCE="self-authored DT-006 recovery v1",
                                LICENSE="MIT", PRODUCT_LEVEL="synthetic_surface_reflectance",
                                ACQUIRED_AT=f"2026-06-{1 + index * 10:02d}T00:00:00Z")
            dataset.update_tags(3, QUALITY_CODES="0=clear,1=cloud,2=shadow",
                                QUALITY_SOURCE="self-authored synthetic mask")
        assets.append({"path": name, "acquired_at": f"2026-06-{1 + index * 10:02d}T00:00:00Z",
                       "product_family": "synthetic-golden-v1", "product_level": "synthetic_surface_reflectance",
                       "source_crs": "EPSG:6933", "resolution_m": [10, 10],
                       "bands": [{"index": 1, "semantic": "red", "scale": 0.0001, "offset": -0.1, "nodata": -9999},
                                 {"index": 2, "semantic": "nir", "scale": 0.0001, "offset": -0.1, "nodata": -9999},
                                 {"index": 3, "semantic": "quality", "codes": {"0": "clear", "1": "cloud", "2": "shadow"}}]})
    files = {}
    for path in [destination / "parcels.json", *(destination / item["path"] for item in assets)]:
        files[path.name] = {"sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "size_bytes": path.stat().st_size}
    write_json(destination / "manifest.json", {
        "schema_version": "1.0", "dataset_id": "dt006-synthetic-recovery-v1",
        "recovery": "newly reconstructed fixtures; not asserted byte-identical to lost W00 artifacts",
        "groups": {"synthetic": {"status": "provided", "source": "self-authored numerical fixtures",
                                  "license": "MIT", "rights": "test-only synthetic, no third-party or customer inputs"},
                   "public_real": {"status": "not_provided", "assets": []},
                   "customer_operational": {"status": "not_provided", "assets": []}},
        "files": files, "assets": assets,
        "raw_arrays": {"red": RED, "nir_by_date": NIR, "quality": QA},
        "expected": {"valid_row_column": [[0, 0], [0, 1], [2, 0], [2, 1], [2, 2]],
                     "ndvi_by_date": [[2/3, 1/3, 1/2, 0, 3/4], [1/2, 1/2, 1/3, 1/4, 1/2]],
                     "mean_ndvi_by_date": [9/20, 5/12], "mean_change": -1/30,
                     "valid_pixel_count": 5, "tolerance": 1e-12,
                     "no_valid_pixels": {"status": "insufficient_data", "value": None},
                     "incomparable_dates": {"status": "insufficient_data", "value": None}},
        "generator_environment": {"rasterio": rasterio.__version__, "gdal": rasterio.__gdal_version__},
        "limitations": ["Not a production processing implementation or permission-isolation test",
                        "Not real imagery, a real farm, agronomic diagnosis or commercial acceptance",
                        "Deterministic bytes are checked within the recorded locked GDAL/Rasterio environment"]})


if __name__ == "__main__":
    generate()
