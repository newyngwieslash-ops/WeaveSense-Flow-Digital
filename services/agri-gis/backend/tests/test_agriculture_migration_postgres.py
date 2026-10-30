"""Fresh isolated PostgreSQL command acceptance; no production data or fixtures."""

import asyncio
import json
import os
import re
import sys
from pathlib import Path
from uuid import uuid4

import asyncpg
import pytest
from test_agriculture_actors_postgres import pg_config

from app.agriculture import migrate


async def command(dsn, mode):
    env = {
        key: os.environ[key] for key in ("PATH", "LANG", "LC_ALL") if key in os.environ
    }
    env["FLOW_GEO_MIGRATION_DATABASE_URL"] = dsn
    process = await asyncio.create_subprocess_exec(
        sys.executable,
        "-m",
        "app.agriculture.migrate",
        "--mode",
        mode,
        cwd=Path(__file__).parents[1],
        env=env,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
    )
    stdout, _ = await asyncio.wait_for(process.communicate(), timeout=180)
    # Deliberately do not attach stderr/DSN to test failures or reports.
    result = json.loads(stdout)
    diagnostic = {
        key: value
        for key in ("code", "phase", "sqlstate")
        if isinstance(value := result.get(key), str)
        and re.fullmatch(r"[a-zA-Z0-9_.-]{1,100}", value)
    }
    assert process.returncode == 0, "migration command failed: " + json.dumps(
        diagnostic
    )
    return result


@pytest.mark.asyncio
async def test_postgres_migration_command_new_repeat_and_drift(monkeypatch):
    owner_dsn, runtime_dsn = pg_config()
    owner = await asyncpg.connect(owner_dsn)
    try:
        assert not await owner.fetch(
            migrate.APPLICATION_TABLES
        ), "fresh isolated database required"
        first = await command(owner_dsn, "new")
        again = await command(owner_dsn, "existing")
        assert first["passed"] and again["passed"]
        assert (
            first["legacy_tables"] == 93 and first["migrations"] == again["migrations"]
        )
        assert (
            await owner.fetchval(
                "SELECT count(*) FROM public.agriculture_schema_migrations"
            )
            == 3
        )
        assert (
            await owner.fetchval(
                "SELECT count(*) FROM public.agriculture_legacy_plot_ownership"
            )
            == 0
        )
        for role in ("flow_geo_app", "flow_geo_worker"):
            for privilege in ("SELECT", "INSERT", "UPDATE", "DELETE"):
                assert not await owner.fetchval(
                    "SELECT has_table_privilege($1,"
                    "'public.agriculture_schema_migrations',$2)",
                    role,
                    privilege,
                )
        with pytest.raises(migrate.MigrationError, match="new_requires_empty"):
            await migrate.migrate_schema(owner_dsn, mode="new")
        with pytest.raises(migrate.MigrationError, match="migration_owner_required"):
            await migrate.migrate_schema(runtime_dsn, mode="existing")
        before = await owner.fetch(
            "SELECT * FROM public.agriculture_schema_migrations ORDER BY name"
        )
        await owner.execute(
            "ALTER TABLE public.monitoring_projects "
            "ADD COLUMN migration_test_drift integer"
        )
        try:
            with pytest.raises(
                migrate.MigrationError, match="unsupported_legacy_column_drift"
            ):
                await migrate.migrate_schema(owner_dsn, mode="existing")
        finally:
            await owner.execute(
                "ALTER TABLE public.monitoring_projects "
                "DROP COLUMN migration_test_drift"
            )
        assert (
            await owner.fetch(
                "SELECT * FROM public.agriculture_schema_migrations ORDER BY name"
            )
            == before
        )
        # Same table/column names in another schema must not masquerade as public.
        schema = "agriculture_migration_test_" + uuid4().hex
        foreign_key = await owner.fetchrow(
            "SELECT quote_ident(conname) AS name,pg_get_constraintdef(oid) AS ddl "
            "FROM pg_catalog.pg_constraint WHERE contype='f' "
            "AND conrelid='public.project_users'::regclass "
            "AND confrelid='public.monitoring_projects'::regclass"
        )
        assert foreign_key is not None
        await owner.execute(f"CREATE SCHEMA {schema}")
        await owner.execute(
            f"CREATE TABLE {schema}.monitoring_projects(id integer PRIMARY KEY)"
        )
        try:
            await owner.execute(
                "ALTER TABLE public.project_users DROP CONSTRAINT "
                + foreign_key["name"]
            )
            await owner.execute(
                "ALTER TABLE public.project_users ADD CONSTRAINT "
                + foreign_key["name"]
                + " FOREIGN KEY(project_id) REFERENCES "
                + f"{schema}.monitoring_projects(id) ON DELETE CASCADE"
            )
            with pytest.raises(
                migrate.MigrationError, match="unsupported_legacy_foreign_key_drift"
            ):
                await migrate.migrate_schema(owner_dsn, mode="existing")
        finally:
            await owner.execute(
                "ALTER TABLE public.project_users DROP CONSTRAINT "
                + foreign_key["name"]
            )
            await owner.execute(
                "ALTER TABLE public.project_users ADD CONSTRAINT "
                + foreign_key["name"]
                + " "
                + foreign_key["ddl"]
            )
            await owner.execute(f"DROP TABLE {schema}.monitoring_projects")
            await owner.execute(f"DROP SCHEMA {schema}")
        original = migrate.migration_inputs()
        await owner.execute(
            "ALTER TABLE public.project_users DROP CONSTRAINT uq_project_user_code"
        )
        await owner.execute(
            "CREATE UNIQUE INDEX migration_partial_unique_probe ON "
            "public.project_users(project_id,user_code) WHERE false"
        )
        try:
            with pytest.raises(
                migrate.MigrationError, match="unsupported_legacy_unique_key_drift"
            ):
                await migrate.migrate_schema(owner_dsn, mode="existing")
        finally:
            await owner.execute("DROP INDEX public.migration_partial_unique_probe")
            await owner.execute(
                "ALTER TABLE public.project_users ADD CONSTRAINT "
                "uq_project_user_code UNIQUE(project_id,user_code)"
            )
        damaged = list(original)
        name, digest, sql = damaged[-1]
        damaged[-1] = (
            name,
            digest,
            sql
            + "\nCREATE TABLE public.migration_rollback_probe(id integer); SELECT 1/0;",
        )
        monkeypatch.setattr(migrate, "migration_inputs", lambda: damaged)
        with pytest.raises(
            migrate.MigrationError, match="migration_failed_transaction_not_confirmed"
        ):
            await migrate.migrate_schema(owner_dsn, mode="existing")
        assert (
            await owner.fetchval(
                "SELECT to_regclass('public.migration_rollback_probe')"
            )
            is None
        )
        assert (
            await owner.fetch(
                "SELECT * FROM public.agriculture_schema_migrations ORDER BY name"
            )
            == before
        )
        monkeypatch.setattr(migrate, "migration_inputs", lambda: original)
        assert (await migrate.migrate_schema(owner_dsn, mode="existing"))["passed"]
    finally:
        await owner.close()
