"""Mandatory CI container evidence. Explicit opt-in only; never fake a pass."""

import io
import json
import os
import socket

import pytest
from test_agriculture_ingestion_parser import raster, zipped

from app.agriculture.ingestion.limits import IngestionDenied, ParseLimits
from app.agriculture.ingestion.runner import DockerSandbox, SafeParser


@pytest.fixture
def sandbox():
    image = os.environ.get("AGRI_PARSER_DOCKER_TEST_IMAGE")
    if not image:
        pytest.skip("actual Docker evidence requires explicit immutable test image")
    # If explicitly requested but Docker/image fails, that is failure, not skip.
    return DockerSandbox(image)


def test_docker_actual_raster_and_malformed_inputs(sandbox, tmp_path, record_property):
    parser = SafeParser(tmp_path / "owned", sandbox)
    record_property("parser_stage", "valid_raster")
    try:
        result = parser.inspect_stream(
            io.BytesIO(raster()), asset_type="imagery", filename="a.tif"
        )
    except Exception:
        # A second, test-only probe supplies structural diagnostics, never a
        # replacement result. The original production-path exception is raised.
        try:
            diagnostic = SafeParser(
                tmp_path / "diagnostic", DockerProbe(sandbox, RASTER_DIAGNOSTIC)
            ).inspect_stream(io.BytesIO(raster()), asset_type="imagery", filename="a.tif")
            record_property("parser_diagnostic", json.dumps(diagnostic["inspection"]))
        except Exception:
            record_property("parser_diagnostic", '{"exception":"probe_unavailable"}')
        raise
    assert result["inspection"]["metadata"]["width"] == 4
    assert result["isolation"] == "container-network-none-readonly"
    for stage, data, name, kind in [
        ("corrupt_raster", raster()[:20], "a.tif", "imagery"),
        ("missing_crs", raster(crs=None), "a.tif", "imagery"),
        ("archive_traversal", zipped([("../escape", b"x")]), "a.zip", "table"),
        ("archive_bomb", zipped([("bomb", b"0" * 200000)]), "a.zip", "table"),
    ]:
        record_property("parser_stage", stage)
        with pytest.raises(IngestionDenied):
            parser.inspect_stream(io.BytesIO(data), asset_type=kind, filename=name)
        assert not list((tmp_path / "owned").iterdir())
    record_property("parser_stage", "pixel_limit")
    with pytest.raises(IngestionDenied, match="raster_pixels_limit"):
        SafeParser(tmp_path / "owned", sandbox, ParseLimits(pixels=2)).inspect_stream(
            io.BytesIO(raster()), asset_type="imagery", filename="a.tif"
        )


RASTER_DIAGNOSTIC = """
import contextlib,io,json,sys,traceback
sys.path.insert(0,'/opt/parser')
diagnostic={'exception':'none'}
try:
    from app.agriculture.ingestion import worker
    sys.argv=['worker','/input/request.json']
    with contextlib.redirect_stdout(io.StringIO()): worker.main()
except BaseException as error:
    allowed={'ImportError','ModuleNotFoundError','OSError','PermissionError',
             'MemoryError','RuntimeError','ValueError','TypeError','KeyError',
             'IngestionDenied','RasterioIOError'}
    kind=type(error).__name__
    diagnostic={'exception':kind if kind in allowed else 'other'}
    for library in {'libexpat.so.1','libstdc++.so.6','libgcc_s.so.1','libz.so.1'}:
        if str(error)==library+': cannot open shared object file: No such file or directory':
            diagnostic['missing_library']=library
    frames=traceback.extract_tb(error.__traceback__)
    for frame in frames:
        if frame.filename in {'/opt/parser/app/agriculture/ingestion/worker.py',
                              '/opt/parser/app/core/dataset_files.py'}:
            diagnostic['line']=frame.lineno
    # No exception message, traceback, file contents, environment, or paths.
print(json.dumps({'ok':True,'result':diagnostic}))
"""


class DockerProbe:
    """Test-only override, using the exact production container security flags."""

    isolation = "test-only-container-probe"

    def __init__(self, sandbox, code):
        self.sandbox, self.code = sandbox, code

    def command(self, directory, limits, operation_id):
        command = self.sandbox.command(directory, limits, operation_id)
        position = command.index(self.sandbox.image)
        return command[:position] + [
            "--entrypoint",
            "python",
            self.sandbox.image,
            "-I",
            "-c",
            self.code,
        ]

    def cleanup(self, operation_id):
        self.sandbox.cleanup(operation_id)


def test_docker_no_host_files_no_network_readonly(sandbox, tmp_path):
    canary = tmp_path / "host-canary"
    canary.write_text("synthetic-not-a-secret")
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        listener.listen()
        port = listener.getsockname()[1]
        code = f"""
import json,os,socket
from pathlib import Path
assert os.geteuid()!=0
assert not Path({str(canary)!r}).exists()
assert Path('/input/input.csv').read_bytes()==b'x'
for p in ['/input/forbidden','/opt/parser/forbidden']:
    try: Path(p).write_text('x')
    except OSError: pass
    else: raise AssertionError('writable prohibited mount')
try: socket.create_connection(('127.0.0.1',{port}),timeout=1)
except OSError: pass
else: raise AssertionError('host network reachable')
routes=Path('/proc/net/route').read_text().splitlines()[1:]
assert not any(row.split()[1]=='00000000' for row in routes)
assert 'NoNewPrivs:\\t1' in Path('/proc/self/status').read_text()
print(json.dumps({{'ok':True,'result':{{'isolation_checks':True}}}}))
"""
        result = SafeParser(
            tmp_path / "owned", DockerProbe(sandbox, code)
        ).inspect_stream(io.BytesIO(b"x"), asset_type="table", filename="a.csv")
    assert result["inspection"]["isolation_checks"] is True
    assert canary.read_text() == "synthetic-not-a-secret"
    assert not list((tmp_path / "owned").iterdir())


def test_docker_timeout_removes_container_and_temp(sandbox, tmp_path):
    probe = DockerProbe(sandbox, "import time; time.sleep(60)")
    parser = SafeParser(tmp_path / "owned", probe, ParseLimits(wall_seconds=2))
    with pytest.raises(IngestionDenied, match="parser_timeout"):
        parser.inspect_stream(io.BytesIO(b"x"), asset_type="table", filename="a.csv")
    assert not list((tmp_path / "owned").iterdir())
