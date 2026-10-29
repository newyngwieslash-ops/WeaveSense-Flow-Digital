import importlib.util
from pathlib import Path
import tempfile
import types
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("python_lock_export", ROOT / "scripts/agriculture/export-python-lock.py")
exporter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(exporter)
BODY = 'colorama==0.4.6 ; sys_platform == "win32" ' + '\\' + '\n    --hash=sha256:' + 'a' * 64 + '\n'


class PythonLockExportTest(unittest.TestCase):
    def test_markers_and_hashes_pass_through_unchanged(self):
        with tempfile.TemporaryDirectory() as tmp:
            project = Path(tmp)
            for name in ["poetry.lock", "pyproject.toml"]:
                (project / name).write_text("original")
            def runner(command, **kwargs):
                self.assertIn("poetry==2.2.1", command)
                self.assertIn("poetry-plugin-export==1.9.0", command)
                self.assertIn("--without-urls", command)
                self.assertNotIn("--without-hashes", command)
                self.assertEqual(command[command.index("--only") + 1], "dev")
                self.assertNotEqual(kwargs["cwd"], project)
                self.assertEqual(kwargs["env"]["POETRY_CACHE_DIR"], str(ROOT / ".local/cache/poetry"))
                return types.SimpleNamespace(stdout=BODY)
            result = exporter.export(project, "dev", runner=runner)
            self.assertTrue(result.endswith(BODY))
            self.assertEqual((project / "poetry.lock").read_text(), "original")

    def test_rejects_empty_unhashed_unpinned_export(self):
        for body in ["", "numpy==2.0\n", "numpy>=2 --hash=sha256:" + "a"*64,
                     "--extra-index-url https://example.invalid\n"]:
            with self.subTest(body=body), self.assertRaises(ValueError):
                exporter.validate_export(body)

    def test_refuses_exporter_lock_rewrite(self):
        with tempfile.TemporaryDirectory() as tmp:
            project = Path(tmp)
            for name in ["poetry.lock", "pyproject.toml"]:
                (project / name).write_text("original")
            def runner(command, **kwargs):
                (kwargs["cwd"] / "poetry.lock").write_text("changed")
                return types.SimpleNamespace(stdout=BODY)
            with self.assertRaisesRegex(ValueError, "changed source"):
                exporter.export(project, "main", runner=runner)
            self.assertEqual((project / "poetry.lock").read_text(), "original")


if __name__ == "__main__":
    unittest.main()
