"""Static safety regression; this never replaces actual Docker smoke."""
import unittest
from copy import deepcopy
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parents[2]


def assert_host_port_policy(config):
    # DT014 adds the same-origin gateway, not direct platform/GIS publication.
    # Keep exact reviewed templates: arbitrary long syntax, ranges and extra
    # entries must require a new policy review rather than a prefix-only check.
    allowed = {
        "web": ["127.0.0.1:${AGRI_WEB_PORT:-8091}:8080"],
        "geo-readiness": ["127.0.0.1:${AGRI_HTTP_PORT:-8090}:8000"],
    }
    for name, service in config["services"].items():
        if name in allowed:
            assert service.get("ports") == allowed[name], name
        else:
            assert "ports" not in service, name


class ComposePolicyTests(unittest.TestCase):
    def setUp(self):
        self.config = yaml.safe_load((ROOT / "docker-compose.agriculture.yml").read_text())

    def test_tmpfs_options_are_one_absolute_mount(self):
        mounts = self.config["services"]["geo-readiness"]["tmpfs"]
        self.assertEqual(mounts, ["/tmp:rw,noexec,nosuid,size=16m"])

    def test_no_database_or_business_api_host_ports(self):
        assert_host_port_policy(self.config)

    def test_private_services_cannot_publish_even_loopback_ports(self):
        for name in ("postgres", "redis", "platform", "loom-engine", "gis"):
            with self.subTest(service=name):
                config = deepcopy(self.config)
                config["services"].setdefault(name, {})["ports"] = [
                    "127.0.0.1:8999:8080"
                ]
                with self.assertRaises(AssertionError):
                    assert_host_port_policy(config)

    def test_gateway_rejects_unreviewed_publication_forms(self):
        invalid = [
            ["0.0.0.0:${AGRI_WEB_PORT:-8091}:8080"],
            ["[::]:8091:8080"],
            ["8091:8080"],
            ["127.0.0.1:${AGRI_WEB_PORT:-8091}:8000"],
            ["127.0.0.1:${UNREVIEWED_PORT:-8091}:8080"],
            ["127.0.0.1:${AGRI_WEB_PORT:-0}:8080"],
            ["127.0.0.1:8091-8092:8080"],
            [{"target": 8080, "published": "8091", "host_ip": "0.0.0.0"}],
            ["127.0.0.1:${AGRI_WEB_PORT:-8091}:8080", "9000:9000"],
            "127.0.0.1:${AGRI_WEB_PORT:-8091}:8080",
            [],
            None,
        ]
        for ports in invalid:
            with self.subTest(ports=ports):
                config = deepcopy(self.config)
                config["services"]["web"]["ports"] = ports
                with self.assertRaises(AssertionError):
                    assert_host_port_policy(config)

    def test_readiness_exception_is_not_a_generic_loopback_bypass(self):
        config = deepcopy(self.config)
        config["services"]["geo-readiness"]["ports"] = ["127.0.0.1:8090:5432"]
        with self.assertRaises(AssertionError):
            assert_host_port_policy(config)

    def test_network_is_internal(self):
        self.assertIs(self.config["networks"]["internal"]["internal"], True)
        for service in self.config["services"].values():
            self.assertEqual(service["networks"], ["internal"])


if __name__ == "__main__":
    unittest.main()
