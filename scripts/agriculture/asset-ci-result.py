"""Exact asset HTTP/browser gate; raw diagnostics never become artifacts."""
import argparse
import importlib.util
import json
import hashlib
from pathlib import Path
import re

spec = importlib.util.spec_from_file_location("farm_result", Path(__file__).with_name("farm-ci-result.py"))
common = importlib.util.module_from_spec(spec)
spec.loader.exec_module(common)
HTTP_ROOT = "TestAssetCatalogHTTPPostgresChain"
BROWSER_ROOT = "TestAssetBrowserPostgresChain"
DOWNLOAD_ROOT = "TestAssetCatalogDownloadPostgresChain"
DOWNLOAD_CHILDREN = set("real_unscoped_ready_entity_requires_fresh_grant get_head_range_and_immutable_content_hash foreign_tenant_and_same_tenant_workspace_denied historical_entity_requires_current_version_grant signed_download_stale_epoch_denied farm_scoped_real_entity_bytes rights_withdrawal_during_read_emits_no_bytes binding_revocation_during_read_emits_no_bytes real_logout_during_read_emits_no_bytes".split())
HTTP_CHILDREN = set("context_and_pending_creation idempotent_receipt_and_real_frontend_contract cross_scope_and_invalid_source_no_writes version_conflict_and_explicit_reauthorization farm_scoped_creation_requires_grant_and_plan revocation_no_content_or_mutation".split())
REQUIRED = {HTTP_ROOT, BROWSER_ROOT} | {HTTP_ROOT + "/" + name for name in HTTP_CHILDREN}
REQUIRED |= {DOWNLOAD_ROOT} | {DOWNLOAD_ROOT + "/" + name for name in DOWNLOAD_CHILDREN}
CHECKS = set("production_nginx_configuration_valid real_react_login_shared_vue_session http_only_platform_cookie explicit_asset_registration_capability explicit_initial_grant_plan pending_rights_metadata_only_preview duplicate_click_single_create asset_survives_reload metadata_not_ready_without_entity unknown_source_rights_pending version_two_committed old_grant_denies_current_and_history explicit_regrant_restores_version_two historical_version_one_readonly history_uses_current_authority not_ready_download_denied foreign_workspace_denied same_tenant_other_workspace_denied same_cookie_after_revocation revoked_old_page_clears_content revoked_history_navigation_no_content all_database_checkpoints_acknowledged no_internal_credentials_identity_spoofing_or_external_requests".split())
STAGES = set("preflight login_navigation login_account login_password login_submit workspace create_form initial_grants create_preview create_confirm create_reload version_form version_confirm old_grant regrant history download scope_negatives revocation complete".split())
UI_FLAGS = set("login session_ready context create detail editor preview busy conflict".split())
ERRORS = set("REVISION_CONFLICT STATE_CONFLICT INVALID_GEOMETRY INVALID_ARGUMENT INVALID_RESPONSE DEPENDENCY_UNAVAILABLE AUTHORITY_UNAVAILABLE NETWORK_ERROR TIMEOUT RESOURCE_NOT_FOUND FORBIDDEN".split())
GO_FILES = {"asset_browser_chain_test.go", "asset_catalog_http_test.go", "asset_catalog_download_chain_test.go", "farm_browser_chain_test.go", "identity_browser_chain_test.go"}
GO_STAGES = {"fixture", "asset_preflight", "child_start", "child_finish", "report_validate"} | {action+"_"+step for action in ("wait", "verify") for step in ("created", "versioned", "regranted", "revoke-request", "revoked-verified")}


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
            for stage in re.findall(r"ASSET_BROWSER_DIAGNOSTIC stage=([A-Za-z0-9_-]+)(?=\s|$)", event["Output"]):
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


def reduce(raw, browser, bootstrap, mode, artifacts_verified=False):
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
                      and network["observed"] > 0 and network["violations"] == 0 and network["creates"] == 1 and network["revisions"] == 1
                      and ("ci_provenance_sha256" not in browser or hex_hash(browser["ci_provenance_sha256"], 64)))
        valid_browser &= valid_meta
        if valid_meta:
            evidence = {"source_commit": browser["source_commit"], "artifact_hashes": hashes, "network": network}
            if "ci_provenance_sha256" in browser:
                evidence["ci_provenance_sha256"] = browser["ci_provenance_sha256"]
    valid_runtime = (isinstance(bootstrap, dict) and all(bootstrap.get(k) is True for k in ("redis_authenticated_ping", "fresh_volume", "runtime_not_owner_checked", "cross_database_denial_checked"))
                     and isinstance(mode, dict) and all(type(v) is bool for v in mode.values())
                     and mode == {"auth_component_only": False, "race": True, "required": True})
    passed = bool(valid_go and valid_browser and valid_runtime and artifacts_verified is True)
    return {"suite": "actual_asset_http_browser_dual_postgres", "passed": passed, "data_class": "fixture", "production_acceptance": False,
            "go_gate_passed": bool(valid_go), "browser_gate_passed": bool(valid_browser), "runtime_gate_passed": bool(valid_runtime), "artifact_verification_passed": artifacts_verified is True,
            "tests": [{"name": n, "result": terminal.get(n, "not_run")} for n in sorted(REQUIRED)],
            "browser_checks": [{"name": n, "result": checks.get(n, "not_run")} for n in sorted(CHECKS)],
            "browser_evidence": evidence, "failure_diagnostics": {} if passed else diagnostics(raw, browser, terminal),
            "passed_count": sum(v == "pass" for v in terminal.values())}


def verify_artifacts(browser, root):
    """Read only fixed repository-owned build/provenance paths, never report inputs."""
    try:
        expected = {
            "runner": root / "scripts/agriculture/asset-browser-e2e.mjs",
            "nginx_template": root / "deploy/agriculture/nginx.conf.template",
            "react_index": root / "web/dist-agriculture/index.html",
            "vue_index": root / "services/agri-gis/frontend/dist-agriculture/index.html",
        }
        provenance_path = root / ".local/evidence/session-ci-provenance.json"
        provenance_bytes = provenance_path.read_bytes()
        if len(provenance_bytes) > 16*1024*1024:
            return False
        provenance = common.strict_json(provenance_bytes.decode())
        return (isinstance(browser, dict)
                and browser.get("source_commit") == provenance.get("source_commit")
                and hex_hash(browser.get("source_commit"), 40)
                and browser.get("ci_provenance_sha256") == hashlib.sha256(provenance_bytes).hexdigest()
                and browser.get("artifact_hashes") == {name: hashlib.sha256(path.read_bytes()).hexdigest() for name, path in expected.items()})
    except (OSError, ValueError, TypeError, AttributeError, UnicodeError):
        return False


def main():
    parser = argparse.ArgumentParser()
    for key in ("input", "browser", "bootstrap", "mode", "output"):
        parser.add_argument("--"+key, type=Path, required=True)
    args = parser.parse_args()
    try:
        browser = common.strict_json(common.read_bounded(args.browser))
        report = reduce(common.read_bounded(args.input, 16*1024*1024), browser, common.strict_json(common.read_bounded(args.bootstrap)), common.strict_json(common.read_bounded(args.mode)), verify_artifacts(browser, Path(__file__).resolve().parents[2]))
    except (OSError, ValueError, TypeError, RecursionError):
        report = {"suite": "actual_asset_http_browser_dual_postgres", "passed": False, "production_acceptance": False, "reason": "missing_or_invalid_required_report"}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2)+"\n")
    return 0 if report["passed"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
