"""Fresh local PostgreSQL platform tests; no Docker, system install or deployment."""

import argparse
import hashlib
import json
import os
import secrets
import socket
import subprocess
import sys
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from native_test_environment import native_test_environment


REPO = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package-root", type=Path, required=True)
    parser.add_argument("--go", type=Path, required=True)
    args = parser.parse_args()
    allowed = (REPO / ".local").resolve()
    root, go = args.package_root.resolve(), args.go.resolve()
    run = REPO / ".local/native-membership" / ("run_" + uuid.uuid4().hex)
    if os.geteuid() == 0 or not allowed.is_relative_to(REPO.resolve()):
        raise SystemExit("non-root workspace execution required")
    if not all(p.resolve().is_relative_to(allowed) for p in (root, go, run)):
        raise SystemExit("fixture paths must stay in repository .local")
    manifest_path = REPO / "deploy/agriculture/native-postgres-packages.json"
    manifest = json.loads(manifest_path.read_text())
    receipt = json.loads((root / "prepared.json").read_text())
    def digest(data):
        return hashlib.sha256(data).hexdigest()
    if receipt["manifest_sha256"] != digest(manifest_path.read_bytes()):
        raise SystemExit("prepared package manifest mismatch")
    for relative, expected in receipt["files"].items():
        path = (root / relative).resolve()
        if not path.is_relative_to(root) or digest(path.read_bytes()) != expected:
            raise SystemExit("prepared package content mismatch")
    for relative, expected in receipt["links"].items():
        path = root / relative
        if not path.is_symlink() or not path.resolve().is_relative_to(root) or os.readlink(path) != expected:
            raise SystemExit("prepared package link mismatch")
    os.umask(0o077)
    run.mkdir(parents=True, exist_ok=False)
    passwords = {key: secrets.token_hex(24) for key in (
        "POSTGRES_PASSWORD", "PLATFORM_MIGRATION_PASSWORD", "PLATFORM_RUNTIME_PASSWORD",
        "INTEL_MIGRATION_PASSWORD", "INTEL_RUNTIME_PASSWORD", "GEO_MIGRATION_PASSWORD", "GEO_RUNTIME_PASSWORD", "GEO_WORKER_PASSWORD", "PLATFORM_SYSTEM_PASSWORD", "INTEL_SYSTEM_PASSWORD")}
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = str(sock.getsockname()[1])
    binary = root / "usr/lib/postgresql/17/bin"
    env = {**native_test_environment(os.environ, run), **passwords, "POSTGRES_USER": "postgres", "PGHOST": "127.0.0.1",
           "PGPORT": port, "PGPASSWORD": passwords["POSTGRES_PASSWORD"],
           "PATH": str(binary) + os.pathsep + os.environ["PATH"],
           "LD_LIBRARY_PATH": str(root / "usr/lib/x86_64-linux-gnu"),
           "GOCACHE": str(REPO / ".local/go-cache"), "GOMODCACHE": str(REPO / ".local/go-mod"),
           "GOPATH": str(REPO / ".local/go-path"), "AGRI_TEST_PG_ISOLATED": "1",
           "GOTOOLCHAIN": "local", "GOPROXY": "off"}
    # Only the migration subprocess receives a generic DATABASE_URL.
    env.pop("DATABASE_URL", None)
    env.pop("CANVAS_DATABASE_DRIVER", None)
    for kind, key, target in [("migrate", "PLATFORM_MIGRATION_PASSWORD", "AGRI_TEST_PG_MIGRATION_DSN"),
                              ("app", "PLATFORM_RUNTIME_PASSWORD", "AGRI_TEST_PG_RUNTIME_DSN")]:
        env[target] = f"postgres://flow_platform_{kind}:{passwords[key]}@127.0.0.1:{port}/flow_platform?sslmode=disable"
    pw = run / "bootstrap-password"
    pw.write_text(passwords["POSTGRES_PASSWORD"] + "\n")
    report = {"kind": "local_real_platform_postgres_fixture", "remote_ci": False,
              "fresh_cluster": True, "production_deployment": False, "browser_test": False,
              "started_at": datetime.now(timezone.utc).isoformat(), "packages": manifest["packages"],
              "source_commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=REPO, text=True).strip(),
              "commands": [], "checks": [], "test_reports": {}, "result": "failed"}
    attempted = False
    stage = "initdb"
    started_at = time.monotonic()
    with (run / "private.log").open("w") as log:
        def command(argv, custom_env=None):
            return subprocess.run([str(a) for a in argv], cwd=REPO, env=custom_env or env,
                                  stdout=log, stderr=log, timeout=300, check=True)

        def sql(domain, kind, query, database=None, denied=False):
            key = f"{domain.upper()}_{'MIGRATION' if kind == 'migrate' else 'RUNTIME'}_PASSWORD"
            result = subprocess.run([str(binary / "psql"), "-X", "-qAt", "-v", "ON_ERROR_STOP=1",
                                     "-U", f"flow_{domain}_{kind}", "-d", database or f"flow_{domain}", "-c", query],
                                    env={**env, "PGPASSWORD": passwords[key]}, stdout=subprocess.PIPE,
                                    stderr=log, text=True, timeout=15)
            if denied:
                assert result.returncode != 0, "forbidden cross-database connection succeeded"
            else:
                assert result.returncode == 0, "SQL probe failed"
            return result.stdout.strip()

        try:
            command([binary / "initdb", "-D", run / "cluster", "-L", root / "usr/share/postgresql/17",
                     "-U", "postgres", "--pwfile", pw, "--auth=scram-sha-256", "--no-locale", "--encoding=UTF8"])
            attempted = True
            command([binary / "pg_ctl", "-D", run / "cluster", "-l", run / "server.log", "-o",
                     f"-h 127.0.0.1 -p {port} -c unix_socket_directories='' -c jit=off", "-w", "start"])
            stage = "production_init_and_role_checks"
            command(["sh", REPO / "deploy/agriculture/postgres/init.sh"])
            for domain in ("platform", "intel", "geo"):
                for kind in ("migrate", "app"):
                    assert sql(domain, kind, "SELECT current_user") == f"flow_{domain}_{kind}"
                    assert sql(domain, kind, "SELECT NOT (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole OR rolreplication) FROM pg_roles WHERE rolname=current_user") == "t"
                    if kind == "app":
                        assert sql(domain, kind, "SELECT datdba <> (SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_database WHERE datname=current_database()") == "t"
                        assert sql(domain, kind, f"SELECT NOT pg_has_role(current_user,'flow_{domain}_migrate','MEMBER') AND NOT has_schema_privilege(current_user,'public','CREATE')") == "t"
                    for other in ("platform", "intel", "geo"):
                        if other != domain:
                            sql(domain, kind, "SELECT 1", f"flow_{other}", denied=True)
            assert sql("platform", "migrate", "SELECT NOT pg_has_role(current_user,'flow_platform_admission','USAGE') AND pg_has_role(current_user,'flow_platform_admission','SET')") == "t"
            assert sql("geo", "app", "SELECT ST_SRID(ST_SetSRID(ST_Point(0,0),4326))") == "4326"
            report["checks"] += ["six_restricted_database_roles", "runtime_not_owner_no_set_role", "cross_database_denied", "admission_membership_no_inherit", "postgis"]
            report["postgres"] = sql("platform", "app", "SELECT version()")
            report["postgis"] = sql("geo", "app", "SELECT postgis_lib_version()")
            report["go"] = subprocess.check_output([str(go), "version"], env=env, text=True).strip()
            stage = "real_platform_migrations"
            migrate_env = {**env, "CANVAS_DATABASE_DRIVER": "postgres", "DATABASE_URL": env["AGRI_TEST_PG_MIGRATION_DSN"]}
            argv = [go, "-C", "backend", "run", "./cmd/migrate-schema", "up"]
            command(argv, migrate_env)
            query = "SELECT md5(string_agg(version::text || ':' || checksum, ',' ORDER BY version)) FROM schema_migrations"
            before = sql("platform", "migrate", query)
            for version in (82, 83):
                assert sql("platform", "migrate", f"SELECT count(*) FROM schema_migrations WHERE version={version}") == "1"
            command(argv, migrate_env)
            assert sql("platform", "migrate", query) == before
            command([go, "-C", "backend", "run", "./cmd/migrate-schema", "verify"], migrate_env)
            report["checks"].append("migration_82_83_applied_replayed_verified")
            report["commands"] += ["go -C backend run ./cmd/migrate-schema up (twice)", "go -C backend run ./cmd/migrate-schema verify"]
            for suite, packages, pattern in [
                ("postgres", ["./internal/database", "./internal/agriculture"], "^TestAgriculturePostgres"),
                ("legacy-flow", ["./internal/flow"], "^Test(OrgInviteSingleUseAndGrantRevocation|P7MembersRoleEditing|AgricultureMembership)")]:
                stage = suite
                argv = [str(go), "-C", "backend", "test", "-p", "1", "-count=1", "-json", *packages, "-run", pattern]
                report["commands"].append("go " + " ".join(argv[1:]))
                raw = subprocess.run(argv, cwd=REPO, env=env, stdout=subprocess.PIPE, stderr=log, timeout=300)
                output = run / (suite + ".json")
                reduced = subprocess.run([sys.executable, "scripts/agriculture/membership-ci-result.py",
                                          "--suite", suite, "--output", str(output)], cwd=REPO,
                                         input=raw.stdout, stdout=log, stderr=log, timeout=30)
                report["test_reports"][suite] = json.loads(output.read_text()) if output.exists() else {"passed": False}
                assert raw.returncode == 0 and reduced.returncode == 0, "test or mandatory no-skip reducer failed"
            report["result"] = "pass"
        except Exception as error:
            # Never print exception text: subprocess arguments/driver messages may contain secrets.
            report["failure"] = {"stage": stage, "type": type(error).__name__}
        finally:
            try:
                if attempted:
                    stopped = subprocess.run([str(binary / "pg_ctl"), "-D", str(run / "cluster"), "-m", "fast", "-w", "stop"],
                                             env=env, stdout=log, stderr=log, timeout=30)
                    report["shutdown_exit_code"] = stopped.returncode
                    if stopped.returncode:
                        report["result"] = "failed"
            finally:
                pw.unlink(missing_ok=True)
    scope = ["scripts/agriculture/native-membership-test.py", "scripts/agriculture/membership-ci-result.py",
             "scripts/agriculture/native_test_environment.py", "tests/agriculture/test_native_test_environment.py",
             "deploy/agriculture/postgres/init.sh", "deploy/agriculture/native-postgres-packages.json",
             "backend/go.mod", "backend/go.sum"]
    scope += [str(p.relative_to(REPO)) for p in (REPO / "backend").rglob("*.go") if ".local" not in p.parts]
    report["source_files"] = {p: digest((REPO / p).read_bytes()) for p in sorted(set(scope))}
    report["worktree_changes_included"] = True
    report["elapsed_seconds"] = round(time.monotonic() - started_at, 3)
    report["finished_at"] = datetime.now(timezone.utc).isoformat()
    report["exit_code"] = int(report["result"] != "pass")
    (run / "report.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"result": report["result"], "failure": report.get("failure"), "report": str(run / "report.json")}))
    return report["exit_code"]


if __name__ == "__main__":
    sys.exit(main())
