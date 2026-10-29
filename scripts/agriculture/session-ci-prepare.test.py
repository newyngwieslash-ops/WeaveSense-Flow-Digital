import importlib.util
import io
from pathlib import Path
import tarfile
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("prepare", Path(__file__).with_name("session-ci-prepare.py"))
prepare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(prepare)


class PreparationTests(unittest.TestCase):
    def test_overlay_only_copies_reviewed_paths(self):
        with tempfile.TemporaryDirectory() as temp:
            source, destination = Path(temp) / "source", Path(temp) / "destination"
            source.mkdir(); destination.mkdir()
            for relative in prepare.OVERLAY:
                path = source / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("authored fixture")
            (source / "UNREVIEWED").write_text("must not copy")
            (source / ".git").mkdir()
            (source / ".git/config").write_text("never copy repository config")
            entries = prepare.overlay(source, destination)
            self.assertEqual(len(entries), len(prepare.OVERLAY))
            self.assertFalse((destination / "UNREVIEWED").exists())
            self.assertFalse((destination / ".git").exists())
            self.assertTrue(all(len(entry["sha256"]) == 64 for entry in entries))

    def test_missing_overlay_does_not_silently_fall_back_to_upstream(self):
        with tempfile.TemporaryDirectory() as temp:
            source, destination = Path(temp) / "source", Path(temp) / "destination"
            source.mkdir(); destination.mkdir()
            with self.assertRaises(ValueError):
                prepare.overlay(source, destination)

    def test_archives_reject_traversal_symlinks_and_git_metadata(self):
        for name, kind in [("../escape", tarfile.REGTYPE), ("/absolute", tarfile.REGTYPE), (".git/config", tarfile.REGTYPE), ("link", tarfile.SYMTYPE)]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                archive = root / "source.tar"
                with tarfile.open(archive, "w") as tar:
                    member = tarfile.TarInfo(name)
                    member.type = kind
                    member.linkname = "../../escape"
                    tar.addfile(member, io.BytesIO())
                with self.assertRaises(ValueError):
                    prepare.extract_archive(archive, root / "destination")

    def test_regular_source_archive_preserves_bytes_without_git(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            archive = root / "source.tar"
            with tarfile.open(archive, "w") as tar:
                member = tarfile.TarInfo("source/file.txt")
                member.size = 7
                tar.addfile(member, io.BytesIO(b"fixture"))
            prepare.extract_archive(archive, root / "destination")
            self.assertEqual((root / "destination/source/file.txt").read_bytes(), b"fixture")


if __name__ == "__main__":
    unittest.main()
