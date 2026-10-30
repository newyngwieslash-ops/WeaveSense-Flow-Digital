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
    def test_progress_retains_only_closed_structural_fields(self):
        raw = b"\n".join((
            b"#1 [internal] load metadata for https://SECRET@example.invalid/image",
            b"#2 [bun 1/1] FROM registry.invalid/image?token=SECRET",
            b"#3 [build 4/15] RUN env DSN=postgres://SECRET command --secret=SECRET",
            b"#3 0.12 SECRET arbitrary command output",
            b"#3 ERROR: SECRET failure https://example.invalid",
            b"#4 [SECRET 1/2] COPY SECRET /app",
            b"#5 [stage-2 2/5] COPY --from=build SECRET /app",
            b"#5 DONE 12.345s",
            b"#6 CACHED",
            b"#7 DONE 1s SECRET",
        ))
        result = smoke.build_progress(b"", raw)
        self.assertEqual(len(result["events"]), 6)
        self.assertEqual(result["events"][0]["operation"], "LOAD_METADATA")
        self.assertEqual(result["events"][2]["operation"], "RUN")
        self.assertEqual(result["events"][-2]["elapsed_seconds"], 12.345)
        for secret in ("SECRET", "DSN", "postgres", "https", "invalid", "--from", "/app"):
            self.assertNotIn(secret, json.dumps(result))

    def test_progress_bounds_and_malformed_lines(self):
        result = smoke.build_progress(b"#1 CACHED\n" * 30000)
        self.assertEqual(len(result["events"]), 128)
        self.assertTrue(result["truncated"])
        for raw in (b"#0 DONE 0s", b"#1 DONE infs", b"#1 DONE -1s",
                    b"#1 [build 3/2] RUN x", b"#1 [build 1/1] RUNX x",
                    b"#1 [build 1/1] RUN " + b"x" * 5000):
            self.assertEqual(smoke.build_progress(raw)["events"], [])
        self.assertEqual(smoke.build_progress(None, None)["events"], [])

    def test_build_timeout_and_nonzero_are_failure_with_safe_progress(self):
        for error_class in (subprocess.TimeoutExpired, subprocess.CalledProcessError):
            with self.subTest(error_class=error_class):
                calls = []

                def command(*args, **kwargs):
                    calls.append(args)
                    self.assertEqual(kwargs["timeout"], 1800)
                    self.assertIn("--progress=plain", args)
                    output = b"#10 [build 4/15] RUN SECRET\n#10 15.0 SECRET"
                    if error_class is subprocess.TimeoutExpired:
                        raise error_class(args, 1800, output=output, stderr=b"DSN=SECRET")
                    raise error_class(1, args, output=output, stderr=b"DSN=SECRET")

                with tempfile.TemporaryDirectory() as directory:
                    with patch.object(smoke, "ROOT", Path(directory)), patch.object(
                        smoke, "command", command
                    ):
                        with self.assertRaises(SystemExit) as result:
                            smoke.main()
                    report = json.loads((Path(directory) / ".local/evidence/gateway-container-smoke.json").read_text())
                self.assertEqual(result.exception.code, 1)
                self.assertEqual(len(calls), 1)
                self.assertFalse(report["passed"])
                self.assertEqual(report["checks"], [])
                self.assertEqual(report["failure"]["phase"], "build")
                self.assertEqual(report["build_progress"]["events"][0]["operation"], "RUN")
                self.assertNotIn("SECRET", json.dumps(report))
                self.assertNotIn("DSN", json.dumps(report))

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
            self.assertIn("build_progress", report)

    def test_success_keeps_all_sixteen_http_assertions_and_progress(self):
        def command(*args, **kwargs):
            if args[:2] == ("docker", "build"):
                self.assertEqual(kwargs["timeout"], 1800)
                self.assertIn("--progress=plain", args)
                return subprocess.CompletedProcess(
                    args, 0, b"", b"#1 [build 4/15] RUN SECRET\n#1 DONE 1.5s"
                )
            output = b"fixture-image\n"
            if args[:2] == ("docker", "inspect"):
                output = b'{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"45678"}]}'
            return subprocess.CompletedProcess(args, 0, output, b"")

        def fetch(origin, path):
            if "index-abcdefgh.js" in path:
                return 200, {"Cache-Control": "immutable"}, b""
            if path in ("/", "/agri/", "/agri/workspaces", "/login"):
                return (200, {"Cache-Control": "no-store"},
                        b'<script src="/assets/index-abcdefgh.js"></script>')
            status = 502 if path == "/api/auth/session" else 404
            return status, {"Cache-Control": "no-store"}, b""

        with tempfile.TemporaryDirectory() as directory:
            with patch.object(smoke, "ROOT", Path(directory)), patch.object(
                smoke, "command", command
            ), patch.object(smoke, "fetch", fetch):
                with self.assertRaises(SystemExit) as result:
                    smoke.main()
            report = json.loads((Path(directory) / ".local/evidence/gateway-container-smoke.json").read_text())
        self.assertEqual(result.exception.code, 0)
        self.assertTrue(report["passed"])
        self.assertEqual(len(report["checks"]), 16)
        self.assertEqual(report["build_progress"]["events"][-1]["status"], "DONE")
        self.assertNotIn("SECRET", json.dumps(report))

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
