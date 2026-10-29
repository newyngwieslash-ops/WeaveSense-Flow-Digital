"""Native PostgreSQL helper must reject an escaped run path before any writes."""

import hashlib
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


class NativePostgresPathTests(unittest.TestCase):
    def test_existing_runs_symlink_is_rejected_before_cluster_creation(self):
        source = Path(__file__).resolve().parents[2]
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            repo, outside = root / 'repo', root / 'outside'
            outside.mkdir()
            script = repo / 'scripts/agriculture/native-postgres-test.py'
            script.parent.mkdir(parents=True)
            shutil.copyfile(source / 'scripts/agriculture/native-postgres-test.py', script)
            manifest = repo / 'deploy/agriculture/native-postgres-packages.json'
            manifest.parent.mkdir(parents=True)
            manifest.write_text('{"packages": []}')
            packages = repo / '.local/packages'
            postgres = packages / 'usr/lib/postgresql/17/bin/postgres'
            postgres.parent.mkdir(parents=True)
            postgres.write_text('fixture placeholder, must never execute')
            (packages / 'prepared.json').write_text(json.dumps({
                'manifest_sha256': hashlib.sha256(manifest.read_bytes()).hexdigest(),
                'files': {}, 'links': {},
            }))
            runs = repo / '.local/native-postgres/runs'
            runs.parent.mkdir(parents=True)
            runs.symlink_to(outside, target_is_directory=True)
            result = subprocess.run([sys.executable, str(script), '--package-root', str(packages)],
                                    capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('fixture run path escapes repository .local', result.stderr)
            self.assertEqual(list(outside.iterdir()), [])
            self.assertFalse(any(repo.rglob('bootstrap-password')))
            self.assertFalse(any(repo.rglob('postmaster.pid')))


if __name__ == '__main__':
    unittest.main()
