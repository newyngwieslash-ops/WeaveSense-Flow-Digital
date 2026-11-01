"""Exact DT024 route-matrix coverage gate; no raw diagnostics leave CI."""
import argparse
import hashlib
import json
from pathlib import Path
import xml.etree.ElementTree as ET

GIS_MODULE = "test_agriculture_isolation_matrix_postgres"
GO_MODULE = "test_agriculture_isolation_go_postgres"
ROUTES = ("ownership", "metadata", "asset_get", "asset_head", "report_get", "report_head", "job_accept", "job_checkpoint")
NEGATIVES = ("unsigned", "wrong_signature", "audience", "path", "body", "expired", "tenant", "workspace")
GIS_CASES = {"test_isolation_exact_production_route_manifest", "test_isolation_public_health_legacy_absent_and_pg_controls"}
GIS_CASES |= {f"test_isolation_signed_route_negative_no_domain_effect[{case}-{route}]" for case in NEGATIVES for route in ROUTES}
GIS_CASES |= {f"test_isolation_production_missing_dependencies_closed[{route}]" for route in ROUTES}
GIS_CASES |= {f"test_isolation_job_snapshot_epoch_no_domain_effect[{route}]" for route in ("job_accept", "job_checkpoint")}
GO_CASES = {"test_isolation_go_route_matrix", "test_isolation_go_double_postgres_objects", "test_isolation_go_postgres_projections"}
REQUIRED = {(GIS_MODULE, n) for n in GIS_CASES} | {(GO_MODULE, n) for n in GO_CASES}
def strict_json(raw):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate evidence key")
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=unique)


CONTRACT_PATH = Path(__file__).resolve().parents[2] / "contracts/agriculture/isolation-matrix.json"
CONTRACT_BYTES = CONTRACT_PATH.read_bytes()
CONTRACT = strict_json(CONTRACT_BYTES)
GO_ROOTS = {"matrix": CONTRACT["matrix_root"]} | {
    name: value["root"] for name, value in CONTRACT["go_suites"].items()
}
EXPECTED_ROUTES = {row["method"] + " " + row["path"] for row in CONTRACT["routes"]}
EXPECTED_OBSERVATIONS = {(row["id"], case["id"]): case["expected_status"]
                         for row in CONTRACT["routes"] for case in row["cases"]}
GO_EXPECTED = {"matrix": {CONTRACT["matrix_root"]} |
               {CONTRACT["matrix_root"] + "/" + row["id"] for row in CONTRACT["routes"]} |
               {case["go_test"] for row in CONTRACT["routes"] for case in row["cases"]}}
GO_EXPECTED.update({name: {value["root"]} | {value["root"] + "/" + c for c in value["children"]}
                    for name, value in CONTRACT["go_suites"].items()})
# The reviewed baseline has one row per registered method/path and one row per
# actual HTTP request. Contract edits cannot silently collapse duplicates.
assert CONTRACT["schema_version"] == "1.0"
assert len(EXPECTED_ROUTES) == len(CONTRACT["routes"]) == 21
assert len({row["id"] for row in CONTRACT["routes"]}) == 21
assert len(EXPECTED_OBSERVATIONS) == sum(len(row["cases"]) for row in CONTRACT["routes"]) == 158
assert len(GO_EXPECTED["matrix"]) == 180
assert len(GO_EXPECTED["objects"]) == 4 and len(GO_EXPECTED["projections"]) == 6



def reduce(raw, reports):
    root = ET.fromstring(raw)
    seen, results = set(), []
    invalid = False
    for case in root.iter("testcase"):
        key = (case.get("classname", "").split(".")[-1], case.get("name", ""))
        if key not in REQUIRED or key in seen:
            invalid = True
            continue
        seen.add(key)
        passed = not any(child.tag in {"failure", "error", "skipped"} for child in case)
        invalid |= not passed
        results.append({"module": key[0], "name": key[1], "passed": passed})
    invalid |= seen != REQUIRED
    invalid |= any(int(s.get(k, "0")) != 0 for s in root.iter("testsuite") for k in ("errors", "failures", "skipped"))
    invalid |= set(reports) != set(GO_ROOTS) | {"routes"}
    for suite in GO_ROOTS:
        report = reports[suite]
        states = report.get("tests", {})
        invalid |= report.get("passed") is not True or type(report.get("exit_code")) is not int or report.get("exit_code") != 0
        invalid |= report.get("auth_component_only") is not False
        invalid |= report.get("race_enabled") is not True
        invalid |= report.get("missing_required") != []
        invalid |= not isinstance(states, dict) or set(states) != GO_EXPECTED[suite]
        invalid |= any(value != "pass" for value in states.values())
    matrix = reports["routes"]
    invalid |= matrix.get("passed") is not True or matrix.get("auth_component_only") is not False
    registered = matrix.get("exact_routes", [])
    invalid |= not isinstance(registered, list) or len(registered) != len(EXPECTED_ROUTES)
    invalid |= set(registered) != EXPECTED_ROUTES or len(set(registered)) != len(registered)
    observations = matrix.get("observations", [])
    observed = set()
    for row in observations:
        key = (row.get("route"), row.get("case"))
        status = row.get("status")
        invalid |= key in observed or key not in EXPECTED_OBSERVATIONS
        observed.add(key)
        invalid |= type(status) is not int or status != EXPECTED_OBSERVATIONS.get(key)
        # Positive controls are read/idempotent too; every recorded request must
        # preserve the snapshot, not just requests reporting an error status.
        invalid |= row.get("business_snapshot_unchanged") is not True
    invalid |= observed != set(EXPECTED_OBSERVATIONS) or len(observations) != len(EXPECTED_OBSERVATIONS)
    return {"suite": "isolation_matrix", "passed": not invalid, "data_class": "fixture",
            "production_acceptance": False,
            "auth_component_only": any(reports[s].get("auth_component_only") is True for s in GO_ROOTS),
            "race_enabled": all(reports[s].get("race_enabled") is True for s in GO_ROOTS),
            "tests": results, "required_count": len(REQUIRED), "observed_count": len(seen),
            "platform_route_count": len(registered), "gis_route_count": 9,
            "expected_request_cases": len(EXPECTED_OBSERVATIONS),
            "contract_sha256": hashlib.sha256(CONTRACT_BYTES).hexdigest(),
            "denial_snapshot_observations": sum(row.get("status", 0) >= 400 for row in observations),
            "scope": "Current agricultural routes and explicit PG fixture adapters; not future domain APIs or W06 runtime."}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", type=Path, required=True)
    parser.add_argument("--reports-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        reports = {key: strict_json((args.reports_dir / f"isolation-go-{key}.json").read_text()) for key in GO_ROOTS}
        reports["routes"] = strict_json((args.reports_dir / "isolation-routes-go.json").read_text())
        report = reduce(args.input.read_text(), reports)
    except Exception:
        report = {"suite": "isolation_matrix", "passed": False, "reason": "missing_or_invalid_evidence"}
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    raise SystemExit(0 if report["passed"] else 1)


if __name__ == "__main__":
    main()
