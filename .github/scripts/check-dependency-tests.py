"""Require named dependency-backed Go tests to run and pass (not merely avoid SKIP)."""

import json
import sys

REQUIRED = {
    ("database", "TestPostgresAssetIDMigration"),
    ("canvas", "TestRepairAssetBytesPostgres"),
    ("repository", "TestCanvasRevision/postgres"),
    ("platform", "TestRuntimeCoordinatorRedisLeasePreservesLongestTTL"),
    ("app", "TestClearAdminRuntimeCacheUsesRedisWhitelist"),
    # Platform-side PostgreSQL RLS (non-superuser role, FLOW_TEST_PG_RLS_DSN).
    ("database", "TestFlowRLSPostgres"),
    ("flow", "TestRuntimeRLSBlocksUnfilteredHandlerQueries"),
    ("flow", "TestBackgroundWorkIsTenantScoped"),
    # Education math workbench: real math-service chain + edu tables under FORCE RLS.
    ("edumath", "TestG1SolidAngle02EndToEnd"),
    ("edumath", "TestSixFamiliesThroughPlatform"),
    ("edumath", "TestStudentLearningLoop"),
    ("edumath", "TestHelpRequestsAndDashboard"),
    ("edumath", "TestFigurePredictionsAndUse"),
    ("edumath", "TestVariantSetsAndExtensions"),
    ("edumath", "TestLessonEdits"),
    ("edumath", "TestWorkerRecoveryAndReconcile"),
    ("edumath", "TestPGEduTablesForceRLS"),
    ("edumath", "TestPGG1Chain"),
    ("edumath", "TestPGStudentLearningLoop"),
    ("edumath", "TestPGHelpRequestsAndDashboard"),
    ("edumath", "TestPGFigurePredictionsAndUse"),
    ("edumath", "TestPGVariantSetsAndExtensions"),
    ("edumath", "TestPGLessonEdits"),
    ("edumath", "TestMCPProtocolWithGoSDK"),
    ("edumath", "TestClassesInvitesConsentAuthorization"),
    ("edumath", "TestAgentMathTools"),
    ("edumath", "TestStageSettlementOutcomes"),
    ("edumath", "TestPGAgentMathTools"),
}


def missing_tests(events):
    ran, passed, failed = set(), set(), set()
    for event in events:
        key = (event.get("Package", "").rsplit("/", 1)[-1], event.get("Test"))
        action = event.get("Action")
        if action == "run":
            ran.add(key)
        elif action == "pass":
            passed.add(key)
        elif action in {"skip", "fail"}:
            failed.add(key)
    return REQUIRED - ((ran & passed) - failed)


if __name__ == "__main__":
    with open(sys.argv[1], encoding="utf-8") as stream:
        missing = missing_tests(json.loads(line) for line in stream if line.strip())
    if missing:
        sys.exit("Dependency tests missing, skipped or failed: " + repr(sorted(missing)))
    print("All dependency-backed tests ran and passed.")
