"""Exact farm Python component/PostGIS gate; never exports exception text."""
import argparse
import hashlib
import json
from pathlib import Path
import xml.etree.ElementTree as ET

CONTRACT = Path(__file__).resolve().parents[2] / "contracts/agriculture/farm-python-acceptance.json"
REQUIRED = set(json.loads(CONTRACT.read_text())["required_cases"])
assert len(REQUIRED) == 81


def reduce(raw):
    seen, results, invalid = set(), [], False
    root = ET.fromstring(raw)
    invalid |= any(int(s.get(key, "0")) != 0 for s in root.iter("testsuite") for key in ("errors", "failures", "skipped"))
    for case in root.iter("testcase"):
        name = case.get("classname", "").split(".")[-1] + ".py::" + case.get("name", "")
        if name not in REQUIRED or name in seen:
            invalid = True
            continue
        seen.add(name)
        passed = not any(child.tag in {"failure", "error", "skipped"} for child in case)
        results.append({"name": name, "passed": passed})
        invalid |= not passed
    return {"passed": not invalid and seen == REQUIRED, "data_class": "fixture",
            "live_farm_data": False, "required_count": len(REQUIRED),
            "observed_count": len(seen), "tests": results,
            "contract_sha256": hashlib.sha256(CONTRACT.read_bytes()).hexdigest()}


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        report = reduce(args.input.read_text())
    except (OSError, ValueError, ET.ParseError):
        report = {"passed": False, "reason": "missing_or_malformed_report"}
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    raise SystemExit(0 if report["passed"] else 1)
