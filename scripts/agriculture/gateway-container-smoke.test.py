import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location(
    "smoke", Path(__file__).with_name("gateway-container-smoke.py")
)
smoke = importlib.util.module_from_spec(spec)
spec.loader.exec_module(smoke)


class SmokeHelperTests(unittest.TestCase):
    def test_run_timeout_still_cleans_exact_owned_name(self):
        calls = []

        def command(*args, **kwargs):
            calls.append(args)
            if args[:2] == ("docker", "run"):
                raise subprocess.TimeoutExpired(args, 60)
            return subprocess.CompletedProcess(args, 0, b"fixture-image\n", b"")

        with tempfile.TemporaryDirectory() as directory:
            with patch.object(smoke, "ROOT", Path(directory)), patch.object(
                smoke, "command", command
            ):
                with self.assertRaises(SystemExit) as result:
                    smoke.main()
            self.assertEqual(result.exception.code, 1)
            run = next(c for c in calls if c[:2] == ("docker", "run"))
            name = run[run.index("--name") + 1]
            self.assertRegex(name, r"^weavesense-gateway-smoke-[a-f0-9]{32}$")
            self.assertIn(("docker", "rm", "--force", name), calls)
            report = json.loads(
                (
                    Path(directory) / ".local/evidence/gateway-container-smoke.json"
                ).read_text()
            )
            self.assertFalse(report["passed"])
            self.assertEqual(report["failure"]["type"], "TimeoutExpired")

    def test_only_owned_loopback_binding_accepted(self):
        self.assertEqual(
            smoke.owned_container_port(
                '{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"45678"}]}'
            ),
            45678,
        )
        for value in (
            '{"8080/tcp":[]}',
            '{"8080/tcp":[{"HostIp":"0.0.0.0","HostPort":"45678"}]}',
            '{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"0"}]}',
        ):
            with self.assertRaises(ValueError):
                smoke.owned_container_port(value)

    def test_requires_real_hashed_script_not_html_fallback(self):
        for path in (b"/assets/index-a1b2c3d4.js", b"/agri/assets/index-a1b2c3d4.js"):
            self.assertEqual(
                smoke.hashed_script(b'<script src="' + path + b'"></script>'),
                path.decode(),
            )
        with self.assertRaises(ValueError):
            smoke.hashed_script(b"<html>not a built app</html>")


if __name__ == "__main__":
    unittest.main()
