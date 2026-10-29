#!/usr/bin/env python3
"""Reject skipped/missing/failed Go tests; emit names/status only, never raw output."""
import argparse
import json
from pathlib import Path
import sys


def verify(lines, suite):
    required = {
        "postgres": {"yingce/backend/internal/database", "yingce/backend/internal/agriculture"},
        "legacy-flow": {"yingce/backend/internal/flow"},
    }[suite]
    passed, failed, skipped, packages = set(), set(), set(), set()
    for line in lines:
        item = json.loads(line)
        action, package, test = item.get("Action"), item.get("Package"), item.get("Test")
        if package not in required:
            continue
        key = (package, test or "<package>")
        if action == "pass":
            if test:
                passed.add(key)
            else:
                packages.add(package)
        elif action == "fail":
            failed.add(key)
        elif action == "skip":
            skipped.add(key)
    present = {package for package, test in passed if suite != "postgres" or test.startswith("TestAgriculturePostgres")}
    missing = sorted(required - (present & packages))
    expected_database_test = ("yingce/backend/internal/database", "TestAgriculturePostgresAdmissionAndStrictRLS")
    if suite == "postgres" and expected_database_test not in passed:
        missing.append("TestAgriculturePostgresAdmissionAndStrictRLS")
    if suite == "postgres":
        for test in ("TestAgriculturePostgresMembershipLifecycle", "TestAgriculturePostgresConcurrentAcceptance"):
            if ("yingce/backend/internal/agriculture", test) not in passed:
                missing.append(test)
    if suite == "legacy-flow":
        for test in ("TestOrgInviteSingleUseAndGrantRevocation", "TestP7MembersRoleEditing"):
            if ("yingce/backend/internal/flow", test) not in passed:
                missing.append(test)
        if not any(test.startswith("TestAgricultureMembership") for _, test in passed):
            missing.append("TestAgricultureMembership*")
    return {"suite": suite, "passed": not (failed or skipped or missing),
            "tests_passed": [f"{p}/{t}" for p, t in sorted(passed)],
            "tests_failed": [f"{p}/{t}" for p, t in sorted(failed)],
            "tests_skipped": [f"{p}/{t}" for p, t in sorted(skipped)],
            "missing_required": missing,
            "data_class": "fixture", "live_farm_or_production_acceptance": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--suite", choices=["postgres", "legacy-flow"], required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        result = verify(sys.stdin, args.suite)
    except (ValueError, TypeError, KeyError):
        result = {"suite": args.suite, "passed": False, "reason": "invalid_go_test_json_stream"}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))
    return 0 if result["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
