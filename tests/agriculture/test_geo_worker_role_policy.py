"""Deployment source contract; actual SQL privilege checks run in disposable CI."""
from pathlib import Path
import re
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[2]


class GeoWorkerRolePolicy(unittest.TestCase):
    def test_role_is_separate_and_unprivileged(self):
        source = (ROOT / "deploy/agriculture/postgres/init.sh").read_text()
        self.assertIn('${GEO_WORKER_PASSWORD:?', source)
        self.assertIn("CREATE ROLE flow_geo_worker LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS", source)
        grants = re.findall(r"^GRANT .* TO flow_geo_worker;", source, re.M)
        self.assertEqual(grants, ["GRANT CONNECT ON DATABASE flow_geo TO flow_geo_worker;", "GRANT USAGE ON SCHEMA public TO flow_geo_worker;"])
        self.assertNotRegex(source, r"GRANT flow_geo_(?:app|migrate|actor_guard) TO flow_geo_worker")

    def test_secret_only_in_database_initialization(self):
        compose = (ROOT / "docker-compose.agriculture.yml").read_text()
        self.assertEqual(compose.count("GEO_WORKER_PASSWORD:"), 2)  # key + required variable
        postgres = compose.split("  postgres:\n", 1)[1].split("  redis:\n", 1)[0]
        self.assertIn("GEO_WORKER_PASSWORD: ${GEO_WORKER_PASSWORD:?", postgres)
        self.assertEqual(compose.count("flow_geo_worker"), 0)
        self.assertIn("\nGEO_WORKER_PASSWORD=\n", (ROOT / "deploy/agriculture/env.example").read_text())

    def test_shells_parse_and_probe_requires_owned_harness(self):
        for shell, path in [("sh", "deploy/agriculture/postgres/init.sh"), ("bash", "scripts/agriculture/geo-worker-role-test.sh")]:
            subprocess.run([shell, "-n", str(ROOT / path)], check=True)
        result = subprocess.run(["bash", "-c", "source scripts/agriculture/geo-worker-role-test.sh"], cwd=ROOT, capture_output=True)
        self.assertEqual(result.returncode, 2)


if __name__ == "__main__":
    unittest.main()
