"""Actual bounded parser children. Local adapter is NOT a kernel sandbox proof."""

import io
import json
import sys
from pathlib import Path
from zipfile import ZIP_DEFLATED, ZipFile, ZipInfo

import numpy as np
import pytest
from rasterio.io import MemoryFile
from rasterio.transform import from_origin

from app.agriculture.ingestion.limits import IngestionDenied, ParseLimits
from app.agriculture.ingestion.runner import (
    DockerSandbox,
    SafeParser,
    configured_parser,
)

WORKER = Path(__file__).parents[1] / "app/agriculture/ingestion/worker.py"


class LocalResourceFixture:
    isolation = "fixture-resource-limits-only-no-kernel-isolation"

    def command(self, directory, limits, operation_id):
        return [sys.executable, "-I", str(WORKER), str(directory / "request.json")]

    def cleanup(self, operation_id):
        pass


def parser(tmp_path, **limits):
    return SafeParser(tmp_path / "owned", LocalResourceFixture(), ParseLimits(**limits))


def raster(*, crs="EPSG:4326", size=4):
    with MemoryFile() as memory:
        with memory.open(
            driver="GTiff",
            height=size,
            width=size,
            count=1,
            dtype="uint8",
            crs=crs,
            transform=from_origin(120, 40, 0.01, 0.01),
        ) as ds:
            ds.write(np.ones((1, size, size), dtype="uint8"))
        return memory.read()


def inspect(p, data, filename="input.tif", asset_type="imagery", **kwargs):
    return p.inspect_stream(
        io.BytesIO(data), filename=filename, asset_type=asset_type, **kwargs
    )


def test_real_rasterio_decode_in_separate_limited_process(tmp_path):
    result = inspect(parser(tmp_path), raster())
    assert result["inspection"]["metadata"]["width"] == 4
    assert result["inspection"]["metadata"]["crs"] == "EPSG:4326"
    assert result["isolation"] == LocalResourceFixture.isolation
    assert not list((tmp_path / "owned").iterdir())


@pytest.mark.parametrize("kind", ["missing_crs", "pixel_limit", "corrupt", "vrt"])
def test_raster_rejection_and_cleanup(tmp_path, kind):
    p = parser(tmp_path, pixels=4) if kind == "pixel_limit" else parser(tmp_path)
    data = raster(crs=None) if kind == "missing_crs" else raster()
    filename = "input.tif"
    if kind == "corrupt":
        data = data[:20]
    if kind == "vrt":
        data = b"<VRTDataset><SourceFilename>/etc/passwd</SourceFilename></VRTDataset>"
        filename = "input.vrt"
    with pytest.raises(IngestionDenied):
        inspect(p, data, filename)
    assert not list((tmp_path / "owned").iterdir())


def zipped(entries, *, compress=ZIP_DEFLATED):
    out = io.BytesIO()
    with ZipFile(out, "w", compression=compress) as archive:
        for name, data in entries:
            archive.writestr(name, data)
    return out.getvalue()


@pytest.mark.parametrize(
    "kind",
    ["traversal", "duplicate", "symlink", "ratio", "nested", "external", "expanded"],
)
def test_actual_archive_abuse_is_bounded_and_cleaned(tmp_path, kind):
    entries = [("data.txt", b"abc")]
    limits = {}
    if kind == "traversal":
        entries = [("../escape", b"bad")]
    elif kind == "duplicate":
        entries *= 2
    elif kind == "symlink":
        info = ZipInfo("link")
        info.create_system = 3
        info.external_attr = 0o120777 << 16
        entries = [(info, b"/etc/passwd")]
    elif kind == "ratio":
        entries = [("bomb.txt", b"0" * 200000)]
    elif kind == "nested":
        entries = [("nested.zip", zipped(entries))]
    elif kind == "external":
        entries = [
            (
                "test.rels",
                b'<Relationships><Relationship TargetMode = "External" Target="file:///etc/passwd"/></Relationships>',
            )
        ]
    elif kind == "expanded":
        limits = {"expanded_bytes": 2}
    with pytest.raises(IngestionDenied):
        inspect(parser(tmp_path, **limits), zipped(entries), "input.zip", "management")
    assert not list((tmp_path / "owned").iterdir())


def test_actual_fiona_geojson_and_coordinate_limit(tmp_path):
    value = {
        "type": "FeatureCollection",
        "features": [
            {
                "type": "Feature",
                "properties": {},
                "geometry": {
                    "type": "LineString",
                    "coordinates": [[120, 40], [121, 41]],
                },
            }
        ],
    }
    raw = json.dumps(value).encode()
    result = inspect(parser(tmp_path), raw, "input.geojson", "vector")
    assert result["inspection"]["metadata"]["feature_count"] == 1
    with pytest.raises(IngestionDenied, match="geometry_coordinates_limit"):
        inspect(parser(tmp_path, coordinates=1), raw, "input.geojson", "vector")


def test_existing_field_workbook_parser_runs_in_child(tmp_path):
    from app.services.field_workbook_parser import FieldVerificationWorkbookParser

    raw = FieldVerificationWorkbookParser.build_template()
    result = inspect(
        parser(tmp_path), raw, "input.xlsx", "table", operation="field_workbook"
    )
    assert result["inspection"]["metadata"]["validated_record_count"] == 1


def test_real_docx_and_pdf_readers(tmp_path):
    from docx import Document
    from pypdf import PdfWriter

    out = io.BytesIO()
    doc = Document()
    doc.add_paragraph("Self-authored synthetic fixture.")
    doc.save(out)
    assert (
        inspect(parser(tmp_path), out.getvalue(), "input.docx", "management")[
            "inspection"
        ]["metadata"]["paragraph_count"]
        == 1
    )
    out = io.BytesIO()
    pdf = PdfWriter()
    pdf.add_blank_page(width=100, height=100)
    pdf.write(out)
    assert (
        inspect(parser(tmp_path), out.getvalue(), "input.pdf", "management")[
            "inspection"
        ]["metadata"]["page_count"]
        == 1
    )


def test_production_has_no_ordinary_process_fallback(tmp_path, monkeypatch):
    monkeypatch.delenv("AGRI_PARSER_IMAGE", raising=False)
    with pytest.raises(IngestionDenied, match="parser_sandbox_unavailable"):
        configured_parser(tmp_path)
    with pytest.raises(IngestionDenied):
        DockerSandbox("untrusted:latest")


def test_actual_csv_xlsx_and_formula_denial(tmp_path):
    from openpyxl import Workbook

    result = inspect(parser(tmp_path), b"key,value\na,1\n", "input.csv", "table")
    assert result["inspection"]["metadata"]["row_count"] == 2
    workbook = Workbook()
    workbook.active.append(["synthetic", 1])
    out = io.BytesIO()
    workbook.save(out)
    result = inspect(parser(tmp_path), out.getvalue(), "input.xlsx", "table")
    assert result["inspection"]["metadata"]["cell_count"] == 2
    workbook.active["B1"] = "=1+1"
    out = io.BytesIO()
    workbook.save(out)
    with pytest.raises(IngestionDenied, match="active_document_content_denied"):
        inspect(parser(tmp_path), out.getvalue(), "input.xlsx", "table")
    assert not list((tmp_path / "owned").iterdir())


def test_arbitrary_stream_not_read_before_resource_supervision(tmp_path):
    class UntrustedReader:
        def read(self, size):
            raise AssertionError("must not read an unbounded network reader")

    with pytest.raises(IngestionDenied, match="parser_requires_completed_local_input"):
        parser(tmp_path).inspect_stream(
            UntrustedReader(), asset_type="table", filename="x.csv"
        )


def test_parent_secret_environment_not_forwarded(tmp_path, monkeypatch):
    monkeypatch.setenv("DATABASE_URL", "sentinel-do-not-forward")
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "sentinel-do-not-forward")

    class EnvironmentFixture(LocalResourceFixture):
        def command(self, directory, limits, operation_id):
            return [
                sys.executable,
                "-I",
                "-c",
                "import os,json; assert not any(k in os.environ for k in "
                "['DATABASE_URL','AWS_SECRET_ACCESS_KEY','HTTP_PROXY']); "
                "print(json.dumps({'ok':True,'result':{'environment_clean':True}}))",
            ]

    result = inspect(
        SafeParser(tmp_path / "owned", EnvironmentFixture()),
        b"fixture",
        "input.csv",
        "table",
    )
    assert result["inspection"]["environment_clean"]
