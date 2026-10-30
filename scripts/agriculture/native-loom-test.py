"""Fresh workspace-only Loom PostgreSQL boundary fixture; no production services."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import socket
import subprocess
import sys
import time
import uuid
import xml.etree.ElementTree as ET

from native_test_environment import native_test_environment

REPO = Path(__file__).resolve().parents[2]
REQUIRED = {
    "migration applies and repeats without changing ledger",
    "two scoped tenants create sources and ordinary unscoped access denies",
    "system scanner has only bounded tenant metadata and no table writes or content",
    "pooled scope reset rollback and escaped transaction handles fail closed",
    "scoped collection claim commit cursor and ordered at least once outbox",
    "scheduler report and enrichment loops use explicit tenant scopes",
    "real HTTP and Agent parameters cannot select system scope or pool",
    "media retry writes real scoped rows and transactional outbox with fixture transport",
}
LEGACY_REQUIRED = {
    "RLS: tenant context hides and blocks other tenants' rows (non-superuser role)",
    "A18: backup/restore dump \u2192 restore into an empty database \u2192 manifest and app reads match",
    "RLS: HTTP API end-to-end on real Postgres as non-superuser",
    "legacy import (PGlite): analyses, notes, schedules, objects; dry run; idempotent",
    "legacy import (real PostgreSQL, FORCE RLS app role)",
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package-root", type=Path, required=True)
    parser.add_argument("--legacy", action="store_true", help="also execute original isolated backup/import/RLS regressions")
    args = parser.parse_args()
    root = args.package_root.resolve()
    allowed = (REPO / ".local").resolve()
    run = allowed / "native-loom" / ("run_" + uuid.uuid4().hex)
    if os.geteuid() == 0 or not allowed.is_relative_to(REPO.resolve()) or not root.is_relative_to(allowed):
        raise SystemExit("non-root prepared workspace package required")
    manifest_path = REPO / "deploy/agriculture/native-postgres-packages.json"
    receipt = json.loads((root / "prepared.json").read_text())

    def digest(path):
        return hashlib.sha256(path.read_bytes()).hexdigest()

    if receipt["manifest_sha256"] != digest(manifest_path):
        raise SystemExit("package manifest mismatch")
    for relative, expected in receipt["files"].items():
        path = (root / relative).resolve()
        if not path.is_relative_to(root) or digest(path) != expected:
            raise SystemExit("prepared package mismatch")
    for relative, expected in receipt["links"].items():
        path = root / relative
        if not path.is_symlink() or not path.resolve().is_relative_to(root) or os.readlink(path) != expected:
            raise SystemExit("prepared link mismatch")
    node = shutil.which("node")
    if not node:
        raise SystemExit("Node required")
    os.umask(0o077)
    run.mkdir(parents=True, exist_ok=False)
    base_env = native_test_environment(os.environ, run)
    keys = ("POSTGRES_PASSWORD", "PLATFORM_MIGRATION_PASSWORD", "PLATFORM_RUNTIME_PASSWORD",
            "INTEL_MIGRATION_PASSWORD", "INTEL_RUNTIME_PASSWORD", "GEO_MIGRATION_PASSWORD",
            "GEO_RUNTIME_PASSWORD", "GEO_WORKER_PASSWORD", "PLATFORM_SYSTEM_PASSWORD",
            "INTEL_SYSTEM_PASSWORD")
    passwords = {key: secrets.token_hex(24) for key in keys}
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = str(sock.getsockname()[1])
    binary = root / "usr/lib/postgresql/17/bin"
    env = {**base_env, **passwords, "POSTGRES_USER": "postgres", "PGHOST": "127.0.0.1",
           "PGPORT": port, "PGPASSWORD": passwords["POSTGRES_PASSWORD"],
           "PATH": str(binary) + os.pathsep + base_env["PATH"],
           "LD_LIBRARY_PATH": str(root / "usr/lib/x86_64-linux-gnu")}
    test_env = dict(base_env)
    for role, key, variable in (("migrate", "INTEL_MIGRATION_PASSWORD", "MIGRATION"),
                                ("app", "INTEL_RUNTIME_PASSWORD", "APP"),
                                ("system", "INTEL_SYSTEM_PASSWORD", "SYSTEM")):
        test_env[f"LOOM_SCOPE_TEST_{variable}_URL"] = f"postgres://flow_intel_{role}:{passwords[key]}@127.0.0.1:{port}/flow_intel?sslmode=disable"
    pw = run / "bootstrap-password"
    pw.write_text(passwords["POSTGRES_PASSWORD"] + "\n")
    required = REQUIRED | (LEGACY_REQUIRED if args.legacy else set())
    report = {"kind": "local_real_loom_postgres_fixture", "fresh_cluster": True,
              "production_deployment": False, "remote_ci": False, "passed": False,
              "required_tests": sorted(required), "tests": [], "source_files": {}}
    attempted = False
    stage = "initdb"
    started = time.monotonic()
    with (run / "private.log").open("w") as log:
        def command(argv, command_env=env, cwd=REPO, timeout=90):
            return subprocess.run([str(a) for a in argv], env=command_env, cwd=cwd,
                                  stdout=log, stderr=log, check=True, timeout=timeout)
        try:
            command([binary / "initdb", "-D", run / "cluster", "-L", root / "usr/share/postgresql/17",
                     "-U", "postgres", "--pwfile", pw, "--auth=scram-sha-256", "--no-locale", "--encoding=UTF8"])
            attempted = True
            command([binary / "pg_ctl", "-D", run / "cluster", "-l", run / "server.log", "-o",
                     f"-h 127.0.0.1 -p {port} -c unix_socket_directories='' -c jit=off", "-w", "start"])
            stage = "production_role_bootstrap"
            command(["sh", REPO / "deploy/agriculture/postgres/init.sh"])
            stage = "real_loom_scope_tests"
            result = subprocess.run([node, "--experimental-strip-types", "--test", "--test-reporter=junit",
                                     "--test-reporter-destination=" + str(run / "tests.xml"), "flow/scope-postgres.test.ts"],
                                    cwd=REPO / "services/loom-engine", env=test_env, stdout=log, stderr=log, timeout=180)
            report["test_exit_code"] = result.returncode
            cases = list(ET.parse(run / "tests.xml").getroot().iter("testcase"))
            if args.legacy:
                # Only this explicit isolated legacy-admin fixture receives its own cluster admin.
                # Runtime behavior still uses the non-owner app role; backups use operator access.
                stage = "legacy_backup_import_rls_tests"
                legacy_env = {**base_env, "PATH": env["PATH"], "LD_LIBRARY_PATH": env["LD_LIBRARY_PATH"],
                    "FLOW_TEST_PG_ADMIN_URL": f"postgres://postgres:{passwords['POSTGRES_PASSWORD']}@127.0.0.1:{port}/postgres?sslmode=disable",
                    "FLOW_TEST_PG_APP_ROLE": "flow_intel_app:" + passwords["INTEL_RUNTIME_PASSWORD"]}
                legacy = subprocess.run([node, "--experimental-strip-types", "--test", "--test-concurrency=1", "--test-reporter=junit",
                                         "--test-reporter-destination=" + str(run / "legacy.xml"), "flow/pg.test.ts", "flow/legacy-import-full.test.ts"],
                                        cwd=REPO / "services/loom-engine", env=legacy_env, stdout=log, stderr=log, timeout=180)
                report["legacy_exit_code"] = legacy.returncode
                cases += list(ET.parse(run / "legacy.xml").getroot().iter("testcase"))
            for case in cases:
                name = case.attrib.get("name", "")
                if name not in required:
                    continue
                status = "pass" if not any(c.tag in {"failure", "error", "skipped"} for c in case) else "not_passed"
                report["tests"].append({"name": name, "status": status})
            report["passed"] = result.returncode == 0 and report.get("legacy_exit_code", 0) == 0 and len(report["tests"]) == len(required) and all(c["status"] == "pass" for c in report["tests"])
        except Exception as error:
            report["failure"] = {"stage": stage, "type": type(error).__name__}
        finally:
            try:
                if attempted:
                    stopped = subprocess.run([str(binary / "pg_ctl"), "-D", str(run / "cluster"), "-m", "fast", "-w", "stop"],
                                             env=env, stdout=log, stderr=log, timeout=30)
                    report["shutdown_exit_code"] = stopped.returncode
                    report["passed"] = report["passed"] and stopped.returncode == 0
            finally:
                pw.unlink(missing_ok=True)
    paths = [p for p in (REPO / "services/loom-engine/flow").rglob("*") if p.suffix in {".ts", ".sql"}]
    paths += [Path(__file__), REPO / "scripts/agriculture/native_test_environment.py",
              REPO / "deploy/agriculture/postgres/init.sh", manifest_path,
              REPO / "services/loom-engine/package-lock.json"]
    report["source_files"] = {str(p.relative_to(REPO)): digest(p) for p in sorted(paths)}
    report["source_commit"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=REPO, text=True).strip()
    report["worktree_changes_included"] = True
    report["elapsed_seconds"] = round(time.monotonic() - started, 3)
    (run / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"passed": report["passed"], "failure": report.get("failure"), "report": str(run / "report.json")}))
    return int(not report["passed"])


if __name__ == "__main__":
    sys.exit(main())
