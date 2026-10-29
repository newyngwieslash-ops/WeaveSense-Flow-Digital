import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("result", Path(__file__).with_name("membership-ci-result.py"))
result = importlib.util.module_from_spec(spec)
spec.loader.exec_module(result)


def event(action, package, test=None, **extra):
    return json.dumps({"Action": action, "Package": "yingce/backend/internal/" + package, **({"Test": test} if test else {}), **extra})


def postgres_pass():
    return [
        event("pass", "database", "TestAgriculturePostgresAdmissionAndStrictRLS"),
        event("pass", "database"),
        event("pass", "agriculture", "TestAgriculturePostgresMembershipLifecycle"),
        event("pass", "agriculture", "TestAgriculturePostgresConcurrentAcceptance"),
        event("pass", "agriculture"),
    ]


class EvidenceTests(unittest.TestCase):
    def test_accepts_required_actual_test_passes(self):
        evidence = result.verify(postgres_pass(), "postgres")
        self.assertTrue(evidence["passed"])
        self.assertEqual(len(evidence["tests_passed"]), 3)
        self.assertFalse(evidence["live_farm_or_production_acceptance"])

    def test_skip_is_not_acceptance_even_when_package_passes(self):
        events = postgres_pass()
        events[0] = event("skip", "database", "TestAgriculturePostgresAdmissionAndStrictRLS")
        self.assertFalse(result.verify(events, "postgres")["passed"])

    def test_zero_tests_and_missing_service_test_fail(self):
        for events in [[], postgres_pass()[:2], postgres_pass()[1:], [event("pass", "database"), event("pass", "agriculture")], [item for item in postgres_pass() if "ConcurrentAcceptance" not in item]]:
            self.assertFalse(result.verify(events, "postgres")["passed"])

    def test_fail_or_missing_package_terminal_fails(self):
        self.assertFalse(result.verify(postgres_pass() + [event("fail", "agriculture")], "postgres")["passed"])
        self.assertFalse(result.verify(postgres_pass()[:-1], "postgres")["passed"])

    def test_raw_output_and_credentials_are_not_in_evidence(self):
        events = postgres_pass() + [event("output", "database", Output="postgres://secret fixture@example.invalid token=never-report")]
        encoded = json.dumps(result.verify(events, "postgres"))
        for value in ["postgres://", "secret", "fixture@example.invalid", "never-report"]:
            self.assertNotIn(value, encoded)

    def test_failed_assertion_retains_coordinates_and_sqlstate_not_message(self):
        events = postgres_pass() + [
            event("output", "database", "TestAgriculturePostgresAdmissionAndStrictRLS", Output="    migration_agriculture_postgres_test.go:123: fixture@example.invalid postgres://secret TOKEN SQLSTATE 42501"),
            event("fail", "database", "TestAgriculturePostgresAdmissionAndStrictRLS"),
        ]
        evidence = result.verify(events, "postgres")
        self.assertEqual(evidence["failure_locations"][0]["locations"], [{"file": "migration_agriculture_postgres_test.go", "line": 123, "sqlstates": ["42501"]}])
        for value in ["example.invalid", "secret", "TOKEN", "postgres://"]:
            self.assertNotIn(value, json.dumps(evidence))

    def test_fixture_stage_is_allowlisted_not_arbitrary_output(self):
        events = postgres_pass() + [
            event("output", "database", "TestAgriculturePostgresAdmissionAndStrictRLS", Output="    migration_agriculture_postgres_test.go:145: fixture_stage=flow_invites.product fixture_stage=secret.password SQLSTATE 42501"),
            event("fail", "database", "TestAgriculturePostgresAdmissionAndStrictRLS"),
        ]
        evidence = result.verify(events, "postgres")
        self.assertEqual(evidence["failure_locations"][0]["locations"][0]["fixture_stages"], ["flow_invites.product"])
        self.assertNotIn("secret", json.dumps(evidence))

    def test_legacy_requires_old_and_new_regressions(self):
        events = [event("pass", "flow", name) for name in ["TestOrgInviteSingleUseAndGrantRevocation", "TestP7MembersRoleEditing", "TestAgricultureMembershipOwnerRemovalPreservesWorkspace"]] + [event("pass", "flow")]
        self.assertTrue(result.verify(events, "legacy-flow")["passed"])
        self.assertFalse(result.verify(events[1:], "legacy-flow")["passed"])

    def test_invalid_json_is_not_silently_accepted(self):
        with self.assertRaises(ValueError):
            result.verify(["not JSON"], "postgres")


if __name__ == "__main__":
    unittest.main()
