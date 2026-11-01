import copy
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("parcel_result", Path(__file__).with_name("parcel-ci-result.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
ROOT = Path(__file__).resolve().parents[2]


class ParcelGateTests(unittest.TestCase):
    def setUp(self):
        self.events = [{"Action": action, "Test": name, "Package": gate.common.PACKAGE} for name in sorted(gate.REQUIRED) for action in ("run", "pass")]
        self.events.append({"Action": "pass", "Package": gate.common.PACKAGE})
        self.browser = {"schema_version": "1.0", "result": "pass", "stage": "complete", "data_class": "fixture", "production_acceptance": False,
                        "results": [{"name": name, "result": "pass"} for name in sorted(gate.CHECKS)], "source_commit": "a"*40,
                        "artifact_hashes": {k: "b"*64 for k in ("nginx_template", "react_index", "vue_index", "runner")},
                        "network": {"observed": 20, "violations": 0, "creates": 1, "revisions": 2}}
        self.bootstrap = {k: True for k in ("redis_authenticated_ping", "fresh_volume", "runtime_not_owner_checked", "cross_database_denial_checked")}
        self.mode = {"auth_component_only": False, "race": True, "required": True}

    def report(self, events=None, **kwargs):
        return gate.reduce("\n".join(map(json.dumps, self.events if events is None else events)), kwargs.get("browser", self.browser), kwargs.get("bootstrap", self.bootstrap), kwargs.get("mode", self.mode))

    def test_complete_exact_go_and_browser_sets(self):
        result = self.report()
        self.assertTrue(result["passed"])
        self.assertEqual(result["passed_count"], 9)
        self.assertEqual(len(result["browser_checks"]), 21)

    def test_missing_duplicate_skipped_failed_unknown_go_never_passes(self):
        for i in range(len(self.events)):
            events = self.events.copy()
            events.pop(i)
            self.assertFalse(self.report(events)["passed"])
        for event in (self.events[0], self.events[1], self.events[-1], {"Action": "skip", "Test": "SECRET"}, {"Action": "fail"}, {"Action": "pass", "Test": "SECRET"}):
            result = self.report(self.events + [event])
            self.assertFalse(result["passed"])
            self.assertNotIn("SECRET", json.dumps(result))

    def test_every_browser_check_is_unique_required_and_passed(self):
        for i in range(len(self.browser["results"])):
            for action in ("omit", "duplicate", "skip", "fail"):
                browser = copy.deepcopy(self.browser)
                if action == "omit":
                    browser["results"].pop(i)
                elif action == "duplicate":
                    browser["results"].append(browser["results"][i])
                else:
                    browser["results"][i]["result"] = action
                self.assertFalse(self.report(browser=browser)["passed"])

    def test_runtime_and_browser_provenance_mandatory(self):
        for key in self.bootstrap:
            self.assertFalse(self.report(bootstrap={**self.bootstrap, key: False})["passed"])
        for key in self.mode:
            for value in (not self.mode[key], int(self.mode[key]), None):
                self.assertFalse(self.report(mode={**self.mode, key: value})["passed"])
        for key in ("artifact_hashes", "network", "source_commit", "schema_version"):
            self.assertFalse(self.report(browser={**self.browser, key: None})["passed"])
        self.assertFalse(self.report(browser={**self.browser, "diagnostic_only": True})["passed"])
        self.assertFalse(self.report(browser={**self.browser, "network": {**self.browser["network"], "violations": 1}})["passed"])

    def test_safe_diagnostics_do_not_relax_failure_or_copy_private_text(self):
        events = [{**e, "Action": "fail"} if e.get("Test") == gate.BROWSER_ROOT and e["Action"] == "pass" else e for e in self.events]
        events.append({"Action": "output", "Package": gate.common.PACKAGE, "Test": gate.BROWSER_ROOT, "Output": "/SECRET/parcel_browser_chain_test.go:120: SECRET\nPARCEL_BROWSER_DIAGNOSTIC stage=wait_created\nPARCEL_BROWSER_DIAGNOSTIC stage=SECRET"})
        browser = {"result": "fail", "stage": "create_preview", "ui_flags": {"context": True, "editor": "SECRET", "secret": "SECRET"}, "error_code": "INVALID_RESPONSE", "failure": "SECRET"}
        report = self.report(events, browser=browser)
        self.assertFalse(report["passed"])
        self.assertEqual(report["failure_diagnostics"]["browser_stage"], "create_preview")
        self.assertEqual(report["failure_diagnostics"]["go_stages"][0]["stages"], ["wait_created"])
        self.assertEqual(report["failure_diagnostics"]["ui_flags"], {"context": True})
        self.assertNotIn("SECRET", json.dumps(report))

    def test_exact_sets_match_actual_sources(self):
        browser = (ROOT / "scripts/agriculture/parcel-browser-e2e.mjs").read_text()
        checks = browser.split("export const REQUIRED_CHECKS = [", 1)[1].split("];", 1)[0]
        self.assertEqual(set(re.findall(r"'([a-z0-9_]+)'", checks)), gate.CHECKS)
        source = (ROOT / "backend/internal/agriculture/parcel_http_chain_test.go").read_text()
        self.assertEqual(set(re.findall(r't.Run\("([a-z0-9_]+)"', source)), gate.HTTP_CHILDREN)

    def test_ci_runs_real_runtime_race_and_publishes_only_summary(self):
        shell = (ROOT / "scripts/agriculture/identity-postgres-ci.sh").read_text()
        self.assertIn("export AGRI_PARCEL_CHAIN_REQUIRED=1", shell)
        self.assertIn("export AGRI_FARM_AUTH_COMPONENT_ONLY=0", shell)
        self.assertIn("test -race ./internal/agriculture -run '^TestParcel(HTTP|Browser)PostgresChain$'", shell)
        self.assertIn("[[ $parcels_code == 0 && $parcels_gate == 0 ]]", shell)
        self.assertIn("--output .local/evidence/identity-parcels.json", shell)
        workflow = (ROOT / ".github/workflows/agriculture-identity.yml").read_text()
        self.assertIn("node --test scaffold/scripts/agriculture/parcel-browser-e2e.test.mjs", workflow)
        self.assertNotIn("product/.local/evidence/parcel-chain-browser.json", workflow)

    def test_cli_missing_input_writes_safe_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = [sys.executable, str(Path(__file__).with_name("parcel-ci-result.py"))]
            for key in ("input", "browser", "bootstrap", "mode"):
                args += ["--"+key, str(root / "SECRET")]
            output = root / "out.json"
            result = subprocess.run(args+["--output", str(output)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertFalse(json.loads(output.read_text())["passed"])
            self.assertNotIn("SECRET", result.stdout+result.stderr+output.read_text())


if __name__ == "__main__":
    unittest.main()
