"""Owner-command component tests; PostgreSQL acceptance is a separate module."""

import pytest

from app.agriculture import migrate


def test_fixed_migrations_have_one_outer_transaction_and_hash_original_bytes():
    inputs = migrate.migration_inputs()
    assert [row[0] for row in inputs] == list(migrate.MIGRATIONS)
    assert all(len(digest) == 64 for _, digest, _ in inputs)
    assert all(not sql.strip().endswith("COMMIT;") for _, _, sql in inputs)
    assert all("BEGIN;\n" not in sql for _, _, sql in inputs)


@pytest.mark.asyncio
@pytest.mark.parametrize("dsn,mode", [(None, "new"), ("", "existing"), ("x", "auto")])
async def test_invalid_configuration_rejected_before_connection(dsn, mode):
    with pytest.raises(migrate.MigrationError, match="migration_configuration_invalid"):
        await migrate.migrate_schema(dsn, mode=mode)


def test_cli_requires_explicit_mode_and_never_uses_runtime_dsn(monkeypatch, capsys):
    monkeypatch.setattr("sys.argv", ["migrate", "--mode", "new"])
    monkeypatch.delenv("FLOW_GEO_MIGRATION_DATABASE_URL", raising=False)
    monkeypatch.setenv("DATABASE_URL", "never-connect-to-this-runtime-value")
    assert migrate.main() == 1
    assert '"code": "migration_configuration_invalid"' in capsys.readouterr().out
