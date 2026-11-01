import copy
import importlib.util
import json
from pathlib import Path
import unittest
import xml.etree.ElementTree as ET

spec = importlib.util.spec_from_file_location("isolation_result", Path(__file__).with_name("isolation-matrix-result.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class IsolationGateTests(unittest.TestCase):
    def setUp(self):
        self.xml = ET.Element("testsuite")
        for cls, name in sorted(module.REQUIRED):
            ET.SubElement(self.xml, "testcase", classname=cls, name=name)
        self.reports = {suite: {"passed": True, "exit_code": 0, "auth_component_only": False,
                               "race_enabled": True,
                               "missing_required": [], "tests": {name: "pass" for name in module.GO_EXPECTED[suite]}}
                        for suite in module.GO_ROOTS}
        self.reports["routes"] = {"passed": True, "auth_component_only": False,
            "exact_routes": sorted(module.EXPECTED_ROUTES), "observations": [
                {"route": route, "case": case, "status": status, "business_snapshot_unchanged": True}
                for (route, case), status in sorted(module.EXPECTED_OBSERVATIONS.items())]}

    def reduce(self):
        return module.reduce(ET.tostring(self.xml, encoding="unicode"), self.reports)

    def test_exact_156_cases_required(self):
        self.assertEqual(len(module.REQUIRED), 156)
        self.assertTrue(self.reduce()["passed"])
        self.xml.remove(self.xml[0])
        self.assertFalse(self.reduce()["passed"])

    def test_race_evidence_required(self):
        for suite in module.GO_ROOTS:
            self.reports[suite]["race_enabled"] = False
            result = self.reduce()
            self.assertFalse(result["passed"])
            self.assertFalse(result["race_enabled"])
            self.reports[suite]["race_enabled"] = True

    def test_duplicate_unknown_skip_and_error_cannot_pass_or_leak(self):
        for tag in ("skipped", "failure", "error"):
            case = self.xml[0]
            node = ET.SubElement(case, tag)
            node.text = "SECRET CREDENTIAL"
            result = self.reduce()
            self.assertFalse(result["passed"])
            self.assertNotIn("SECRET", json.dumps(result))
            case.remove(node)
        self.xml.append(copy.deepcopy(self.xml[0]))
        self.assertFalse(self.reduce()["passed"])
        self.xml[-1].set("name", "SECRET")
        result = self.reduce()
        self.assertFalse(result["passed"])
        self.assertNotIn("SECRET", json.dumps(result))

    def test_component_only_changed_snapshot_or_missing_route_rejected(self):
        for key in module.GO_ROOTS:
            self.reports[key]["auth_component_only"] = True
            self.assertFalse(self.reduce()["passed"])
            self.reports[key]["auth_component_only"] = False
        self.reports["routes"]["observations"][0]["business_snapshot_unchanged"] = False
        self.assertFalse(self.reduce()["passed"])
        self.reports["routes"]["observations"][0]["business_snapshot_unchanged"] = True
        self.reports["routes"]["exact_routes"].pop()
        self.assertFalse(self.reduce()["passed"])


    def test_every_go_root_and_child_is_independently_required(self):
        for suite, expected in module.GO_EXPECTED.items():
            for name in expected:
                with self.subTest(suite=suite, missing=name):
                    del self.reports[suite]["tests"][name]
                    # A forged optimistic bridge summary must not hide a gap.
                    self.assertTrue(self.reports[suite]["passed"])
                    self.assertEqual(self.reports[suite]["missing_required"], [])
                    self.assertFalse(self.reduce()["passed"])
                    self.reports[suite]["tests"][name] = "pass"
            self.reports[suite]["tests"]["SECRET_EXTRA_TEST"] = "pass"
            result = self.reduce()
            self.assertFalse(result["passed"])
            self.assertNotIn("SECRET", json.dumps(result))
            del self.reports[suite]["tests"]["SECRET_EXTRA_TEST"]

    def test_every_request_case_and_expected_status_is_required(self):
        observations = self.reports["routes"]["observations"]
        for index in range(len(observations)):
            row = observations.pop(index)
            self.assertFalse(self.reduce()["passed"])
            observations.insert(index, row)
            original = row["status"]
            row["status"] = 200 if original != 200 else 403
            self.assertFalse(self.reduce()["passed"])
            row["status"] = original
        observations.append(copy.deepcopy(observations[0]))
        self.assertFalse(self.reduce()["passed"])
        observations.pop()
        observations[0]["case"] = "SECRET_NEW_CASE"
        result = self.reduce()
        self.assertFalse(result["passed"])
        self.assertNotIn("SECRET", json.dumps(result))

    def test_route_string_replacement_and_duplicate_rejected_at_same_count(self):
        routes = self.reports["routes"]["exact_routes"]
        original = routes[0]
        for replacement in (routes[1], "GET /api/SECRET-unreviewed"):
            routes[0] = replacement
            result = self.reduce()
            self.assertFalse(result["passed"])
            self.assertNotIn("SECRET", json.dumps(result))
        routes[0] = original
        self.assertTrue(self.reduce()["passed"])

    def test_booleans_missing_snapshots_and_false_pass_summaries_rejected(self):
        row = self.reports["routes"]["observations"][0]
        for value in (None, False, 1, "true"):
            row["business_snapshot_unchanged"] = value
            self.assertFalse(self.reduce()["passed"])
        row["business_snapshot_unchanged"] = True
        self.reports["matrix"]["exit_code"] = False
        self.assertFalse(self.reduce()["passed"])
        self.reports["matrix"]["exit_code"] = 0
        name = next(iter(module.GO_EXPECTED["matrix"]))
        for value in ("fail", "skip", True):
            self.reports["matrix"]["tests"][name] = value
            self.assertFalse(self.reduce()["passed"])

    def test_json_duplicate_keys_rejected_before_information_is_lost(self):
        for raw in ('{"passed":false,"passed":true}',
                    '{"tests":{"root":"fail","root":"pass"}}',
                    '{"observations":[{"status":403,"status":200}]}'):
            with self.assertRaises(ValueError):
                module.strict_json(raw)

    def test_contract_counts_and_positive_control_snapshot(self):
        self.assertEqual(len(module.GO_EXPECTED["matrix"]), 260)
        self.assertEqual(len(module.EXPECTED_OBSERVATIONS), 229)
        self.assertEqual(len(module.EXPECTED_ROUTES), 30)
        row = next(row for row in self.reports["routes"]["observations"] if row["status"] == 200)
        row["business_snapshot_unchanged"] = False
        self.assertFalse(self.reduce()["passed"])


if __name__ == "__main__":
    unittest.main()
