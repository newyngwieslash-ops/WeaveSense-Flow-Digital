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
    def test_gateway_dependency_closure_is_explicit_and_unique(self):
        required = {
            "contracts/agriculture/navigation.ts",
            "deploy/agriculture/Dockerfile.web", "deploy/agriculture/Dockerfile.web.dockerignore",
            "deploy/agriculture/nginx.conf.template", "deploy/agriculture/web-entrypoint.sh",
            "deploy/agriculture/errors/404.html", "deploy/agriculture/errors/50x.html",
            "scripts/agriculture/gateway-browser-e2e.mjs", "scripts/agriculture/gateway-browser-e2e.test.mjs",
            "scripts/agriculture/gateway-container-smoke.py", "scripts/agriculture/gateway-container-smoke.test.py",
            "scripts/agriculture/session-e2e.mjs", "scripts/agriculture/browser-navigation.mjs",
            "services/agri-gis/frontend/agriculture/session-lifecycle.ts",
            "services/agri-gis/frontend/agriculture/App.vue", "web/src/lib/auth-return-path.ts",
            "tests/agriculture/test_gateway_configuration.py",
        }
        self.assertTrue(required <= set(prepare.OVERLAY))
        self.assertEqual(len(prepare.OVERLAY), len(set(prepare.OVERLAY)))
        self.assertFalse(any("*" in path for path in prepare.OVERLAY))

    def test_membership_overlay_includes_dependency_closure_explicitly(self):
        required = {
            "backend/cmd/server/main.go", "backend/internal/agriculture/roles.go",
            "backend/internal/agriculture/routes.go", "backend/internal/agriculture/service.go",
            "backend/internal/agriculture/service_test.go", "backend/internal/agriculture/postgres_test.go",
            "backend/internal/database/migrations.go", "backend/internal/database/migration_agriculture.go",
            "backend/internal/database/migration_agriculture_postgres_test.go", "backend/internal/model/models_agriculture.go",
            "backend/internal/model/models_tenancy.go", "backend/internal/model/models_flow_billing.go",
            "deploy/agriculture/postgres/init.sh", "scripts/agriculture/membership-ci.sh",
            "web/src/features/agriculture/organization.tsx", "web/src/services/api/agriculture.ts",
            "scripts/agriculture/organization-e2e.mjs", "scripts/agriculture/organization-e2e.test.mjs",
            "scripts/agriculture/ORGANIZATION_E2E.md",
            "backend/internal/model/models_agriculture_authorization.go",
            "backend/internal/database/migration_agriculture_authorization.go",
            "backend/internal/database/migration_agriculture_authorization_test.go",
            "backend/internal/database/migration_agriculture_authorization_postgres_test.go",
            "backend/internal/agriculture/authorization.go", "backend/internal/agriculture/authorization_test.go",
            "backend/internal/agriculture/authorization_postgres_test.go",
            "backend/internal/agriculture/resource_ownership.go", "backend/internal/agriculture/resource_ownership_test.go",
            "backend/internal/agriculture/resource_bindings.go", "backend/internal/agriculture/resource_bindings_test.go",
            "backend/internal/agriculture/policy.go", "backend/internal/agriculture/policy_test.go",
            "backend/go.mod", "backend/go.sum",
            "backend/internal/agricanonical/canonical.go",
            "backend/internal/agriculture/delegation.go",
            "backend/internal/agriculture/identity_http_integration_test.go",
            "services/agri-gis/backend/pyproject.toml", "services/agri-gis/backend/poetry.lock",
            "services/agri-gis/backend/app/agriculture/identity/verify.py",
            "services/agri-gis/backend/app/agriculture/identity/dependency.py",
            "services/agri-gis/backend/app/agriculture/identity/replay.py",
            "services/agri-gis/backend/scripts/migrations/20261029_agriculture_identity_replay.sql",
            "services/agri-gis/backend/tests/identity_http_fixture.py",
            "services/agri-gis/backend/tests/test_agriculture_identity_postgres.py",
            "scripts/agriculture/identity-postgres-ci.sh",
            "scripts/agriculture/identity-ci-result.py",
            "scripts/agriculture/identity-chain-result.py",
            "scripts/agriculture/identity-chain-result.test.py",
            "scripts/agriculture/identity-chain-browser.mjs",
            "scripts/agriculture/identity-chain-browser.test.mjs",
            "backend/internal/agriculture/identity_browser_chain_test.go",
            "services/agri-gis/backend/tests/identity_chain_fixture.py",
            "services/agri-gis/backend/tests/test_identity_chain_fixture.py",
            "scripts/agriculture/export-python-lock.py",
            "scripts/agriculture/python-test-requirements.txt",
            "tests/agriculture/fixtures/identity-canonical.json",
            "contracts/agriculture/service-request.schema.json",
        }
        self.assertTrue(required.issubset(set(prepare.OVERLAY)))
        self.assertEqual(len(prepare.OVERLAY), len(set(prepare.OVERLAY)))
        self.assertTrue(all("*" not in path and not path.startswith(".github/") for path in prepare.OVERLAY))

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
