"""Fixture configuration/route resource boundaries, not live chain acceptance."""

import json

import pytest
from identity_chain_fixture import DatabaseOwnership, read_private_config, safe_failure

from app.agriculture.identity.verify import IdentityError


def test_failure_diagnostic_excludes_driver_message_and_arguments():
    try:
        raise ValueError("private_dsn_sentinel")
    except ValueError as error:
        record = safe_failure(error)
    assert record["type"] == "ValueError" and record["line"] > 0
    assert record["file"] == "test_identity_chain_fixture.py"
    assert "private_dsn_sentinel" not in json.dumps(record)


def config_file(tmp_path):
    value = {
        "contexts": [
            {
                "tenant": "tenant_a",
                "workspace": "workspace_a",
                "actor": "1",
                "role": "interpreter",
            }
        ],
        "geo_migration_dsn": "postgresql://fixture@127.0.0.1/fixture",
        "geo_runtime_dsn": "postgresql://fixture@[::1]/fixture",
    }
    path = tmp_path / "config.json"
    path.write_text(json.dumps(value))
    path.chmod(0o600)
    return path, value


def test_private_config_requires_exact_private_regular_file(tmp_path):
    path, value = config_file(tmp_path)
    assert read_private_config(path) == value
    path.chmod(0o644)
    with pytest.raises(ValueError):
        read_private_config(path)
    path.chmod(0o600)
    link = tmp_path / "link.json"
    link.symlink_to(path)
    with pytest.raises(OSError):
        read_private_config(link)


@pytest.mark.parametrize(
    "host", ["localhost", "10.0.0.1", "example.invalid", "[::1%lo]"]
)
def test_fixture_never_connects_nonliteral_or_external_database(tmp_path, host):
    path, value = config_file(tmp_path)
    value["geo_runtime_dsn"] = f"postgresql://fixture@{host}/fixture"
    path.write_text(json.dumps(value))
    with pytest.raises(ValueError):
        read_private_config(path)


def test_duplicate_workspace_fixture_is_not_accepted(tmp_path):
    path, value = config_file(tmp_path)
    value["contexts"] *= 2
    path.write_text(json.dumps(value))
    with pytest.raises(ValueError):
        read_private_config(path)


@pytest.mark.parametrize(
    "query", ["host=10.0.0.1", "hostaddr=127.0.0.2", "sslmode=require"]
)
def test_fixture_database_connection_overrides_are_rejected(tmp_path, query):
    path, value = config_file(tmp_path)
    value["geo_runtime_dsn"] += "?" + query
    path.write_text(json.dumps(value))
    with pytest.raises(ValueError):
        read_private_config(path)


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "body",
    [
        {
            "task_code": "task_a",
            "plot_code": "plot_a",
            "crop_type": "corn",
            "operator_code": "admin",
        },
        {
            "task_code": "task_a",
            "plot_code": "plot_a",
            "crop_type": "corn",
            "tenant_id": "other",
        },
        {
            "task_code": "task_a",
            "plot_code": "plot_a",
            "crop_type": "corn",
            "resources": [],
        },
        {"task_code": "task_a", "plot_code": "plot_a", "crop_type": 7},
    ],
)
async def test_route_refuses_identity_or_resource_parameters_before_database(body):
    with pytest.raises(IdentityError) as error:
        await DatabaseOwnership(None).target(body)
    assert error.value.status == 400
