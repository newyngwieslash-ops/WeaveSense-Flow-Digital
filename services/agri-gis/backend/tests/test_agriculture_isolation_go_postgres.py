"""Real platform/GIS PostgreSQL isolation; strict non-skipped Go suite bridge."""

import json
import os
import re
import subprocess
from pathlib import Path
from uuid import uuid4

from test_agriculture_actors_postgres import pg_config
from test_agriculture_jobs_postgres import configured_go_mod_cache


def run_go(root_test, children, suite):
    pg_config()
    root = Path(__file__).resolve().parents[4]
    for key in (
        "AGRI_TEST_PG_MIGRATION_DSN",
        "AGRI_TEST_PG_RUNTIME_DSN",
        "AGRI_TEST_GO_BIN",
        "AGRI_IDENTITY_PYTHON",
    ):
        assert os.environ.get(key), (
            "required isolated platform dependency missing: " + key
        )
    component = os.getenv("AGRI_ISOLATION_AUTH_COMPONENT_ONLY", "0")
    assert component in {"0", "1"}
    race = os.getenv("AGRI_ISOLATION_GO_RACE", "0")
    assert race in {"0", "1"}
    if component == "0":
        assert os.environ.get("REDIS_URL"), "full CI isolation requires real Redis"
    private = root / ".local/isolation-private-runs" / uuid4().hex
    private.mkdir(parents=True, mode=0o700)
    report_dir = Path(os.getenv("AGRI_ISOLATION_REPORT_DIR", root / ".local/evidence"))
    report_dir = report_dir.resolve()
    assert report_dir.is_relative_to(
        (root / ".local").resolve()
    ), "unsafe isolation evidence path"
    report_dir.mkdir(parents=True, exist_ok=True)
    target = report_dir / ("isolation-go-" + suite + ".json")
    target.write_text('{"passed":false,"status":"not_run"}\n')
    keys = (
        "PATH",
        "HOME",
        "TMPDIR",
        "LANG",
        "LC_ALL",
        "LD_LIBRARY_PATH",
        "AGRI_TEST_PG_ISOLATED",
        "AGRI_TEST_PG_MIGRATION_DSN",
        "AGRI_TEST_PG_RUNTIME_DSN",
        "AGRI_TEST_GEO_MIGRATION_DSN",
        "AGRI_TEST_GEO_RUNTIME_DSN",
        "AGRI_TEST_GEO_WORKER_DSN",
        "AGRI_IDENTITY_PYTHON",
        "REDIS_URL",
    )
    env = {key: os.environ[key] for key in keys if key in os.environ}
    env.update(
        {
            "GOMODCACHE": configured_go_mod_cache(
                root, os.getenv("AGRI_TEST_GO_MOD_CACHE")
            ),
            "GOCACHE": str(root / ".local/cache/go"),
            "GOTOOLCHAIN": "local",
            "GOPROXY": "off",
            "AGRI_ISOLATION_AUTH_COMPONENT_ONLY": component,
            "AGRI_ISOLATION_REQUIRED": "1",
            "AGRI_ISOLATION_MATRIX_REPORT": str(
                report_dir / "isolation-routes-go.json"
            ),
        }
    )
    with (private / "go.jsonl").open("wb") as log:
        result = subprocess.run(
            [
                os.environ["AGRI_TEST_GO_BIN"],
                "test",
                *(["-race"] if race == "1" else []),
                "-json",
                "./internal/agriculture",
                "-run",
                "^" + root_test + "$",
                "-count=1",
            ],
            cwd=root / "backend",
            env=env,
            stdout=log,
            stderr=log,
            timeout=300,
        )
    states, locations = {}, set()
    for line in (private / "go.jsonl").read_text(errors="replace").splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if event.get("Test") and event.get("Action") in {"pass", "fail", "skip"}:
            states[event["Test"]] = event["Action"]
        # Fixed source location only; never raw failure output or credentials.
        for match in re.finditer(
            r"\b([a-z_]+_test\.go):(\d+):", event.get("Output", "")
        ):
            locations.add(match.group(1) + ":" + match.group(2))
    required = {root_test} | {root_test + "/" + child for child in children}
    missing = sorted(
        required - {name for name, state in states.items() if state == "pass"}
    )
    passed = (
        result.returncode == 0
        and not missing
        and all(s == "pass" for s in states.values())
    )
    report = {
        "suite": suite,
        "passed": passed,
        "exit_code": result.returncode,
        "auth_component_only": component == "1",
        "data_class": "fixture",
        "race_enabled": race == "1",
        "tests": states,
        "missing_required": missing,
        "source_failure_locations": sorted(locations) if not passed else [],
        "production_acceptance": False,
    }
    target.write_text(json.dumps(report, indent=2) + "\n")
    assert passed, "isolation suite failed: " + json.dumps(report, sort_keys=True)


def test_isolation_go_route_matrix():
    public = [f"P{i:02}" for i in range(1, 44)]
    scope = [f"P{i:02}" for i in (2, 3, 4, 5, 8, 9, 10, 11, 12, 13, 14, 15, 16)]
    children = [
        p + "/" + case
        for p in public
        for case in ("anonymous", "forged_session", "expired_session")
    ]
    children += [
        p + "/" + case
        for p in scope
        for case in ("foreign_tenant", "unjoined_same_tenant_workspace", "viewer_role")
    ]
    children += [p + "_revoked_member" for p in scope] + [
        p + "_logged_out" for p in public
    ]
    farms = [f"P{i:02}" for i in range(20, 44)]
    children += [p + "/" + case for p in farms for case in (
        "foreign_tenant", "unjoined_same_tenant_workspace", "current_scope_boundary"
    )]
    children += [p + "_revoked_member" for p in farms]
    children += [p + "/production_provider_closed" for p in ("P17", "P18", "P19")]
    children += [
        "P01/scope_filter",
        "P06/forged_body_identity",
        "P07/wrong_recipient",
        "P07/accepted_token_replay_by_other_user",
        "P02_positive_read",
        "P13_positive_read",
        "P03_admin_control",
        "P08_admin_control",
        "P14_admin_control",
        "P06_idempotent_owner_control",
    ]
    children += [
        p + "/" + case
        for p in ("I01", "I02", "I03", "I04", "I05", "I06")
        for case in ("anonymous", "forged_session")
    ]
    children += [
        p + "_" + case
        for p in ("I01", "I02", "I03", "I05")
        for case in (
            "signed_control",
            "wrong_signer",
            "signed_scope_mismatch",
            "wrong_direction",
            "revoked_actor",
        )
    ]
    children += [
        "I04_" + case for case in (
            "signed_missing_intent", "wrong_signer", "signed_scope_mismatch",
            "wrong_direction", "revoked_actor",
        )
    ]
    children += [
        "I06_signed_missing_asset",
        "I06_wrong_signer",
        "I06_signed_scope_mismatch",
        "I06_wrong_direction",
        "I06_revoked_actor",
        "I01_stale_epoch",
        "I01_unjoined_workspace",
        "I02_substituted_snapshot",
    ]
    run_go("TestAgricultureIsolationMatrixPostgres", children, "matrix")


def test_isolation_go_double_postgres_objects():
    run_go(
        "TestObjectGatewayDoublePostgresIsolation",
        [
            "session_get_head_ranges",
            "scope_and_current_resource_withdrawal",
            "platform_membership_and_object_revocation",
        ],
        "objects",
    )


def test_isolation_go_postgres_projections():
    run_go(
        "TestIsolationProjectionPostgres",
        [
            "cache_scope_keys_and_revision",
            "cache_revocation_and_inflight_fill",
            "sse_cursor_and_revocation",
            "sse_every_frame_logout",
            "pool_concurrent_scope_and_empty_denial",
        ],
        "projections",
    )
