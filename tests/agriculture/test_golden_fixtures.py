"""Fixture integrity and independent arithmetic oracle, not a product pipeline test."""

import hashlib
import importlib.util
import json
from pathlib import Path

import numpy as np
import pytest
import rasterio
from rasterio.warp import transform


ROOT = Path(__file__).parent / "fixtures" / "golden"
MANIFEST = json.loads((ROOT / "manifest.json").read_text())


def reference_ndvi(dataset):
    """Test-only mask-first arithmetic, with no fallback CRS or no-data value."""
    if dataset.crs is None:
        raise ValueError("CRS_REQUIRED")
    red, nir, quality = dataset.read().astype(np.float64)
    valid = (red != dataset.nodata) & (nir != dataset.nodata) & (quality == 0)
    red = red * dataset.scales[0] + dataset.offsets[0]
    nir = nir * dataset.scales[1] + dataset.offsets[1]
    valid &= np.isfinite(red + nir) & (np.abs(red + nir) > 1e-12)
    values = (nir[valid] - red[valid]) / (nir[valid] + red[valid])
    return {"status": "ok" if values.size else "insufficient_data",
            "value": float(values.mean()) if values.size else None,
            "values": values, "mask": valid}


def reference_change(first, second):
    if first.crs != second.crs or first.transform != second.transform or first.shape != second.shape:
        return {"status": "insufficient_data", "value": None}
    if first.tags().get("PRODUCT_LEVEL") != second.tags().get("PRODUCT_LEVEL"):
        return {"status": "insufficient_data", "value": None}
    one, two = reference_ndvi(first), reference_ndvi(second)
    if not np.array_equal(one["mask"], two["mask"]) or one["value"] is None or two["value"] is None:
        return {"status": "insufficient_data", "value": None}
    return {"status": "ok", "value": two["value"] - one["value"]}


def test_hashes_and_dataset_classes():
    for name, expected in MANIFEST["files"].items():
        data = (ROOT / name).read_bytes()
        assert hashlib.sha256(data).hexdigest() == expected["sha256"]
        assert len(data) == expected["size_bytes"]
    assert MANIFEST["groups"]["synthetic"]["license"] == "MIT"
    for group in ("public_real", "customer_operational"):
        assert MANIFEST["groups"][group] == {"status": "not_provided", "assets": []}


def test_two_tenants_explicit_crs_overlap_and_metric_area():
    one, two = json.loads((ROOT / "parcels.json").read_text())
    assert one["tenant_id"] != two["tenant_id"]
    assert one["parcel_id"] == two["parcel_id"]
    assert one["source_crs"] != two["source_crs"]
    assert one["geometry"] == two["geometry"]
    for record in (one, two):
        assert record["output_axis_order"] == "longitude,latitude"
        ring = record["geometry"]["coordinates"][0]
        assert all(-180 <= x <= 180 and -90 <= y <= 90 for x, y in ring)
        x, y = transform("EPSG:4326", "EPSG:6933", *zip(*ring))
        # Translate first to avoid subtracting large nearly equal products.
        x, y = np.array(x) - x[0], np.array(y) - y[0]
        area = abs(np.dot(x[:-1], y[1:]) - np.dot(y[:-1], x[1:])) / 2
        assert area == pytest.approx(10000, abs=0.01)
        assert area / 10000 == pytest.approx(1, abs=1e-6)
        assert area * 15 / 10000 == pytest.approx(15, abs=1.5e-5)
    assert one["source_coordinates"][1][0] - one["source_coordinates"][0][0] == 100
    assert one["source_coordinates"][2][1] - one["source_coordinates"][1][1] == 100


@pytest.mark.parametrize("index", [0, 1])
def test_actual_tiff_metadata_bands_masks_and_hand_truth(index):
    asset = MANIFEST["assets"][index]
    with rasterio.open(ROOT / asset["path"]) as ds:
        assert ds.crs.to_epsg() == 6933 and ds.res == (10, 10)
        assert ds.count == 3 and ds.shape == (3, 3)
        assert ds.descriptions == ("red", "nir", "pixel_quality")
        assert ds.scales == (0.0001, 0.0001, 1) and ds.offsets == (-0.1, -0.1, 0)
        assert ds.units == ("reflectance", "reflectance", "quality_code")
        assert ds.nodata == -9999 and ds.tags()["SYNTHETIC"] == "true"
        assert ds.tags()["ACQUIRED_AT"] == asset["acquired_at"]
        assert ds.tags(3)["QUALITY_CODES"] == "0=clear,1=cloud,2=shadow"
        expected = MANIFEST["raw_arrays"]
        np.testing.assert_array_equal(ds.read(), [expected["red"], expected["nir_by_date"][index], expected["quality"]])
        result = reference_ndvi(ds)
        np.testing.assert_array_equal(np.argwhere(result["mask"]), MANIFEST["expected"]["valid_row_column"])
        np.testing.assert_allclose(result["values"], MANIFEST["expected"]["ndvi_by_date"][index], atol=1e-12, rtol=0)
        assert result["value"] == pytest.approx(MANIFEST["expected"]["mean_ndvi_by_date"][index], abs=1e-12)
        assert not result["mask"][0, 2]  # nodata before scaling
        assert not result["mask"][1, :].any()  # cloud, shadow, zero denominator


def test_comparable_two_dates():
    with rasterio.open(ROOT / MANIFEST["assets"][0]["path"]) as a, rasterio.open(ROOT / MANIFEST["assets"][1]["path"]) as b:
        result = reference_change(a, b)
        assert result["status"] == "ok"
        assert result["value"] == pytest.approx(-1/30, abs=1e-12)


@pytest.mark.parametrize("variant", ["cloud", "missing_crs", "shifted_grid", "other_product"])
def test_bad_quality_and_metadata_fail_closed(tmp_path, variant):
    with rasterio.open(ROOT / MANIFEST["assets"][0]["path"]) as original:
        profile, arrays = original.profile.copy(), original.read()
        if variant == "cloud":
            arrays[2, :, :] = 1
        if variant == "missing_crs":
            profile["crs"] = None
        if variant == "shifted_grid":
            profile["transform"] = original.transform * rasterio.Affine.translation(1, 0)
        path = tmp_path / "negative.tif"
        with rasterio.open(path, "w", **profile) as out:
            out.write(arrays)
            out.scales, out.offsets = original.scales, original.offsets
            out.update_tags(PRODUCT_LEVEL="other" if variant == "other_product" else original.tags()["PRODUCT_LEVEL"])
        with rasterio.open(path) as bad:
            if variant == "missing_crs":
                with pytest.raises(ValueError, match="CRS_REQUIRED"):
                    reference_ndvi(bad)
            elif variant == "cloud":
                result = reference_ndvi(bad)
                assert result["status"] == "insufficient_data" and result["value"] is None
            else:
                assert reference_change(original, bad) == {"status": "insufficient_data", "value": None}


def test_regeneration_is_byte_deterministic(tmp_path):
    spec = importlib.util.spec_from_file_location("golden_generator", ROOT / "generate.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    module.generate(tmp_path)
    for name in [*MANIFEST["files"], "manifest.json"]:
        assert (tmp_path / name).read_bytes() == (ROOT / name).read_bytes()
