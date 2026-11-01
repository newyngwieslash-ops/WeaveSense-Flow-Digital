"""Deployment policy checks only, not an image or live GIS acceptance."""
import json
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[2]


class FarmDeploymentPolicyTests(unittest.TestCase):
    def test_dictionary_template_cannot_supply_default_regions(self):
        document = json.loads((ROOT / "deploy/agriculture/farm-dictionary.template.json").read_text())
        self.assertEqual(set(document), {"schema_version", "dictionary_version", "regions"})
        self.assertEqual(document["schema_version"], "agriculture-farm-dictionary/1")
        self.assertEqual(document["regions"], [])
        self.assertEqual(document["dictionary_version"], "REPLACE_WITH_APPROVED_VERSION")

    def test_linux_runtime_has_iana_database_and_real_import_smoke(self):
        source = (ROOT / "services/agri-gis/backend/Dockerfile").read_text()
        runtime = source.split("# ===== 第二阶段：Runtime =====", 1)[1]
        self.assertIn("fonts-wqy-zenhei tzdata", runtime)
        self.assertIn("ZoneInfo('UTC'); ZoneInfo('Asia/Shanghai')", runtime)
        self.assertLess(runtime.index('ENV PATH="/app/.venv/bin:$PATH"'), runtime.index("RUN python -c"))

    def test_empty_configuration_does_not_enable_readiness_business(self):
        env = (ROOT / "deploy/agriculture/env.example").read_text()
        self.assertIn("\nAGRI_FARM_DICTIONARY_CONFIG=\n", env)
        compose = (ROOT / "docker-compose.agriculture.yml").read_text()
        readiness = compose.split("  geo-readiness:\n", 1)[1].split("  platform-migrate:\n", 1)[0]
        self.assertNotIn("AGRI_FARM_DICTIONARY_CONFIG", readiness)

    def test_mount_is_explicit_read_only_and_requires_existing_file(self):
        docs = (ROOT / "deploy/agriculture/FARM_DICTIONARY.md").read_text()
        self.assertIn("${AGRI_FARM_DICTIONARY_HOST_FILE:?", docs)
        self.assertIn("read_only: true", docs)
        self.assertIn("create_host_path: false", docs)
        self.assertIn("app.agriculture.main:app", docs)


if __name__ == "__main__":
    unittest.main()
