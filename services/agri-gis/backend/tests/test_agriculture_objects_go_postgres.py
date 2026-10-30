"""Actual cookie-session Go gateway → signed Python/PostgreSQL private objects.

The HTTP-contract peer tests are separately named fixtures; only the PostgreSQL
root is the cross-language store chain. Raw subprocess logs remain private.
"""

import json
import os
import subprocess
import tempfile
from pathlib import Path

import pytest
from test_agriculture_actors_postgres import pg_config
from test_agriculture_jobs_postgres import configured_go_mod_cache


def test_real_go_python_object_gateway_chain():
    pg_config()
    go = os.environ.get("AGRI_TEST_GO_BIN")
    python = os.environ.get("AGRI_IDENTITY_PYTHON")
    if not go or not python:
        pytest.fail("required explicit Go/Python executables missing")
    root = Path(__file__).resolve().parents[4]
    module_cache = configured_go_mod_cache(root, os.getenv("AGRI_TEST_GO_MOD_CACHE"))
    with tempfile.TemporaryDirectory(prefix="object-chain-") as private:
        output = Path(private) / "go.json"
        with output.open("wb") as log:
            result = subprocess.run(
                [
                    go,
                    "test",
                    "-json",
                    "./internal/agriculture",
                    "-run",
                    "^(TestObjectGatewayRealSessionAndStrictTransport|TestServiceAssertionsNormalizeNonUTCClock|TestObjectGatewayRealHTTPPostgres)$",
                    "-count=1",
                ],
                cwd=root / "backend",
                env={
                    **{
                        key: os.environ[key]
                        for key in (
                            "PATH",
                            "HOME",
                            "TMPDIR",
                            "LANG",
                            "LC_ALL",
                            "LD_LIBRARY_PATH",
                            "AGRI_TEST_PG_ISOLATED",
                            "AGRI_TEST_GEO_MIGRATION_DSN",
                            "AGRI_TEST_GEO_RUNTIME_DSN",
                            "AGRI_TEST_GEO_WORKER_DSN",
                            "AGRI_IDENTITY_PYTHON",
                        )
                        if key in os.environ
                    },
                    "GOMODCACHE": module_cache,
                    "GOCACHE": str(root / ".local/go-cache"),
                    "GOTOOLCHAIN": "local",
                    "GOPROXY": "off",
                },
                stdout=log,
                stderr=log,
                timeout=120,
            )
        states = {}
        for line in output.read_text(errors="replace").splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if event.get("Test") and event.get("Action") in {"pass", "fail", "skip"}:
                states[event["Test"]] = event["Action"]
        required = {"TestObjectGatewayRealSessionAndStrictTransport", "TestServiceAssertionsNormalizeNonUTCClock", "TestObjectGatewayRealHTTPPostgres"}
        for parent, children in {
            "TestObjectGatewayRealSessionAndStrictTransport": (
                "actual_session_get_head_range_and_filtered_headers",
                "malformed_request_and_session_fail_before_domain_download",
                "strict_metadata_and_bytes_contract",
                "foreign_scope_and_revocation_rechecked",
            ),
            "TestObjectGatewayRealHTTPPostgres": (
                "session_get_head_ranges",
                "scope_and_current_resource_withdrawal",
                "platform_membership_and_object_revocation",
            ),
        }.items():
            required.update(parent + "/" + child for child in children)
        assert result.returncode == 0 and all(
            states.get(name) == "pass" for name in required
        ) and all(state == "pass" for state in states.values()), (
            "object gateway required roots missing/failed/skipped: "
            + json.dumps(states, sort_keys=True)
        )
