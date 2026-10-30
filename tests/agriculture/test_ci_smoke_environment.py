"""Shell environment/cleanup contract only, NOT actual Docker acceptance."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class SmokeEnvironmentTests(unittest.TestCase):
    def test_fixture_initializes_every_required_variable_before_up_and_cleanup(self):
        required = sorted(set(re.findall(r"\$\{([A-Z_]+):\?", (ROOT / "docker-compose.agriculture.yml").read_text())))
        for mode in ("core", "migrations"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                # A command-boundary test double deliberately fails BEFORE any
                # Docker operation. It proves shell setup/cleanup, not containers.
                docker = root / "docker"
                docker.write_text("""#!/usr/bin/env python3
import json, os, pathlib, stat, sys
args = sys.argv[1:]
if args == ['compose', 'version'] or args == ['info'] or args[:1] == ['ps'] or args[:2] == ['volume', 'ls']:
    sys.exit(0)
if not args or args[0] != 'compose': sys.exit(91)
phase = 'up' if 'up' in args else 'down' if 'down' in args else 'unexpected'
missing = [name for name in json.loads(os.environ['TEST_REQUIRED']) if not os.environ.get(name)]
key = pathlib.Path(os.environ.get('FLOW_AGRICULTURE_CSRF_KEY_FILE', '/missing'))
valid = (not missing and key.is_file() and key.stat().st_size == 32
         and stat.S_IMODE(key.stat().st_mode) == 0o600
         and os.environ.get('FLOW_AGRICULTURE_EXTERNAL_ORIGIN') == 'http://127.0.0.1:8091'
         and os.environ.get('FLOW_AGRICULTURE_ALLOW_HTTP_LOOPBACK_DEV') == 'true')
with open(os.environ['TEST_LOG'], 'a') as output:
    output.write(json.dumps({'phase': phase, 'valid': valid, 'missing': missing, 'key_path': str(key)}) + '\\n')
if not valid or phase == 'unexpected': sys.exit(92)
sys.exit(42 if phase == 'up' else 0)
""")
                docker.chmod(0o755)
                env = {key: value for key, value in os.environ.items() if key not in required}
                env.update(PATH=str(root) + os.pathsep + os.environ["PATH"], TEST_REQUIRED=json.dumps(required), TEST_LOG=str(root / "calls.jsonl"))
                env.pop("GITHUB_ACTIONS", None)
                result = subprocess.run(["bash", "scripts/agriculture/ci-smoke.sh", mode], cwd=ROOT, env=env, capture_output=True, text=True)
                self.assertEqual(result.returncode, 42, "shell must preserve controlled up failure")
                calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                self.assertEqual([call["phase"] for call in calls], ["up", "down"])
                self.assertTrue(all(call["valid"] for call in calls))
                self.assertFalse(Path(calls[0]["key_path"]).exists(), "owned temporary key must be removed")


if __name__ == "__main__":
    unittest.main()
