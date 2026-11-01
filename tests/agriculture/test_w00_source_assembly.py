"""W00 source closure, not a replacement for real Docker migration evidence."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("prepare", ROOT / "scripts/agriculture/session-ci-prepare.py")
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)

REQUIRED = {
    "scripts/agriculture/ci-smoke.sh",
    "scripts/agriculture/geo-worker-role-test.sh",
    "deploy/agriculture/health/Dockerfile",
    "deploy/agriculture/health/health.py",
    "deploy/agriculture/postgres/init.sh",
    "deploy/agriculture/loom-migrate.mjs",
    "docker-compose.agriculture.yml",
    "services/loom-engine/flow/tools/migrate.ts",
    "services/loom-engine/flow/db.ts",
    "services/loom-engine/flow/install-system-role.sql",
    "services/loom-engine/flow/migrations/0019_scope_required.sql",
    "backend/internal/database/migration_flow_scope.go",
    "backend/internal/database/migration_flow_scanner.go",
}


class W00SourceAssemblyTests(unittest.TestCase):
    def test_workflow_uses_shared_fail_closed_assembly(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/agriculture-w00.yml").read_text())
        steps = workflow["jobs"]["compose"]["steps"]
        step = next(step for step in steps if step.get("name") == "Explicit scaffold bootstrap or existing full product checkout")
        source = step["run"]
        self.assertIn('mode=full', source)
        self.assertIn('if [[ "$SCAFFOLD_MODE" == true ]]; then mode=scaffold; fi', source)
        self.assertIn('python3 scaffold/scripts/agriculture/session-ci-prepare.py --scaffold scaffold --destination product --mode "$mode"', source)
        for unsafe in ("git fetch", "git init", "cp -R", "mv scaffold", "|| true"):
            self.assertNotIn(unsafe, source)
        self.assertEqual(workflow["permissions"], {"contents": "read"})
        self.assertEqual(workflow["jobs"]["compose"]["strategy"]["matrix"]["mode"], ["core", "migrations"])
        artifact = next(step for step in steps if step.get("uses", "").startswith("actions/upload-artifact@"))
        self.assertEqual(artifact["with"]["path"], "product/.local/evidence/session-ci-provenance.json")
        self.assertEqual(artifact["with"]["if-no-files-found"], "error")

    def test_current_migration_dependencies_are_explicit(self):
        self.assertTrue(REQUIRED <= set(prepare.OVERLAY))
        for path in REQUIRED:
            self.assertTrue((ROOT / path).is_file(), path)
        script = (ROOT / "scripts/agriculture/ci-smoke.sh").read_text()
        for assertion in ('for service in platform-migrate loom-migrate',
                          'run --rm --no-deps "$service"',
                          'platform-migrate migrate-schema verify',
                          "count(*) > 0 FROM flow_schema_migrations"):
            self.assertIn(assertion, script)

    def test_missing_loom_entrypoint_cannot_use_baseline_silently(self):
        with tempfile.TemporaryDirectory() as temporary:
            source, destination = Path(temporary) / "source", Path(temporary) / "product"
            source.mkdir()
            destination.mkdir()
            missing = "services/loom-engine/flow/tools/migrate.ts"
            for path in prepare.OVERLAY:
                if path == missing:
                    continue
                target = source / path
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_text("reviewed fixture")
            # Even if upstream has a stale version, missing reviewed input fails.
            target = destination / missing
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("stale upstream")
            with self.assertRaisesRegex(ValueError, "Missing or unsafe explicitly allowed overlay"):
                prepare.overlay(source, destination)


if __name__ == "__main__":
    unittest.main()
