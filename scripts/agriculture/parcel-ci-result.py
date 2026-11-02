"""Exact parcel HTTP/browser gate; raw diagnostics never become artifacts."""
import argparse
import importlib.util
import json
from pathlib import Path
import re

spec = importlib.util.spec_from_file_location("farm_result", Path(__file__).with_name("farm-ci-result.py"))
common = importlib.util.module_from_spec(spec)
spec.loader.exec_module(common)
HTTP_ROOT = "TestParcelHTTPPostgresChain"
BROWSER_ROOT = "TestParcelBrowserPostgresChain"
HTTP_CHILDREN = set("preview_create_receipt_and_replay context_list_detail_history_current real_response_frontend_contract reject_foreign_workspace_and_invalid_geometry_without_writes revision_history_and_stale_preview_conflict direct_grants_mixed_context_cursor_and_version_staleness revoke_current_grant_blocks_content_and_preview".split())
REQUIRED = {HTTP_ROOT, BROWSER_ROOT} | {HTTP_ROOT + "/" + name for name in HTTP_CHILDREN}
CHECKS = set("production_nginx_configuration_valid real_react_login_shared_vue_session http_only_platform_cookie explicit_parent_parcel_capabilities create_preview_bound_to_parent duplicate_click_single_create parcel_survives_reload current_geometry_crs_area_hash ui_revision_two_same_identity historical_revision_one_readonly historical_at_selector_version_one current_revision_restored concurrent_api_revision_three stale_revision_409_preserves_draft foreign_workspace_denied same_tenant_other_workspace_denied same_cookie_after_revocation revoked_old_page_clears_content revoked_history_navigation_no_content all_database_checkpoints_acknowledged no_internal_credentials_identity_spoofing_or_external_requests".split())
STAGES = set("preflight login_navigation login_account login_password login_submit workspace parent_open parcel_open create_form create_preview create_confirm create_reload revision_form revision_confirm history at_time race scope_negatives revocation complete".split())
UI_FLAGS = set("login session_ready context create detail editor preview busy conflict".split())
ERRORS = set("REVISION_CONFLICT STATE_CONFLICT INVALID_GEOMETRY INVALID_ARGUMENT INVALID_RESPONSE DEPENDENCY_UNAVAILABLE AUTHORITY_UNAVAILABLE NETWORK_ERROR TIMEOUT RESOURCE_NOT_FOUND FORBIDDEN".split())
GO_FILES = {"parcel_browser_chain_test.go", "parcel_http_chain_test.go", "farm_browser_chain_test.go", "identity_browser_chain_test.go"}
GO_STAGES = {"fixture", "explicit_grants", "child_start", "child_finish", "report_validate"} | {action+"_"+step for action in ("wait", "verify") for step in ("created", "revised", "raced", "revoke-request", "revoked-verified")}


def hex_hash(value, size):
    return isinstance(value, str) and re.fullmatch("[a-f0-9]{%d}" % size, value) is not None


def diagnostics(raw, browser, terminal):
    locations, stages = {}, {}
    for line in raw.splitlines():
        try:
            event = common.strict_json(line)
            if not isinstance(event, dict):
                continue
            name = event.get("Test")
            if not isinstance(name, str) or name not in REQUIRED or terminal.get(name) != "fail" or event.get("Action") != "output" or event.get("Package") != common.PACKAGE or not isinstance(event.get("Output"), str):
                continue
            for file, number in re.findall(r"\b([A-Za-z0-9_]+\.go):([1-9][0-9]{0,5})(?=[:\s])", event["Output"]):
                if file in GO_FILES:
                    locations.setdefault(name, set()).add((file, int(number)))
            for stage in re.findall(r"PARCEL_BROWSER_DIAGNOSTIC stage=([A-Za-z0-9_-]+)(?=\s|$)", event["Output"]):
                if stage in GO_STAGES:
                    stages.setdefault(name, set()).add(stage)
        except (ValueError, TypeError):
            continue
    out = {"go_locations": [{"test": n, "locations": [{"file": f, "line": i} for f, i in sorted(points)[:12]]} for n, points in sorted(locations.items())],
           "go_stages": [{"test": n, "stages": sorted(values)} for n, values in sorted(stages.items())]}
    if isinstance(browser, dict):
        stage = browser.get("stage")
        if isinstance(stage, str) and stage in STAGES:
            out["browser_stage"] = stage
        flags = browser.get("ui_flags")
        if isinstance(flags, dict):
            out["ui_flags"] = {k: flags[k] for k in sorted(UI_FLAGS & flags.keys()) if type(flags[k]) is bool}
        code = browser.get("error_code")
        if code is None or isinstance(code, str) and code in ERRORS:
            out["error_code"] = code
    return out


def reduce(raw, browser, bootstrap, mode):
    valid_go, terminal = common.reduce_go(raw, REQUIRED, common.PACKAGE)
    checks, evidence = {}, {}
    valid_browser = isinstance(browser, dict)
    if valid_browser:
        valid_browser = browser.get("result") == "pass" and browser.get("stage") == "complete" and browser.get("schema_version") == "1.0" and browser.get("data_class") == "fixture" and browser.get("production_acceptance") is False and browser.get("diagnostic_only") is not True
        rows = browser.get("results")
        if not isinstance(rows, list):
            valid_browser, rows = False, []
        for row in rows:
            if not isinstance(row, dict) or not isinstance(row.get("name"), str) or row["name"] not in CHECKS or row["name"] in checks:
                valid_browser = False
                continue
            checks[row["name"]] = "pass" if row.get("result") == "pass" else "not_pass"
        valid_browser &= set(checks) == CHECKS and all(v == "pass" for v in checks.values())
        hashes, network = browser.get("artifact_hashes"), browser.get("network")
        valid_meta = (hex_hash(browser.get("source_commit"), 40)
                      and isinstance(hashes, dict) and set(hashes) == {"nginx_template", "react_index", "vue_index", "runner"}
                      and all(hex_hash(v, 64) for v in hashes.values())
                      and isinstance(network, dict) and set(network) == {"observed", "violations", "creates", "revisions"}
                      and all(type(v) is int and 0 <= v <= 10**9 for v in network.values())
                      and network["observed"] > 0 and network["violations"] == 0
                      and ("ci_provenance_sha256" not in browser or hex_hash(browser["ci_provenance_sha256"], 64)))
        valid_browser &= valid_meta
        if valid_meta:
            evidence = {"source_commit": browser["source_commit"], "artifact_hashes": hashes, "network": network}
            if "ci_provenance_sha256" in browser:
                evidence["ci_provenance_sha256"] = browser["ci_provenance_sha256"]
    valid_runtime = (isinstance(bootstrap, dict) and all(bootstrap.get(k) is True for k in ("redis_authenticated_ping", "fresh_volume", "runtime_not_owner_checked", "cross_database_denial_checked"))
                     and isinstance(mode, dict) and all(type(v) is bool for v in mode.values())
                     and mode == {"auth_component_only": False, "race": True, "required": True})
    passed = bool(valid_go and valid_browser and valid_runtime)
    return {"suite": "actual_parcel_http_browser_dual_postgres", "passed": passed, "data_class": "fixture", "production_acceptance": False,
            "go_gate_passed": bool(valid_go), "browser_gate_passed": bool(valid_browser), "runtime_gate_passed": bool(valid_runtime),
            "tests": [{"name": n, "result": terminal.get(n, "not_run")} for n in sorted(REQUIRED)],
            "browser_checks": [{"name": n, "result": checks.get(n, "not_run")} for n in sorted(CHECKS)],
            "browser_evidence": evidence, "failure_diagnostics": {} if passed else diagnostics(raw, browser, terminal),
            "passed_count": sum(v == "pass" for v in terminal.values())}


def main():
    parser = argparse.ArgumentParser()
    for key in ("input", "browser", "bootstrap", "mode", "output"):
        parser.add_argument("--"+key, type=Path, required=True)
    args = parser.parse_args()
    try:
        report = reduce(common.read_bounded(args.input, 16*1024*1024), common.strict_json(common.read_bounded(args.browser)), common.strict_json(common.read_bounded(args.bootstrap)), common.strict_json(common.read_bounded(args.mode)))
    except (OSError, ValueError, TypeError, RecursionError):
        report = {"suite": "actual_parcel_http_browser_dual_postgres", "passed": False, "production_acceptance": False, "reason": "missing_or_invalid_required_report"}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2)+"\n")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
