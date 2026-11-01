"""Actual child rlimits are component proof, not kernel sandbox proof."""

import io
import sys
import time
from pathlib import Path

import pytest

from app.agriculture.ingestion.limits import IngestionDenied, ParseLimits
from app.agriculture.ingestion.runner import SafeParser


class LimitProbe:
    isolation = "test-only-resource-probe"

    def command(self, directory, limits, operation_id):
        return [
            sys.executable,
            "-I",
            str(Path(__file__).with_name("ingestion_limit_probe.py")),
            str(directory / "request.json"),
        ]

    def cleanup(self, operation_id):
        pass


@pytest.mark.parametrize("mode", ["cpu", "wall", "memory", "file", "output"])
def test_real_process_limits_cleanup(mode, tmp_path):
    limits = ParseLimits(
        cpu_seconds=1,
        wall_seconds=2,
        memory_bytes=128 * 1024**2,
        expanded_bytes=1024,
        output_bytes=1024,
    )
    parser = SafeParser(tmp_path / "owned", LimitProbe(), limits)
    start = time.monotonic()
    expected = {
        "cpu": "parser_process_failed",
        "wall": "parser_timeout",
        "memory": "probe_memory_enforced",
        "file": "probe_file_enforced",
        "output": "parser_output_limit",
    }[mode]
    with pytest.raises(IngestionDenied, match="^" + expected + "$"):
        parser.inspect_stream(
            io.BytesIO(b"input"), asset_type="table", filename=mode + ".csv"
        )
    assert time.monotonic() - start < 4
    assert not list((tmp_path / "owned").iterdir())
