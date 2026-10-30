#!/usr/bin/env python3
"""Read-only pinned source acquisition for disposable CI; never publishes history.

Scaffold mode overlays ONLY these reviewed paths, never arbitrary scaffold content.
Full mode moves the actual checked-out product intact, without fetching upstream.
"""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import tarfile
import tempfile

FLOW = ("https://github.com/newyngwieslash-ops/WeaveSense-Flow.git", "206c82af7ae007f45da4c582f373fbabf027f1b8")
GIS = ("https://github.com/autool/AgriScope.git", "571a2dcd9abf8b8a22b66c750ee710b61d482051")
OVERLAY = (
    "backend/cmd/server/main.go",
    "backend/internal/browsersecurity/policy.go",
    "backend/internal/browsersecurity/middleware.go",
    "backend/internal/browsersecurity/policy_test.go",
    "backend/internal/handler/agriculture_browser_security_test.go",
    "backend/internal/agriculture/revocation_postgres_test.go",
    "backend/internal/agriculture/authority_fingerprint.go",
    "backend/internal/agriculture/authority_fingerprint_test.go",
    "backend/internal/model/models_agriculture.go",
    "backend/internal/model/models_agriculture_authorization.go",
    "backend/internal/model/models_tenancy.go",
    "backend/internal/model/models_flow_billing.go",
    "backend/internal/database/migrations.go",
    "backend/internal/database/migration_agriculture.go",
    "backend/internal/database/migration_agriculture_test.go",
    "backend/internal/database/migration_agriculture_postgres_test.go",
    "backend/internal/database/migration_agriculture_authorization.go",
    "backend/internal/database/migration_agriculture_authorization_test.go",
    "backend/internal/database/migration_agriculture_authorization_postgres_test.go",
    "backend/internal/agriculture/roles.go",
    "backend/internal/agriculture/routes.go",
    "backend/internal/agriculture/service.go",
    "backend/internal/agriculture/service_test.go",
    "backend/internal/agriculture/postgres_test.go",
    "backend/internal/agriculture/authorization.go",
    "backend/internal/agriculture/authorization_test.go",
    "backend/internal/agriculture/authorization_http_test.go",
    "backend/internal/agriculture/authorization_v1.go",
    "backend/internal/agriculture/authorization_v1_test.go",
    "backend/internal/agriculture/binding_pagination.go",
    "backend/internal/agriculture/binding_pagination_test.go",
    "backend/internal/agriculture/authorization_postgres_test.go",
    "backend/internal/agriculture/resource_ownership.go",
    "backend/internal/agriculture/resource_ownership_test.go",
    "backend/internal/agriculture/resource_bindings.go",
    "backend/internal/agriculture/resource_bindings_test.go",
    "backend/internal/agriculture/policy.go",
    "backend/internal/agriculture/policy_test.go",
    "backend/internal/flow/orgs.go",
    "backend/internal/flow/p7.go",
    "backend/internal/flow/rls_runtime.go",
    "backend/internal/flow/routes.go",
    "backend/internal/flow/routes_p5.go",
    "backend/internal/flow/routes_p7.go",
    "backend/internal/flow/scope.go",
    "backend/internal/flow/agriculture_membership_regression_test.go",
    "deploy/agriculture/postgres/init.sh",
    "scripts/agriculture/membership-ci.sh",
    "scripts/agriculture/membership-ci-result.py",
    "scripts/agriculture/membership-ci-result.test.py",
    "backend/internal/auth/auth.go",
    "backend/internal/auth/password_reset_test.go",
    "backend/internal/auth/session_security_test.go",
    "backend/internal/handler/auth.go",
    "backend/internal/handler/logout_security_test.go",
    "web/package.json",
    "web/vite.agriculture.config.ts",
    "web/tsconfig.agriculture.json",
    "web/scripts/agriculture-build-policy.ts",
    "web/scripts/check-agriculture-artifact.ts",
    "web/src/features/agriculture/main.tsx",
    "web/src/features/agriculture/session.ts",
    "web/src/features/agriculture/profile.css",
    "web/src/features/agriculture/organization-controller.ts",
    "web/src/features/agriculture/organization.tsx",
    "web/src/services/api/agriculture.ts",
    "web/test/agriculture-api.test.ts",
    "web/test/agriculture-organization.test.ts",
    "web/src/lib/auth-return-path.ts",
    "web/src/pages/auth/login.tsx",
    "web/src/pages/auth/register.tsx",
    "web/src/pages/auth/forgot-password.tsx",
    "web/src/services/live2d-loader.ts",
    "web/src/services/api/auth.ts",
    "web/test/agriculture-profile.test.ts",
    "web/test/auth-return-path.test.ts",
    "web/test/auth-session-race.test.ts",
    "web/test/live2d-loader-cleanup.test.ts",
    "docs/agriculture/profile/licenses/ant-design-icons-svg-4.5.0.LICENSE",
    "services/agri-gis/frontend/package.json",
    "services/agri-gis/frontend/vite.agriculture.config.ts",
    "services/agri-gis/frontend/tsconfig.agriculture.json",
    "services/agri-gis/frontend/agriculture/main.ts",
    "services/agri-gis/frontend/agriculture/App.vue",
    "services/agri-gis/frontend/agriculture/session.ts",
    "services/agri-gis/frontend/agriculture/session-lifecycle.ts",
    "services/agri-gis/frontend/agriculture/session.test.ts",
    "services/agri-gis/frontend/agriculture/index.html",
    "services/agri-gis/frontend/agriculture/style.css",
    "scripts/agriculture/session-e2e.mjs",
    "scripts/agriculture/csrf-fixture.mjs",
    "scripts/agriculture/csrf-fixture.test.mjs",
    "contracts/agriculture/session-hints.ts",
    "contracts/agriculture/authority-snapshot.ts",
    "web/src/features/agriculture/request-security.ts",
    "web/src/features/agriculture/routine-session.ts",
    "web/src/features/agriculture/confirmation-scope.ts",
    "web/test/agriculture-session-security.test.ts",
    "docs/agriculture/BROWSER_SECURITY.md",
    "docs/agriculture/SESSION_REVALIDATION.md",
    "scripts/agriculture/browser-navigation.mjs",
    "scripts/agriculture/session-e2e.test.mjs",
    "scripts/agriculture/organization-e2e.mjs",
    "scripts/agriculture/organization-e2e.test.mjs",
    "scripts/agriculture/ORGANIZATION_E2E.md",
    "scripts/agriculture/session-ci-prepare.py",
    "scripts/agriculture/session-ci-prepare.test.py",
    "scripts/agriculture/SESSION_E2E.md",
    # DT012 authored identity implementation, fixed dependency locks and evidence tests.
    "backend/go.mod",
    "backend/go.sum",
    "backend/internal/agricanonical/canonical.go",
    "backend/internal/agricanonical/canonical_test.go",
    "backend/internal/agriculture/delegation.go",
    "backend/internal/agriculture/delegation_test.go",
    "backend/internal/agriculture/service_identity.go",
    "backend/internal/agriculture/service_identity_config.go",
    "backend/internal/agriculture/service_identity_keys.go",
    "backend/internal/agriculture/service_identity_test.go",
    "backend/internal/agriculture/service_identity_transport.go",
    "backend/internal/agriculture/service_identity_transport_test.go",
    "backend/internal/agriculture/identity_http_integration_test.go",
    "services/agri-gis/backend/pyproject.toml",
    "services/agri-gis/backend/poetry.lock",
    "services/agri-gis/backend/app/agriculture/__init__.py",
    "services/agri-gis/backend/app/agriculture/main.py",
    "services/agri-gis/backend/app/agriculture/identity/__init__.py",
    "services/agri-gis/backend/app/agriculture/identity/callback.py",
    "services/agri-gis/backend/app/agriculture/identity/canonical.py",
    "services/agri-gis/backend/app/agriculture/identity/config.py",
    "services/agri-gis/backend/app/agriculture/identity/dependency.py",
    "services/agri-gis/backend/app/agriculture/identity/keys.py",
    "services/agri-gis/backend/app/agriculture/identity/providers.py",
    "services/agri-gis/backend/app/agriculture/identity/replay.py",
    "services/agri-gis/backend/app/agriculture/identity/verify.py",
    "services/agri-gis/backend/scripts/migrations/20261029_agriculture_identity_replay.sql",
    "services/agri-gis/backend/tests/identity_http_fixture.py",
    "services/agri-gis/backend/tests/test_identity_canonical.py",
    "services/agri-gis/backend/tests/test_agriculture_identity.py",
    "services/agri-gis/backend/tests/test_agriculture_identity_postgres.py",
    "scripts/agriculture/identity-ci.sh",
    "scripts/agriculture/identity-postgres-ci.sh",
    "scripts/agriculture/identity-ci-result.py",
    "scripts/agriculture/identity-ci-result.test.py",
    "scripts/agriculture/export-python-lock.py",
    "scripts/agriculture/python-test-requirements.txt",
    "deploy/agriculture/platform-identity.template.json",
    "deploy/agriculture/gis-identity.template.json",
    "docs/agriculture/SERVICE_IDENTITY.md",
    "docs/agriculture/PLATFORM_SERVICE_IDENTITY.md",
    "docs/agriculture/GIS_SERVICE_IDENTITY.md",
    "tests/agriculture/fixtures/identity-canonical.json",
    "tests/agriculture/test_contracts.py",
    "tests/agriculture/test_python_lock_export.py",
    "contracts/agriculture/README.md",
    "contracts/agriculture/analysis-submit.schema.json",
    "contracts/agriculture/common.schema.json",
    "contracts/agriculture/delegation.schema.json",
    "contracts/agriculture/event-envelope.schema.json",
    "contracts/agriculture/job.schema.json",
    "contracts/agriculture/manifest.schema.json",
    "contracts/agriculture/result.schema.json",
    "contracts/agriculture/service-request.schema.json",
    "contracts/agriculture/source-rights.schema.json",
    "contracts/agriculture/source-rights.py",
    "contracts/agriculture/source-rights.template.json",
    "contracts/agriculture/openapi.json",
    "contracts/agriculture/examples/analysis-submit.json",
    "contracts/agriculture/examples/delegation-read.json",
    "contracts/agriculture/examples/delegation-write.json",
    "contracts/agriculture/examples/event-envelope.json",
    "contracts/agriculture/examples/job.json",
    "contracts/agriculture/examples/manifest.json",
    "contracts/agriculture/examples/result.json",
    "services/agri-gis/backend/app/main.py",
    "services/agri-gis/backend/app/agriculture/actor_models.py",
    "services/agri-gis/backend/app/agriculture/actor_session.py",
    "services/agri-gis/backend/app/agriculture/actors.py",
    "services/agri-gis/backend/app/agriculture/operation_authorization.py",
    "services/agri-gis/backend/app/services/project_user_service.py",
    "services/agri-gis/backend/app/services/service_sharing_service.py",
    "services/agri-gis/backend/app/services/workbench_service.py",
    "services/agri-gis/backend/app/dao/workbench_dao.py",
    "services/agri-gis/backend/scripts/migrations/20261029_agriculture_actor_bindings.sql",
    "services/agri-gis/backend/tests/test_agriculture_actors.py",
    "services/agri-gis/backend/tests/test_agriculture_actors_postgres.py",
    "services/agri-gis/backend/tests/test_legacy_actor_gate.py",
    "services/agri-gis/backend/tests/test_legacy_identity_contracts.py",
    "services/agri-gis/backend/tests/test_agriculture_legacy_write_integration.py",
    "services/agri-gis/backend/tests/test_boundary_delivery_services.py",
    "services/agri-gis/backend/tests/test_rule_config.py",
    "services/agri-gis/backend/tests/test_service_sharing.py",
    "services/agri-gis/backend/tests/test_workbench.py",
    "services/agri-gis/frontend/src/api/request.ts",
    "services/agri-gis/frontend/src/main.ts",
    "services/agri-gis/frontend/src/security/legacyIdentity.ts",
    "services/agri-gis/frontend/src/store/userStore.ts",
    "services/agri-gis/frontend/src/store/fieldCaptureStore.ts",
    "services/agri-gis/frontend/src/store/uavCaptureStore.ts",
    "services/agri-gis/frontend/src/components/layout/UserIdentitySwitcher.vue",
    "services/agri-gis/frontend/src/components/field/mobile/MobileFieldCaptureForm.vue",
    "services/agri-gis/frontend/src/components/uav/mobile/MobileUavCaptureForm.vue",
    "services/agri-gis/frontend/src/views/FieldCaptureView.vue",
    "services/agri-gis/frontend/src/views/UavCaptureView.vue",
    "services/agri-gis/frontend/agriculture/legacy-identity.test.ts",
    "scripts/agriculture/legacy-identity-inventory.py",
    "scripts/agriculture/legacy-identity-inventory.test.py",
    "docs/agriculture/evidence/w01/legacy-identity-inventory.json",
    "docs/agriculture/LEGACY_ACTOR_MIGRATION.md",
    "docs/agriculture/W01_LEGACY_ACTOR_GATE.md",
    "docs/agriculture/GIS_ACTOR_BINDINGS.md",
    "docs/agriculture/LEGACY_IDENTITY_INVENTORY.md",
    # DT014 same-origin production gateway, shared navigation and real browser gate.
    "contracts/agriculture/navigation.ts",
    "deploy/agriculture/Dockerfile.web",
    "deploy/agriculture/Dockerfile.web.dockerignore",
    "deploy/agriculture/nginx.conf.template",
    "deploy/agriculture/web-entrypoint.sh",
    "deploy/agriculture/env.example",
    "deploy/agriculture/errors/404.html",
    "deploy/agriculture/errors/50x.html",
    "deploy/agriculture/README.md",
    "docker-compose.agriculture.yml",
    "scripts/agriculture/gateway-browser-e2e.mjs",
    "scripts/agriculture/gateway-browser-e2e.test.mjs",
    "scripts/agriculture/gateway-container-smoke.py",
    "scripts/agriculture/gateway-container-smoke.test.py",
    "scripts/agriculture/native-gateway-test.py",
    "tests/agriculture/test_gateway_configuration.py",
    "docs/agriculture/SAME_ORIGIN_GATEWAY.md",
)


def extract_archive(archive, destination):
    with tarfile.open(archive) as source:
        for member in source:
            name = PurePosixPath(member.name)
            if name.is_absolute() or ".." in name.parts or ".git" in name.parts or not (member.isfile() or member.isdir()):
                raise ValueError("Unsafe or unsupported source archive member")
            target = destination.joinpath(*name.parts)
            if member.isdir():
                target.mkdir(parents=True, exist_ok=True)
            else:
                target.parent.mkdir(parents=True, exist_ok=True)
                with source.extractfile(member) as reader, target.open("wb") as writer:
                    shutil.copyfileobj(reader, writer)
                target.chmod(member.mode & 0o777)


def acquire(url, sha, destination, temporary):
    repository = temporary / sha
    repository.mkdir()
    subprocess.run(["git", "init", "--quiet", str(repository)], check=True)
    subprocess.run(["git", "-C", str(repository), "-c", "core.hooksPath=/dev/null", "fetch", "--no-tags", "--depth=1", url, sha], check=True)
    actual = subprocess.check_output(["git", "-C", str(repository), "rev-parse", "FETCH_HEAD"], text=True).strip()
    if actual != sha:
        raise ValueError("Source SHA does not match reviewed baseline")
    archive = temporary / f"{sha}.tar"
    subprocess.run(["git", "-C", str(repository), "archive", "--format=tar", f"--output={archive}", sha], check=True)
    destination.mkdir(parents=True, exist_ok=True)
    extract_archive(archive, destination)


def overlay(source, destination):
    entries = []
    for relative in OVERLAY:
        path = source / relative
        if not path.is_file() or path.is_symlink() or not path.resolve().is_relative_to(source.resolve()):
            raise ValueError(f"Missing or unsafe explicitly allowed overlay: {relative}")
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(path, target)
        entries.append({"path": relative, "sha256": hashlib.sha256(path.read_bytes()).hexdigest()})
    return entries


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--scaffold", type=Path)
    parser.add_argument("--destination", type=Path)
    parser.add_argument("--mode", choices=["scaffold", "full"])
    parser.add_argument("--list-overlay", action="store_true")
    args = parser.parse_args()
    if args.list_overlay:
        print(json.dumps(OVERLAY, indent=2))
        return
    if not all([args.scaffold, args.destination, args.mode]):
        parser.error("--scaffold --destination --mode are required")
    source, destination = args.scaffold.resolve(), args.destination.resolve()
    if destination.exists() or destination.is_relative_to(source) or source.is_relative_to(destination):
        raise ValueError("Destination must be a new independent directory")
    commit = subprocess.check_output(["git", "-C", str(source), "rev-parse", "HEAD"], text=True).strip()
    if args.mode == "full":
        for path in ["backend/go.mod", "services/agri-gis/frontend/package-lock.json", "services/loom-engine/flow/db.ts"]:
            if not (source / path).is_file():
                raise ValueError("Full-product mode requires actual integrated source checkout")
        source.rename(destination)
        manifest = {"mode": "actual_full_product_checkout", "source_commit": commit, "upstream_replaced": False}
    else:
        with tempfile.TemporaryDirectory(prefix="agriculture-ci-source-") as temp:
            acquire(*FLOW, destination, Path(temp))
            gis_destination = destination / "services/agri-gis"
            if gis_destination.exists():
                raise ValueError("Pinned Flow unexpectedly already contains GIS; refusing overwrite")
            acquire(*GIS, gis_destination, Path(temp))
        manifest = {"mode": "read_only_pinned_sources_plus_allowlisted_overlay", "source_commit": commit,
                    "flow_sha": FLOW[1], "gis_sha": GIS[1], "source_git_history_published": False,
                    "overlay": overlay(source, destination)}
    evidence = destination / ".local/evidence"
    evidence.mkdir(parents=True, exist_ok=True)
    (evidence / "session-ci-provenance.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({"mode": manifest["mode"], "source_commit": commit, "overlay_count": len(manifest.get("overlay", []))}))


if __name__ == "__main__":
    main()
