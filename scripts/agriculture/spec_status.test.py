"""Recovery status invariants; only DT001..012 have a historical exception."""
import importlib.util
import json
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('spec_status.py')
spec = importlib.util.spec_from_file_location('status', SCRIPT)
status = importlib.util.module_from_spec(spec)
spec.loader.exec_module(status)


class StatusTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name) / 'specs/agriculture/v1.0.0'
        self.root.mkdir(parents=True)
        for name in ('requirements.json', 'MANIFEST.json', 'tasks.json',
                     'DEVELOPMENT_CHECKLIST.md', 'docs/implementation/STATUS.md'):
            target = self.root / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(status.SPEC / name, target)
        self.document = json.loads((self.root / 'tasks.json').read_text())
        for task in self.document['tasks']:
            task['status'] = 'done' if task['id'] in status.HISTORICAL_DONE else 'not_started'
        self.document['recovery'] = {'historical_done': sorted(status.HISTORICAL_DONE)}
        self.save()

    def save(self):
        (self.root / 'tasks.json').write_text(json.dumps(self.document))

    def cli(self, *args):
        return subprocess.run([sys.executable, str(SCRIPT), '--spec-root', str(self.root), *args],
                              capture_output=True, text=True)

    def git(self, *args):
        return subprocess.check_output(['git', *args], cwd=self.root.parents[2], stderr=subprocess.PIPE).decode().strip()

    def frozen_source(self):
        self.git('init', '-q')
        self.git('config', 'user.name', 'Fixture Only')
        self.git('config', 'user.email', 'fixture@example.invalid')
        self.git('add', 'fixture.py')
        self.git('commit', '-qm', 'fixture source snapshot')
        return self.git('rev-parse', 'HEAD')

    def test_historical_completion_retained_but_strict_evidence_fails(self):
        self.assertEqual(self.cli('--sync').returncode, 0)
        check = self.cli('--check')
        self.assertEqual(check.returncode, 0, check.stderr)
        self.assertIn('WARNING historical done', check.stdout)
        self.assertNotEqual(self.cli('--require-evidence').returncode, 0)

    def test_new_done_without_evidence_fails_default(self):
        self.document['tasks'][12]['status'] = 'done'
        self.save()
        result = self.cli('--sync')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('new done task missing verified evidence', result.stderr)

    def test_historical_exception_cannot_be_extended(self):
        self.document['recovery']['historical_done'].append('DT-013')
        self.save()
        self.assertNotEqual(self.cli('--sync').returncode, 0)

    def test_unknown_requirement_rejected(self):
        self.document['tasks'][0]['requirements'].append('AG-FR-999')
        self.save()
        self.assertNotEqual(self.cli('--sync').returncode, 0)

    def test_missing_or_modified_baseline_hash_rejected(self):
        del self.document['source_requirements_sha256']
        self.save()
        self.assertNotEqual(self.cli('--sync').returncode, 0)
        self.document['source_requirements_sha256'] = '0' * 64
        self.save()
        self.assertNotEqual(self.cli('--sync').returncode, 0)

    def test_cycle_and_unknown_dependency_rejected(self):
        self.document['tasks'][0]['dependencies'] = ['DT-002']
        self.save()
        self.assertNotEqual(self.cli('--sync').returncode, 0)
        self.document['tasks'][0]['dependencies'] = ['DT-999']
        self.save()
        self.assertNotEqual(self.cli('--sync').returncode, 0)

    def test_done_dependency_incomplete_rejected(self):
        self.document['tasks'][0]['status'] = 'in_progress'
        self.save()
        self.assertNotEqual(self.cli('--sync').returncode, 0)

    def test_checklist_drift_and_deterministic_sync_preserve_descriptions(self):
        task_source = (self.root / 'tasks.json').read_bytes()
        self.assertEqual(self.cli('--sync').returncode, 0)
        first = (self.root / 'DEVELOPMENT_CHECKLIST.md').read_bytes()
        self.assertEqual(self.cli('--sync').returncode, 0)
        self.assertEqual(first, (self.root / 'DEVELOPMENT_CHECKLIST.md').read_bytes())
        self.assertEqual(task_source, (self.root / 'tasks.json').read_bytes())
        path = self.root / 'DEVELOPMENT_CHECKLIST.md'
        path.write_text(path.read_text().replace('- 状态：`done`', '- 状态：`not_started`', 1))
        self.assertNotEqual(self.cli('--check').returncode, 0)

    def test_verified_without_commands_or_source_hash_rejected(self):
        self.document['tasks'][12]['status'] = 'done'
        self.save()
        evidence = self.root / 'docs/implementation/evidence.txt'
        evidence.write_text('fresh fixture test passed, not live acceptance\n')
        row = {'verification': 'verified', 'evidence': [{
            'path': 'docs/implementation/evidence.txt',
            'sha256': status.hashlib.sha256(evidence.read_bytes()).hexdigest()}]}
        path = self.root / 'docs/implementation/RECOVERY_STATUS.json'
        path.write_text(json.dumps({'tasks': {'DT-013': row}}))
        self.assertNotEqual(self.cli('--sync').returncode, 0)
        row['commands'] = [{'command': 'pytest fixture', 'exit_code': 0, 'result': 'passed'}]
        path.write_text(json.dumps({'tasks': {'DT-013': row}}))
        self.assertNotEqual(self.cli('--sync').returncode, 0)
        source = self.root.parents[2] / 'fixture.py'
        source.write_text('assert 1 == 1\n')
        row['source_hashes'] = [{'path': 'fixture.py', 'sha256': status.hashlib.sha256(source.read_bytes()).hexdigest()}]
        path.write_text(json.dumps({'tasks': {'DT-013': row}}))
        self.assertNotEqual(self.cli('--sync').returncode, 0)
        row['source_commit'] = self.frozen_source()
        path.write_text(json.dumps({'tasks': {'DT-013': row}}))
        self.assertEqual(self.cli('--sync').returncode, 0)
        source.write_text('assert 1 == 2\n')
        self.assertEqual(self.cli('--check').returncode, 0)
        self.git('add', 'fixture.py')
        self.git('commit', '-qm', 'later task modifies source')
        self.assertEqual(self.cli('--check').returncode, 0)
        for invalid in ('HEAD', 'a' * 40, '../bad'):
            row['source_commit'] = invalid
            path.write_text(json.dumps({'tasks': {'DT-013': row}}))
            self.assertNotEqual(self.cli('--check').returncode, 0)
        # A real commit object with no parent is not authoritative current history.
        orphan = self.git('commit-tree', 'HEAD^{tree}', '-m', 'unrelated fixture commit')
        row['source_commit'] = orphan
        path.write_text(json.dumps({'tasks': {'DT-013': row}}))
        self.assertNotEqual(self.cli('--check').returncode, 0)
        row['source_commit'] = self.git('rev-parse', 'HEAD~1')
        row['source_hashes'][0]['sha256'] = '0' * 64
        path.write_text(json.dumps({'tasks': {'DT-013': row}}))
        self.assertNotEqual(self.cli('--check').returncode, 0)


if __name__ == '__main__':
    unittest.main()
