"""CI wiring contracts, not substitutes for real PostgreSQL route acceptance."""
import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("prepare", ROOT / "scripts/agriculture/session-ci-prepare.py")
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


class IsolationWiringTests(unittest.TestCase):
    def test_all_new_test_inputs_are_in_explicit_assembly(self):
        required = {
            "backend/internal/agriculture/isolation_matrix_postgres_test.go",
            "backend/internal/agriculture/isolation_object_session_test.go",
            "backend/internal/agriculture/isolation_projection_postgres_test.go",
            "services/agri-gis/backend/tests/test_agriculture_isolation_go_postgres.py",
            "services/agri-gis/backend/tests/test_agriculture_isolation_matrix_postgres.py",
            "scripts/agriculture/isolation-matrix-result.py",
            "scripts/agriculture/isolation-matrix-result.test.py",
            "tests/agriculture/test_isolation_ci_wiring.py",
            "contracts/agriculture/isolation-matrix.json",
        }
        self.assertTrue(required <= set(prepare.OVERLAY))
        self.assertEqual(len(prepare.OVERLAY), len(set(prepare.OVERLAY)))
        for path in required:
            self.assertTrue((ROOT / path).is_file(), path)

    def test_ci_disallows_component_mode_and_requires_both_exit_codes(self):
        script = (ROOT / "scripts/agriculture/identity-postgres-ci.sh").read_text()
        self.assertIn("export AGRI_ISOLATION_AUTH_COMPONENT_ONLY=0", script)
        self.assertIn("export AGRI_ISOLATION_REQUIRED=1", script)
        self.assertIn("export AGRI_ISOLATION_GO_RACE=1", script)
        self.assertIn("test_agriculture_isolation_matrix_postgres.py tests/test_agriculture_isolation_go_postgres.py", script)
        self.assertIn("[[ $isolation_code == 0 && $isolation_gate == 0 ]]", script)
        self.assertIn("isolation-matrix-result.py --input", script)

    def test_only_sanitized_reports_uploaded(self):
        workflow = (ROOT / ".github/workflows/agriculture-identity.yml").read_text()
        self.assertIn("contents: read", workflow)
        self.assertIn("persist-credentials: false", workflow)
        self.assertIn("python3 scaffold/tests/agriculture/test_isolation_ci_wiring.py", workflow)
        paths = workflow.split("          path: |\n", 1)[1].split("          include-hidden-files:", 1)[0]
        for path in ("isolation-go-*.json", "isolation-routes-go.json"):
            self.assertIn(path, paths)
        for forbidden in ("private.log", "go.jsonl", "isolation.xml", "isolation-private-runs", "**/*"):
            self.assertNotIn(forbidden, paths)


if __name__ == "__main__":
    unittest.main()
