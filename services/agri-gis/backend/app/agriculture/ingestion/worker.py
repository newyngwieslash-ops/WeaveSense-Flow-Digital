"""Fixed subprocess entrypoint; only bounded summaries leave the process."""

import json
import os
import resource
import sys
from pathlib import Path

# -I excludes caller PYTHONPATH and cwd; add only this reviewed source root.
sys.path.insert(0, str(Path(__file__).resolve().parents[3]))
from app.agriculture.ingestion.archive import check_archive  # noqa: E402
from app.agriculture.ingestion.limits import IngestionDenied, ParseLimits  # noqa: E402


def constrain(limits):
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_AS, (limits.memory_bytes, limits.memory_bytes))
    resource.setrlimit(resource.RLIMIT_CPU, (limits.cpu_seconds, limits.cpu_seconds))
    resource.setrlimit(
        resource.RLIMIT_FSIZE, (limits.expanded_bytes, limits.expanded_bytes)
    )
    resource.setrlimit(resource.RLIMIT_NOFILE, (64, 64))


def vector_summary(path, limits):
    import fiona

    driver = {".geojson": "GeoJSON", ".json": "GeoJSON", ".gpkg": "GPKG"}.get(
        path.suffix
    )
    if driver is None:
        raise IngestionDenied("vector_driver_denied")
    features = coordinates = 0

    def count(value, depth=0):
        nonlocal coordinates
        if depth > 12:
            raise IngestionDenied("geometry_depth_limit")
        if isinstance(value, list | tuple):
            if value and isinstance(value[0], int | float):
                coordinates += 1
            else:
                for child in value:
                    count(child, depth + 1)
        if coordinates > limits.coordinates:
            raise IngestionDenied("geometry_coordinates_limit")

    with fiona.Env(GDAL_DISABLE_READDIR_ON_OPEN="EMPTY_DIR", PROJ_NETWORK="OFF"):
        with fiona.open(path, enabled_drivers=[driver]) as source:
            if not source.crs:
                raise IngestionDenied("crs_required")
            for feature in source:
                features += 1
                if features > limits.features:
                    raise IngestionDenied("vector_features_limit")
                geometry = feature.geometry
                if geometry is not None:
                    if geometry.type == "GeometryCollection":
                        raise IngestionDenied("geometry_collection_not_supported")
                    count(geometry.coordinates)
            return {
                "feature_count": features,
                "coordinate_count": coordinates,
                "crs": str(source.crs),
                "driver": source.driver,
            }


def inspect(path, operation, asset_type, filename, limits):
    from app.core.dataset_files import inspect_dataset_file

    suffix = Path(filename).suffix.lower()
    if suffix not in {
        ".tif",
        ".tiff",
        ".geojson",
        ".json",
        ".gpkg",
        ".csv",
        ".zip",
        ".xlsx",
        ".docx",
        ".pdf",
    }:
        raise IngestionDenied("parser_format_not_enabled")
    metadata = {}
    if suffix in {".zip", ".xlsx", ".docx"}:
        metadata.update(check_archive(path, limits))
    if suffix in {".tif", ".tiff"}:
        import rasterio

        with rasterio.Env(
            GDAL_DISABLE_READDIR_ON_OPEN="EMPTY_DIR",
            GDAL_CACHEMAX=16 * 1024 * 1024,
            PROJ_NETWORK="OFF",
        ):
            with rasterio.open(path, driver="GTiff") as source:
                if not source.crs:
                    raise IngestionDenied("crs_required")
                if source.width * source.height > limits.pixels:
                    raise IngestionDenied("raster_pixels_limit")
                if not 1 <= source.count <= limits.bands:
                    raise IngestionDenied("raster_bands_limit")
                if source.subdatasets or source.driver != "GTiff":
                    raise IngestionDenied("raster_reference_denied")
                # Actual decoding, bounded windows; malformed strips must fail.
                from rasterio.windows import Window

                for y in range(0, source.height, 256):
                    for x in range(0, source.width, 256):
                        source.read(
                            window=Window(
                                x,
                                y,
                                min(256, source.width - x),
                                min(256, source.height - y),
                            )
                        )
    if asset_type == "vector":
        metadata.update(vector_summary(path, limits))
    if operation == "field_workbook":
        if suffix != ".xlsx":
            raise IngestionDenied("invalid_workbook_format")
        from app.services.field_workbook_parser import FieldVerificationWorkbookParser

        rows = FieldVerificationWorkbookParser().parse(filename, path.read_bytes())
        metadata["validated_record_count"] = len(rows)
    elif suffix == ".docx":
        from docx import Document

        document = Document(path)
        if len(document.paragraphs) > limits.features:
            raise IngestionDenied("document_items_limit")
        metadata["paragraph_count"] = len(document.paragraphs)
    elif suffix == ".xlsx":
        from openpyxl import load_workbook

        workbook = load_workbook(
            path, read_only=True, data_only=False, keep_links=False
        )
        cells = rows = 0
        try:
            for sheet in workbook:
                for row in sheet.iter_rows():
                    rows += 1
                    if rows > limits.features:
                        raise IngestionDenied("document_items_limit")
                    for cell in row:
                        cells += 1
                        if cells > limits.coordinates:
                            raise IngestionDenied("document_items_limit")
                        if cell.data_type == "f":
                            raise IngestionDenied("active_document_content_denied")
            metadata["row_count"], metadata["cell_count"] = rows, cells
        finally:
            workbook.close()
    elif suffix == ".csv":
        import csv

        rows = cells = 0
        csv.field_size_limit(min(limits.input_bytes, 65536))
        with path.open(encoding="utf-8-sig", newline="") as stream:
            for row in csv.reader(stream, strict=True):
                rows += 1
                cells += len(row)
                if rows > limits.features or cells > limits.coordinates:
                    raise IngestionDenied("document_items_limit")
        metadata["row_count"], metadata["cell_count"] = rows, cells
    elif suffix == ".pdf":
        from pypdf import PdfReader

        reader = PdfReader(path, strict=True)
        if reader.is_encrypted or len(reader.pages) > limits.features:
            raise IngestionDenied("document_items_limit")
        catalog = reader.trailer["/Root"]
        if any(key in catalog for key in ("/OpenAction", "/AA", "/Names")):
            raise IngestionDenied("active_document_content_denied")
        metadata["page_count"] = len(reader.pages)
    result = inspect_dataset_file(
        path, asset_type, filename, None, limits.expanded_bytes
    )
    return {
        "media_type": result.media_type,
        "suffix": result.suffix,
        "metadata": result.metadata | metadata,
    }


def main():
    if os.geteuid() == 0:
        raise IngestionDenied("parser_requires_nonroot")
    request = json.loads(Path(sys.argv[1]).read_bytes())
    if set(request) != {"operation", "asset_type", "filename", "limits"}:
        raise IngestionDenied("invalid_parser_request")
    limits = ParseLimits(**request["limits"])
    constrain(limits)
    if request["operation"] not in {"inspect", "field_workbook"}:
        raise IngestionDenied("invalid_parser_operation")
    filename = request["filename"]
    if Path(filename).name != filename or "/" in filename or "\\" in filename:
        raise IngestionDenied("invalid_parser_filename")
    path = Path(sys.argv[1]).parent / ("input" + Path(filename).suffix.lower())
    if path.is_symlink() or not 0 < path.stat().st_size <= limits.input_bytes:
        raise IngestionDenied("parser_input_limit")
    print(
        json.dumps(
            {
                "ok": True,
                "result": inspect(
                    path, request["operation"], request["asset_type"], filename, limits
                ),
            },
            ensure_ascii=True,
        )
    )


if __name__ == "__main__":
    try:
        main()
    except IngestionDenied as error:
        print(json.dumps({"ok": False, "code": str(error)}))
        raise SystemExit(2) from None
    except BaseException:
        print(json.dumps({"ok": False, "code": "parser_rejected_input"}))
        raise SystemExit(2) from None
