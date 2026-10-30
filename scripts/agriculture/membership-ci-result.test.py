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
        event("pass", "database", "TestAgriculturePostgresAuthorizationBindingsAndEpoch"),
        event("pass", "database"),
        event("pass", "agriculture", "TestAgriculturePostgresMembershipLifecycle"),
        event("pass", "agriculture", "TestAgriculturePostgresConcurrentAcceptance"),
        event("pass", "agriculture", "TestAgriculturePostgresResourceAuthorization"),
        event("pass", "agriculture", "TestAgriculturePostgresRoleDowngradeRevokesPendingDelegation"),
        event("pass", "agriculture"),
    ]


class EvidenceTests(unittest.TestCase):
    def test_new_fixed_suites_require_each_case_and_no_skip(self):
        suites = {
            "flow-admission": ("flow", "TestFlowPostgresCallbackAndLegacyAdmission", ["first_personal_admission_and_disabled_no_resurrection"]),
            "flow-workers": ("flow", "TestFlowPostgresWorkerShortTransactions", ["missing_scanner_fails_closed", "model_gateway_and_notification_outside_transaction", "model_reconciliation_http_outside_transaction", "command_http_outside_transaction_and_retry_receipt_commits"]),
            "flow-scanner": ("flow", "TestFlowPostgresScannerAndRuntimeAdmission", ["fixed_scanner_no_business_access", "privilege_drift_rejected_and_restored", "bounded_keyset_reaches_all_tenants"]),
            "flow-continuations": ("flow", "TestFlowPostgresWaitAndNotificationScopes", ["wait_continuation_outside_short_transaction", "notifications_outside_short_transaction"]),
            "edu-artifacts": ("edumath", "TestEduPostgresSignedArtifactAdmission", ["active_nonmember_student", "signature_negatives_no_http", "revoked_actor_or_role_no_http", "pooled_scope_and_cross_school"]),
            "edu-onboarding": ("edumath", "TestEduPostgresOnboardingAdmission", ["class_invite_hash_and_atomic_limit", "roster_claim_single_use", "disabled_or_revoked_actor_denied", "metadata_scope_and_no_platform_grants"]),
        }
        for suite, (package, root, children) in suites.items():
            events = [event("pass", package, name) for name in [root] + [root + "/" + child for child in children]] + [event("pass", package)]
            self.assertTrue(result.verify(events, suite)["passed"], suite)
            for index in range(len(events)):
                self.assertFalse(result.verify(events[:index] + events[index+1:], suite)["passed"], suite)
            self.assertFalse(result.verify(events + [event("skip", package, root + "/extra")], suite)["passed"], suite)

    def test_flow_scope_requires_real_root_and_every_child(self):
        root = "TestFlowPostgresStrictScopeAndProviderAtomicity"
        names = [root] + [root + "/" + child for child in ("empty_scope_and_pool_reuse", "provider_and_wallet_rollback_together", "provider_state_idempotency_and_quota", "unknown_provider_and_cross_tenant", "child_release_and_suspended_provider_preserve_legacy_math")]
        events = [event("pass", "flow", name) for name in names] + [event("pass", "flow")]
        self.assertTrue(result.verify(events, "flow-scope")["passed"])
        for index in range(len(events)):
            self.assertFalse(result.verify(events[:index] + events[index + 1:], "flow-scope")["passed"])
        self.assertFalse(result.verify(events + [event("skip", "flow", root + "/optional")], "flow-scope")["passed"])

    def test_accepts_required_actual_test_passes(self):
        evidence = result.verify(postgres_pass(), "postgres")
        self.assertTrue(evidence["passed"])
        self.assertEqual(len(evidence["tests_passed"]), 6)
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

    def test_both_resource_authorization_tests_are_required(self):
        for name in ("TestAgriculturePostgresAuthorizationBindingsAndEpoch", "TestAgriculturePostgresResourceAuthorization"):
            self.assertFalse(result.verify([line for line in postgres_pass() if name not in line], "postgres")["passed"])

    def test_pending_delegation_revocation_must_pass_not_skip_or_disappear(self):
        name = "TestAgriculturePostgresRoleDowngradeRevokesPendingDelegation"
        missing = [line for line in postgres_pass() if name not in line]
        self.assertFalse(result.verify(missing, "postgres")["passed"])
        self.assertFalse(result.verify(missing + [event("skip", "agriculture", name)], "postgres")["passed"])

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
