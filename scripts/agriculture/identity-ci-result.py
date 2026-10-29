"""Reduce private test output to allowlisted names/status only; fail on skips."""
import argparse
import json
from pathlib import Path
import re
import xml.etree.ElementTree as ET

GO_ROOT = "TestIdentityRealHTTPGoPython"
GO_CASES = {GO_ROOT} | {GO_ROOT + "/" + name for name in (
    "valid_read", "scope_path_mismatch", "signature", "method", "path", "query", "body", "write_payload_hash",
    "audience", "expired", "revoked_signing_key", "actor", "epoch", "revoked_binding_old_ticket")}
PG_REQUIRED = {
    "test_postgres_replay_parallel_persistence_scope_and_fail_closed",
    "test_postgres_actual_verifier_dependency_nonce_order_and_parallel_http",
}


def safe_locations(text):
    """Keep only source basenames/lines, never exception text or local paths."""
    matches = re.findall(r"\b([A-Za-z0-9_]+\.(?:py|go)):(\d+)\b", text)
    return [{"file": name, "line": int(line)} for name, line in sorted(set(matches))[:20]]


def reduce(kind, raw):
    results, seen, invalid = [], set(), False
    locations = {}
    if kind == "go":
        terminal = {}
        package_pass = False
        for line in raw.splitlines():
            try:
                event = json.loads(line)
            except (ValueError, TypeError):
                invalid = True
                continue
            action, name = event.get("Action"), event.get("Test")
            if name in GO_CASES and action == "output":
                locations.setdefault(name, []).extend(safe_locations(event.get("Output", "")))
            if not name and action == "pass":
                package_pass = True
            if action in {"fail", "skip"}:
                invalid = True
            if name in GO_CASES and action in {"pass", "fail", "skip"}:
                if name in terminal:
                    invalid = True
                terminal[name] = action
        results = [{"name": name, "result": status} for name, status in sorted(terminal.items())]
        seen = set(terminal)
        invalid |= seen != GO_CASES or not package_pass
    else:
        try:
            root = ET.fromstring(raw)
        except ET.ParseError:
            return {"suite": kind, "passed": False, "tests": [], "reason": "invalid_test_report"}
        classes = set()
        for case in root.iter("testcase"):
            name = case.get("name", "").split("[", 1)[0]
            module = case.get("classname", "").split(".")[-1]
            if not re.fullmatch(r"test_[A-Za-z0-9_]+", name) or module not in {
                "test_agriculture_identity", "test_agriculture_identity_postgres", "test_identity_canonical"
            }:
                invalid = True
                continue
            status = "fail" if case.find("failure") is not None or case.find("error") is not None else "skip" if case.find("skipped") is not None else "pass"
            results.append({"name": name, "module": module, "result": status})
            if status == "fail":
                locations[name] = safe_locations(" ".join(node.text or "" for node in case if node.tag in {"failure", "error"}))
            invalid |= status != "pass"
            seen.add(name)
            classes.add(module)
        invalid |= not PG_REQUIRED <= seen or classes != {
            "test_agriculture_identity", "test_agriculture_identity_postgres", "test_identity_canonical"
        }
        # Collection/setup errors may not be represented by a testcase failure.
        invalid |= any(int(s.get("errors", "0")) or int(s.get("failures", "0")) or int(s.get("skipped", "0")) for s in root.iter("testsuite"))
    return {"suite": kind, "passed": bool(results) and not invalid, "data_class": "fixture",
            "production_acceptance": False, "tests": results,
            "failure_locations": [{"test": item["name"], "locations": locations.get(item["name"], [])[:20]} for item in results if item["result"] == "fail"],
            "passed_count": sum(x["result"] == "pass" for x in results)}


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--kind", choices=["go", "python"], required=True)
    p.add_argument("--input", type=Path, required=True)
    p.add_argument("--output", type=Path, required=True)
    args = p.parse_args()
    try:
        report = reduce(args.kind, args.input.read_text())
    except Exception:
        report = {"suite": args.kind, "passed": False, "tests": [], "reason": "unreadable_test_report"}
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    suite = ET.Element("testsuite", name="identity-" + args.kind, tests=str(len(report["tests"])))
    for item in report["tests"]:
        case = ET.SubElement(suite, "testcase", name=item["name"])
        if item["result"] != "pass":
            ET.SubElement(case, "failure", message="required test did not pass")
    if not report["passed"]:
        case = ET.SubElement(suite, "testcase", name="required_suite_gate")
        ET.SubElement(case, "failure", message="missing, skipped or failed required coverage")
    ET.ElementTree(suite).write(args.output.with_suffix(".xml"), encoding="utf-8", xml_declaration=True)
    raise SystemExit(0 if report["passed"] else 1)


if __name__ == "__main__":
    main()
