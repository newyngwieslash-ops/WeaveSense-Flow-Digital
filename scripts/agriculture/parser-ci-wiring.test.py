"""Local wiring checks only; these never substitute for actual Docker acceptance."""
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("prepare", Path(__file__).with_name("session-ci-prepare.py"))
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


class ParserWiringTests(unittest.TestCase):
    def test_exact_ingestion_dependency_closure_is_allowlisted(self):
        modules = {"__init__", "source_policy", "download", "limits", "runner", "worker", "archive"}
        tests = {"ingestion_limit_probe", "test_agriculture_ingestion_docker", "test_agriculture_ingestion_download",
                 "test_agriculture_ingestion_limits", "test_agriculture_ingestion_parser"}
        required = {f"services/agri-gis/backend/app/agriculture/ingestion/{name}.py" for name in modules}
        required |= {f"services/agri-gis/backend/tests/{name}.py" for name in tests}
        required |= {"deploy/agriculture/Dockerfile.parser", "scripts/agriculture/parser-docker-ci.sh",
                     "docs/agriculture/INGESTION_SECURITY.md", "scripts/agriculture/parser-ci-wiring.test.py",
                     "scripts/agriculture/parser-ci-result.py", "scripts/agriculture/parser-ci-result.test.py",
                     "scripts/agriculture/export-python-lock.py",
                     "scripts/agriculture/python-test-requirements.txt", "services/agri-gis/backend/poetry.lock",
                     "services/agri-gis/backend/pyproject.toml"}
        self.assertTrue(required <= set(prepare.OVERLAY))
        self.assertEqual(len(prepare.OVERLAY), len(set(prepare.OVERLAY)))
        for path in required:
            self.assertTrue((ROOT / path).is_file(), path)

    def test_missing_parser_source_fails_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            source, destination = Path(tmp) / "source", Path(tmp) / "destination"
            source.mkdir()
            with patch.object(prepare, "OVERLAY", ("deploy/agriculture/Dockerfile.parser",)):
                with self.assertRaisesRegex(ValueError, "Missing or unsafe"):
                    prepare.overlay(source, destination)

    def test_workflow_has_no_privileged_write_or_skip_path(self):
        workflow = (ROOT / ".github/workflows/agriculture-parser.yml").read_text()
        for value in ("contents: read", "persist-credentials: false", "python-version: '3.12'",
                      "version: '0.13.4'", "--mode \"$mode\"", "--require-hashes --no-deps",
                      "--group main", "--group dev", "uv pip check",
                      'export AGRI_IDENTITY_PYTHON="$PWD/.local/identity-rebuild/venv/bin/python"',
                      "bash scripts/agriculture/parser-docker-ci.sh", "re.fullmatch('[0-9a-f]{40}', source)",
                      "AGRI_E2E_SOURCE_COMMIT={source}"):
            self.assertIn(value, workflow)
        for forbidden in ("contents: write", "pull_request_target", "privileged:", "--privileged",
                          "docker push", "secrets.", "continue-on-error", "|| true", "sudo "):
            self.assertNotIn(forbidden, workflow)
        actual = workflow.split("name: Actual isolated Docker parser acceptance", 1)[1].split("- uses:", 1)[0]
        self.assertIn("set -euo pipefail", actual)
        self.assertNotIn("if:", actual)

    def test_artifacts_are_explicit_sanitized_json_only(self):
        workflow = (ROOT / ".github/workflows/agriculture-parser.yml").read_text()
        paths = workflow.split("          path: |\n", 1)[1].split("          include-hidden-files:", 1)[0]
        self.assertEqual(paths.split(), ["product/.local/evidence/parser-docker.json",
                                       "product/.local/evidence/session-ci-provenance.json"])
        identity = (ROOT / ".github/workflows/agriculture-identity.yml").read_text()
        self.assertIn("product/.local/evidence/scope-stream-*.json", identity)


if __name__ == "__main__":
    unittest.main()
