import copy
import importlib.util
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("farm_result", Path(__file__).with_name("farm-ci-result.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
ROOT = Path(__file__).resolve().parents[2]


class FarmGateTests(unittest.TestCase):
    def setUp(self):
        self.events = []
        for name in sorted(gate.REQUIRED):
            self.events.extend({"Action": action, "Test": name, "Package": gate.PACKAGE} for action in ("run", "pass"))
        self.events.append({"Action": "pass", "Package": gate.PACKAGE})
        self.browser = {"result": "pass", "stage": "complete", "data_class": "fixture", "production_acceptance": False,
                        "results": [{"name": n, "result": "pass"} for n in sorted(gate.BROWSER_CHECKS)]}
        self.browser.update(schema_version="1.0", source_commit="a" * 40,
                            artifact_hashes={k: "b" * 64 for k in ("nginx_template", "react_index", "vue_index", "runner")},
                            network={"observed": 10, "violations": 0, "previews": 4, "creates": 2, "updates": 2, "archives": 1})
        self.bootstrap = {key: True for key in ("redis_authenticated_ping", "fresh_volume", "runtime_not_owner_checked", "cross_database_denial_checked")}
        self.mode = {"auth_component_only": False, "race": True, "required": True}

    def report(self, events=None, **kwargs):
        return gate.reduce("\n".join(map(json.dumps, self.events if events is None else events)),
                           kwargs.get("browser", self.browser), kwargs.get("bootstrap", self.bootstrap), kwargs.get("mode", self.mode))

    def test_all_real_chain_requirements(self):
        result = self.report()
        self.assertTrue(result["passed"])
        self.assertEqual(result["passed_count"], 10)
        self.assertEqual(len(result["browser_checks"]), 23)

    def test_every_required_go_run_and_terminal_and_package_are_required(self):
        for index in range(len(self.events)):
            self.assertFalse(self.report(self.events[:index] + self.events[index + 1:])["passed"], index)

    def test_duplicate_failed_skipped_and_unknown_tests_fail(self):
        for event in (self.events[0], self.events[1], self.events[-1],
                      {"Action": "skip", "Test": "SECRET", "Package": gate.PACKAGE},
                      {"Action": "fail", "Package": gate.PACKAGE},
                      {"Action": "build-fail", "Package": gate.PACKAGE},
                      {"Action": "run", "Test": "SECRET", "Package": gate.PACKAGE},
                      {"Action": "pass", "Test": "SECRET", "Package": gate.PACKAGE}):
            report = self.report(self.events + [event])
            self.assertFalse(report["passed"])
            self.assertNotIn("SECRET", json.dumps(report))

    def test_wrong_package_cannot_satisfy_go_gate(self):
        for index in range(len(self.events)):
            events = copy.deepcopy(self.events)
            events[index]["Package"] = "other/package"
            self.assertFalse(self.report(events)["passed"])

    def test_browser_exact_checks_required_no_duplicates_or_skips(self):
        for index in range(len(self.browser["results"])):
            for change in ("remove", "skip", "fail", "duplicate"):
                browser = copy.deepcopy(self.browser)
                if change == "remove":
                    browser["results"].pop(index)
                elif change == "duplicate":
                    browser["results"].append(browser["results"][index])
                else:
                    browser["results"][index]["result"] = change
                self.assertFalse(self.report(browser=browser)["passed"])

    def test_runtime_requires_actual_bootstrap_and_exact_boolean_mode(self):
        for key in self.bootstrap:
            for value in (False, 1, "true", None):
                self.assertFalse(self.report(bootstrap={**self.bootstrap, key: value})["passed"])
        for key in self.mode:
            for value in (not self.mode[key], int(self.mode[key]), "false", None):
                self.assertFalse(self.report(mode={**self.mode, key: value})["passed"])

    def test_malformed_go_reports_and_secret_output_are_closed(self):
        valid = "\n".join(map(json.dumps, self.events))
        for suffix in ('\n[]', '\nnull', '\n{"Test":[]}', '\n{"Action":"pass","Action":"pass"}', '\n{"Action":NaN}'):
            self.assertFalse(gate.reduce(valid + suffix, self.browser, self.bootstrap, self.mode)["passed"])
        report = self.report(self.events + [{"Action": "output", "Output": "postgres://SECRET Bearer SECRET", "Package": gate.PACKAGE}])
        self.assertTrue(report["passed"])
        self.assertNotIn("SECRET", json.dumps(report))

    def test_browser_unknown_fields_never_escape_summary(self):
        browser = {**self.browser, "token": "SECRET"}
        report = self.report(browser=browser)
        self.assertTrue(report["passed"])
        self.assertNotIn("SECRET", json.dumps(report))
        for key in ("result", "stage", "data_class", "production_acceptance", "results"):
            self.assertFalse(self.report(browser={**self.browser, key: None})["passed"])

    def test_browser_evidence_is_required_and_bounded_without_leaks(self):
        for key in ("schema_version", "source_commit", "artifact_hashes", "network"):
            self.assertFalse(self.report(browser={**self.browser, key: None})["passed"])
        for path, value in (("observed", 0), ("violations", 1), ("creates", -1), ("updates", True), ("archives", 10**10)):
            browser = copy.deepcopy(self.browser)
            browser["network"][path] = value
            self.assertFalse(self.report(browser=browser)["passed"])
        for key in self.browser["artifact_hashes"]:
            browser = copy.deepcopy(self.browser)
            browser["artifact_hashes"][key] = "SECRET"
            report = self.report(browser=browser)
            self.assertFalse(report["passed"])
            self.assertNotIn("SECRET", json.dumps(report))
        self.assertFalse(self.report(browser={**self.browser, "ci_provenance_sha256": "SECRET"})["passed"])
        report = self.report(browser={**self.browser, "ci_provenance_sha256": "c" * 64})
        self.assertTrue(report["passed"])
        self.assertEqual(report["browser_evidence"]["artifact_hashes"], self.browser["artifact_hashes"])

    def test_failure_coordinates_and_browser_stage_are_closed_and_not_pass(self):
        events = self.events.copy()
        events = [{**event, "Action": "fail"} if event.get("Test") == gate.BROWSER_ROOT and event["Action"] == "pass" else event for event in events]
        events.append({"Action": "output", "Package": gate.PACKAGE, "Test": gate.BROWSER_ROOT,
                       "Output": "/SECRET/farm_browser_chain_test.go:704: Bearer SECRET\nSECRET.go:123: bad\nidentity_browser_chain_test.go:53: hidden\nFARM_BROWSER_DIAGNOSTIC stage=browser_private_config\nFARM_BROWSER_DIAGNOSTIC stage=SECRET"})
        browser = {"result": "fail", "stage": "create_north_preview", "results": [],
                   "failure": "assertion failed: north_created_with_explicit_grants", "secret": "SECRET"}
        result = self.report(events, browser=browser)
        self.assertFalse(result["passed"])
        diagnostic = result["failure_diagnostics"]
        self.assertEqual(diagnostic["browser_stage"], "create_north_preview")
        self.assertEqual(diagnostic["browser_failure_code"], "assertion_failed")
        self.assertEqual(len(diagnostic["go_locations"][0]["locations"]), 2)
        self.assertEqual(diagnostic["go_stages"], [{"test": gate.BROWSER_ROOT, "stages": ["browser_private_config"]}])
        self.assertNotIn("SECRET", json.dumps(result))

    def test_unknown_failure_text_stage_and_location_cannot_escape(self):
        for stage in ("SECRET", [], None):
            browser = {"result": "fail", "stage": stage, "failure": "assertion failed: SECRET"}
            result = self.report(browser=browser)
            self.assertFalse(result["passed"])
            self.assertNotIn("SECRET", json.dumps(result))
            self.assertNotIn("browser_stage", result["failure_diagnostics"])
        diagnostic = gate.failure_diagnostics("{}\nnull\n{\"Test\":[]}", {}, {})
        self.assertEqual(diagnostic, {"go_locations": []})

    def test_fixed_sets_match_actual_go_and_browser_sources(self):
        source = (ROOT / "scripts/agriculture/farm-browser-e2e.mjs").read_text()
        checks = source.split("export const REQUIRED_CHECKS = [", 1)[1].split("];", 1)[0]
        self.assertEqual(set(re.findall(r"'([a-z0-9_]+)'", checks)), gate.BROWSER_CHECKS)
        source = (ROOT / "backend/internal/agriculture/farm_browser_chain_test.go").read_text()
        block = source.split("func TestFarmHTTPPostgresChain", 1)[1].split("func TestFarmBrowserPostgresChain", 1)[0]
        self.assertEqual(set(re.findall(r't.Run\("([a-z0-9_]+)"', block)), gate.HTTP_CHILDREN)

    def test_shell_requires_race_and_real_runtime_and_safe_artifact(self):
        source = (ROOT / "scripts/agriculture/identity-postgres-ci.sh").read_text()
        self.assertIn("export AGRI_FARM_CHAIN_REQUIRED=1", source)
        self.assertIn("export AGRI_FARM_AUTH_COMPONENT_ONLY=0", source)
        self.assertIn("test -race ./internal/agriculture -run '^TestFarm(HTTP|Browser)PostgresChain$'", source)
        self.assertIn("[[ $farms_code == 0 && $farms_gate == 0 ]]", source)
        self.assertIn("--output .local/evidence/identity-farms.json", source)
        workflow = (ROOT / ".github/workflows/agriculture-identity.yml").read_text()
        self.assertIn("product/.local/evidence/identity-*.json", workflow)
        self.assertNotIn("product/.local/evidence/farm-chain-browser.json", workflow)

    def test_cli_missing_inputs_fails_without_leaking_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            args = [sys.executable, str(Path(__file__).with_name("farm-ci-result.py"))]
            for key in ("input", "browser", "bootstrap", "mode"):
                args += ["--" + key, str(root / "SECRET")]
            output = root / "summary.json"
            result = subprocess.run(args + ["--output", str(output)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertFalse(json.loads(output.read_text())["passed"])
            self.assertNotIn("SECRET", output.read_text() + result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
