"""Exact parcel identity/domain/PostGIS gate; no raw exception output."""
import argparse
import hashlib
import json
from pathlib import Path
import xml.etree.ElementTree as ET

CONTRACT = Path(__file__).resolve().parents[2] / "contracts/agriculture/parcel-python-acceptance.json"
CASES = json.loads(CONTRACT.read_text())["required_cases"]
REQUIRED = set(CASES)
assert len(CASES) == len(REQUIRED) == 52


def reduce(raw):
    root = ET.fromstring(raw)
    seen, results = set(), []
    invalid = any(int(s.get(key, "0")) != 0 for s in root.iter("testsuite") for key in ("errors", "failures", "skipped"))
    for case in root.iter("testcase"):
        name = case.get("classname", "").split(".")[-1] + ".py::" + case.get("name", "")
        if name not in REQUIRED or name in seen:
            invalid = True
            continue
        seen.add(name)
        passed = not any(child.tag in {"failure", "error", "skipped"} for child in case)
        invalid |= not passed
        results.append({"name": name, "passed": passed})
    return {"suite": "parcel_python_domain_and_postgis", "passed": not invalid and seen == REQUIRED,
            "data_class": "fixture", "production_acceptance": False, "live_farm_data": False,
            "required_count": len(REQUIRED), "observed_count": len(seen), "tests": results,
            "contract_sha256": hashlib.sha256(CONTRACT.read_bytes()).hexdigest()}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        with args.input.open("rb") as source:
            raw = source.read(16 * 1024 * 1024 + 1)
        if len(raw) > 16 * 1024 * 1024:
            raise ValueError("oversized report")
        report = reduce(raw.decode("utf-8"))
    except (OSError, ValueError, ET.ParseError):
        report = {"suite": "parcel_python_domain_and_postgis", "passed": False, "reason": "missing_or_malformed_report"}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    raise SystemExit(0 if report["passed"] else 1)
