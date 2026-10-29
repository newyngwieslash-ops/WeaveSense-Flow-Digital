"""Shared independently specified bytes; fixture data is not live GIS evidence."""
import json
from pathlib import Path

import pytest

from app.agriculture.identity.canonical import canonicalize

FIXTURE = (Path(__file__).resolve().parents[4]
           / "tests/agriculture/fixtures/identity-canonical.json")
VECTORS = json.loads(FIXTURE.read_text())


@pytest.mark.parametrize("case", VECTORS["valid"], ids=lambda case: case["name"])
def test_golden(case):
    result = canonicalize(case["input"].encode())
    assert result.hex() == case["output_hex"]
    assert canonicalize(result) == result


@pytest.mark.parametrize("case", VECTORS["invalid"], ids=lambda case: case["name"])
def test_invalid(case):
    with pytest.raises(ValueError):
        canonicalize(case["input"].encode())


def test_invalid_utf8():
    for case in VECTORS["invalid_hex"]:
        with pytest.raises(ValueError):
            canonicalize(bytes.fromhex(case["input_hex"]))


def test_bounds():
    canonicalize(b'{"v":"' + b'a' * ((1 << 20) - 8) + b'"}')
    with pytest.raises(ValueError):
        canonicalize(b'{"v":"' + b'a' * ((1 << 20) - 7) + b'"}')
    canonicalize(b'{"v":' + b'[' * 30 + b'0' + b']' * 30 + b'}')
    with pytest.raises(ValueError):
        canonicalize(b'{"v":' + b'[' * 31 + b'0' + b']' * 31 + b'}')
    canonicalize(json.dumps({str(i): 0 for i in range(256)}).encode())
    with pytest.raises(ValueError):
        canonicalize(json.dumps({str(i): 0 for i in range(257)}).encode())
    canonicalize(json.dumps({"v": [0] * 9998}).encode())
    with pytest.raises(ValueError):
        canonicalize(json.dumps({"v": [0] * 9999}).encode())
