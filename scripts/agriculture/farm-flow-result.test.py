import importlib.util
import json
from pathlib import Path
import re
import unittest

spec = importlib.util.spec_from_file_location("farm_flow", Path(__file__).with_name("farm-flow-result.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)
ROOT = Path(__file__).resolve().parents[2]


class FarmFlowGateTests(unittest.TestCase):
    def events(self):
        return [{"Action": a, "Test": n, "Package": gate.PACKAGE}
                for n in sorted(gate.REQUIRED) for a in ("run", "pass")] + [{"Action": "pass", "Package": gate.PACKAGE}]

    def report(self, events):
        return gate.reduce("\n".join(map(json.dumps, events)))

    def test_exact_real_adapter_and_legacy_suite(self):
        self.assertTrue(self.report(self.events())["passed"])
        self.assertEqual(self.report(self.events())["passed_count"], 10)
        for i in range(len(self.events())):
            events = self.events()
            events.pop(i)
            self.assertFalse(self.report(events)["passed"], i)

    def test_skip_failure_duplicate_and_wrong_package_are_rejected(self):
        for action in ("skip", "fail", "pass", "run"):
            events = self.events() + [{"Action": action, "Test": "SECRET", "Package": gate.PACKAGE}]
            report = self.report(events)
            self.assertFalse(report["passed"])
            self.assertNotIn("SECRET", json.dumps(report))
        self.assertFalse(self.report(self.events() + [self.events()[0]])["passed"])
        events = self.events()
        events[-1]["Package"] = "wrong"
        self.assertFalse(self.report(events)["passed"])

    def test_tests_match_existing_and_new_source(self):
        for root, filename in (("TestCommandAdapterPostgres", "command_adapter_postgres_test.go"),
                               ("TestFlowPostgresWorkerShortTransactions", "worker_scope_postgres_test.go")):
            source = (ROOT / "backend/internal/flow" / filename).read_text()
            self.assertIn("func " + root + "(", source)
            self.assertEqual(set(re.findall(r't.Run\("([a-z0-9_]+)"', source)), gate.CHILDREN[root])

    def test_ci_uses_system_role_and_race_without_raw_artifacts(self):
        source = (ROOT / "scripts/agriculture/identity-postgres-ci.sh").read_text()
        self.assertIn('export AGRI_TEST_PG_SYSTEM_DSN="postgres://flow_platform_system:', source)
        self.assertIn("test -race ./internal/flow -run '^Test(CommandAdapterPostgres|FlowPostgresWorkerShortTransactions)$'", source)
        self.assertIn("[[ $farm_flow_code == 0 && $farm_flow_gate == 0 ]]", source)
        self.assertIn("--output .local/evidence/identity-farm-flow.json", source)


if __name__ == "__main__":
    unittest.main()
