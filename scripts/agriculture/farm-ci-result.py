"""Closed, secret-free gate for real farm HTTP/browser dual-PG acceptance."""
import argparse
import json
from pathlib import Path
import re

PACKAGE = "yingce/backend/internal/agriculture"
HTTP_ROOT = "TestFarmHTTPPostgresChain"
BROWSER_ROOT = "TestFarmBrowserPostgresChain"
HTTP_CHILDREN = {
    "create_explicit_grants_and_regions", "governance_without_content_grant",
    "cross_tenant_and_workspace", "preview_hash_expiry_and_scope",
    "idempotent_and_conflicting_requests", "revision_race_and_archive",
    "current_revocation_blocks_replay", "signed_workspace_profiles_current_authority",
}
REQUIRED = {HTTP_ROOT, BROWSER_ROOT} | {HTTP_ROOT + "/" + n for n in HTTP_CHILDREN}
BROWSER_CHECKS = {
    "production_nginx_configuration_valid", "real_react_login_shared_vue_session", "http_only_platform_cookie",
    "north_created_with_explicit_grants", "south_created_with_explicit_grants", "duplicate_click_single_create",
    "created_farms_survive_reload", "explicit_region_unit_crs_demo", "openlayers_real_canvas",
    "invalid_geometry_no_write", "missing_crs_no_preview", "duplicate_external_code_rejected",
    "north_ui_update_revision_two", "concurrent_real_api_update_revision_three", "stale_revision_409_preserves_draft",
    "archive_ui_history_readonly", "foreign_workspace_denied", "same_tenant_other_workspace_denied",
    "same_cookie_after_revocation", "revoked_old_page_clears_content", "revoked_history_navigation_no_content",
    "all_database_checkpoints_acknowledged", "no_internal_credentials_identity_spoofing_or_external_requests",
}


def strict_json(raw):
    def pairs(items):
        value = {}
        for key, item in items:
            if key in value:
                raise ValueError("duplicate key")
            value[key] = item
        return value

    def invalid_constant(_):
        raise ValueError("nonfinite number")
    return json.loads(raw, object_pairs_hook=pairs, parse_constant=invalid_constant)


def reduce_go(raw, required, package):
    terminal, ran, package_pass, invalid = {}, set(), 0, False
    for line in raw.splitlines():
        try:
            event = strict_json(line)
            if not isinstance(event, dict):
                raise ValueError("event object required")
            name, action = event.get("Test"), event.get("Action")
            if name is not None and not isinstance(name, str):
                raise ValueError("invalid name")
            if not isinstance(action, str):
                raise ValueError("invalid action")
        except (ValueError, TypeError):
            invalid = True
            continue
        if action in {"fail", "skip", "build-fail"}:
            invalid = True
        if action == "run":
            if name not in required or name in ran or event.get("Package") != package:
                invalid = True
            else:
                ran.add(name)
        if action in {"pass", "fail", "skip"}:
            if event.get("Package") != package:
                invalid = True
            if name:
                if name not in required or name in terminal:
                    invalid = True
                else:
                    terminal[name] = action
            elif action == "pass":
                package_pass += 1
    valid_go = (not invalid and package_pass == 1 and ran == required
                and set(terminal) == required and all(v == "pass" for v in terminal.values()))
    return valid_go, terminal


def reduce(raw, browser, bootstrap, mode):
    valid_go, terminal = reduce_go(raw, REQUIRED, PACKAGE)
    checks = {}
    browser_evidence = {}
    valid_browser = isinstance(browser, dict)
    if valid_browser:
        valid_browser = (browser.get("result") == "pass" and browser.get("stage") == "complete"
                         and browser.get("data_class") == "fixture" and browser.get("production_acceptance") is False)
        rows = browser.get("results")
        if not isinstance(rows, list):
            valid_browser = False
            rows = []
        for item in rows:
            if (not isinstance(item, dict) or not isinstance(item.get("name"), str)
                    or item["name"] not in BROWSER_CHECKS or item["name"] in checks):
                valid_browser = False
                continue
            checks[item["name"]] = "pass" if item.get("result") == "pass" else "not_pass"
        valid_browser &= set(checks) == BROWSER_CHECKS and all(v == "pass" for v in checks.values())
        def hex_hash(value, length):
            return isinstance(value, str) and re.fullmatch("[0-9a-f]{%d}" % length, value) is not None
        hashes, network = browser.get("artifact_hashes"), browser.get("network")
        metadata_valid = (
            browser.get("schema_version") == "1.0" and hex_hash(browser.get("source_commit"), 40)
            and isinstance(hashes, dict) and set(hashes) == {"nginx_template", "react_index", "vue_index", "runner"}
            and all(hex_hash(v, 64) for v in hashes.values())
            and isinstance(network, dict) and set(network) == {"observed", "violations", "previews", "creates", "updates", "archives"}
            and all(type(v) is int and 0 <= v <= 10**9 for v in network.values())
            and network["observed"] > 0 and network["violations"] == 0
            and ("ci_provenance_sha256" not in browser or hex_hash(browser["ci_provenance_sha256"], 64))
        )
        valid_browser &= metadata_valid
        if metadata_valid:
            browser_evidence = {"source_commit": browser["source_commit"], "artifact_hashes": hashes, "network": network}
            if "ci_provenance_sha256" in browser:
                browser_evidence["ci_provenance_sha256"] = browser["ci_provenance_sha256"]
    valid_runtime = (isinstance(bootstrap, dict) and isinstance(mode, dict)
                     and bootstrap.get("redis_authenticated_ping") is True
                     and bootstrap.get("fresh_volume") is True
                     and bootstrap.get("runtime_not_owner_checked") is True
                     and bootstrap.get("cross_database_denial_checked") is True
                     and all(type(v) is bool for v in mode.values())
                     and mode == {"auth_component_only": False, "race": True, "required": True})
    return {
        "suite": "actual_farm_http_browser_dual_postgres", "passed": bool(valid_go and valid_browser and valid_runtime),
        "data_class": "fixture", "production_acceptance": False,
        "go_gate_passed": bool(valid_go), "browser_gate_passed": bool(valid_browser), "runtime_gate_passed": bool(valid_runtime),
        "tests": [{"name": n, "result": terminal.get(n, "not_run")} for n in sorted(REQUIRED)],
        "browser_checks": [{"name": n, "result": checks.get(n, "not_run")} for n in sorted(BROWSER_CHECKS)],
        "browser_evidence": browser_evidence,
        "passed_count": sum(v == "pass" for v in terminal.values()),
    }


def read_bounded(path, limit=2 * 1024 * 1024):
    with path.open("rb") as source:
        value = source.read(limit + 1)
    if len(value) > limit:
        raise ValueError("oversized report")
    return value.decode("utf-8")


def main():
    parser = argparse.ArgumentParser()
    for key in ("input", "browser", "bootstrap", "mode", "output"):
        parser.add_argument("--" + key, type=Path, required=True)
    args = parser.parse_args()
    try:
        report = reduce(read_bounded(args.input, 16 * 1024 * 1024),
                        strict_json(read_bounded(args.browser)), strict_json(read_bounded(args.bootstrap)),
                        strict_json(read_bounded(args.mode)))
    except (OSError, ValueError, TypeError, RecursionError):
        report = {"suite": "actual_farm_http_browser_dual_postgres", "passed": False,
                  "reason": "missing_or_invalid_required_report", "production_acceptance": False}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
