"""Static safety regression; this never replaces actual Docker smoke."""
import unittest
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]


class ComposePolicyTests(unittest.TestCase):
    def setUp(self):
        self.config = yaml.safe_load((ROOT / "docker-compose.agriculture.yml").read_text())

    def test_tmpfs_options_are_one_absolute_mount(self):
        mounts = self.config["services"]["geo-readiness"]["tmpfs"]
        self.assertEqual(mounts, ["/tmp:rw,noexec,nosuid,size=16m"])

    def test_no_database_or_business_api_host_ports(self):
        for name, service in self.config["services"].items():
            if name != "geo-readiness":
                self.assertNotIn("ports", service, name)
        for port in self.config["services"]["geo-readiness"]["ports"]:
            self.assertTrue(port.startswith("127.0.0.1:"))

    def test_network_is_internal(self):
        self.assertIs(self.config["networks"]["internal"]["internal"], True)
        for service in self.config["services"].values():
            self.assertEqual(service["networks"], ["internal"])


if __name__ == "__main__":
    unittest.main()
