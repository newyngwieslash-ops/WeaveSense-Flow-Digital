"""Explicit owner-only GIS schema installation, never a runtime startup hook."""

import argparse
import asyncio
import hashlib
import json
import os
import re
from pathlib import Path
from uuid import uuid4

import asyncpg
from sqlalchemy import CheckConstraint, UniqueConstraint, inspect, text
from sqlalchemy.ext.asyncio import create_async_engine
from sqlalchemy.pool import NullPool

from .domain_boundary import validate_model_inventory

MIGRATIONS = (
    "20261029_agriculture_actor_bindings.sql",
    "20261029_agriculture_identity_replay.sql",
    "20261030_agriculture_domain_rls.sql",
)
LEDGER = "agriculture_schema_migrations"
ROLE_CHECK = """
SELECT current_database()='flow_geo' AND current_user='flow_geo_migrate'
 AND session_user=current_user AND rolcanlogin
 AND NOT (rolsuper OR rolbypassrls OR rolcreatedb OR rolcreaterole OR rolreplication)
 AND NOT EXISTS (
 SELECT 1 FROM pg_catalog.pg_auth_members m
 JOIN pg_catalog.pg_roles granted ON granted.oid=m.roleid
 WHERE m.member=pg_roles.oid
 AND (granted.rolname<>'flow_geo_actor_guard' OR m.inherit_option))
FROM pg_catalog.pg_roles WHERE rolname=current_user
"""
APPLICATION_TABLES = """
SELECT c.relname,pg_catalog.pg_get_userbyid(c.relowner) AS owner
FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relkind IN ('r','p')
AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
 JOIN pg_catalog.pg_extension e ON e.oid=d.refobjid
 WHERE d.classid='pg_catalog.pg_class'::regclass AND d.objid=c.oid
 AND d.refclassid='pg_catalog.pg_extension'::regclass AND d.deptype='e')
"""


class MigrationError(Exception):
    """Only this fixed code is suitable for console output."""

    def __init__(self, code, *, phase=None, sqlstate=None):
        super().__init__(code)
        self.phase = phase
        self.sqlstate = sqlstate


def migration_inputs():
    directory = Path(__file__).resolve().parents[2] / "scripts/migrations"
    result = []
    for name in MIGRATIONS:
        data = (directory / name).read_bytes()
        lines = data.decode("utf-8").splitlines()
        statements = [
            i
            for i, line in enumerate(lines)
            if line.strip() and not line.lstrip().startswith("--")
        ]
        if (
            not statements
            or lines[statements[0]].strip() != "BEGIN;"
            or lines[statements[-1]].strip() != "COMMIT;"
        ):
            raise MigrationError("migration_transaction_envelope_invalid")
        lines[statements[0]] = lines[statements[-1]] = ""
        result.append((name, hashlib.sha256(data).hexdigest(), "\n".join(lines)))
    return result


def validate_legacy_schema(connection, tables):
    """Validate the fixed baseline shape; never repair arbitrary legacy drift."""
    inspector = inspect(connection)
    dialect = connection.dialect
    for name, table in tables.items():
        # PostgreSQL normalizes CHECK syntax, never handwritten alias rewriting.
        probe = "agriculture_check_" + uuid4().hex
        quote = dialect.identifier_preparer.quote
        checks = [c for c in table.constraints if isinstance(c, CheckConstraint)]
        definitions = [
            quote(c.name)
            + " "
            + str(c.type.compile(dialect=dialect))
            + (
                " DEFAULT " + str(c.server_default.arg)
                if c.server_default is not None
                else ""
            )
            for c in table.columns
        ] + ["CHECK (" + str(c.sqltext) + ")" for c in checks]
        connection.exec_driver_sql(
            "CREATE TEMPORARY TABLE "
            + quote(probe)
            + " ("
            + ",".join(definitions)
            + ") ON COMMIT DROP"
        )
        columns = {c["name"]: c for c in inspector.get_columns(name, schema="public")}
        if set(columns) != set(table.columns.keys()):
            raise MigrationError("unsupported_legacy_column_drift")
        for expected in table.columns:
            actual = columns[expected.name]
            if (
                str(actual["type"].compile(dialect=dialect)).lower()
                != str(expected.type.compile(dialect=dialect)).lower()
                or actual["nullable"] != expected.nullable
            ):
                raise MigrationError("unsupported_legacy_column_drift")
            default = actual.get("default")
            if expected.server_default is not None:
                if default != str(expected.server_default.arg):
                    raise MigrationError("unsupported_legacy_default_drift")
            elif default is not None and not (
                expected.primary_key
                and expected.autoincrement is not False
                and default.startswith("nextval(")
            ):
                raise MigrationError("unsupported_legacy_default_drift")
        pk = inspector.get_pk_constraint(name, schema="public")["constrained_columns"]
        if tuple(pk) != tuple(c.name for c in table.primary_key):
            raise MigrationError("unsupported_legacy_primary_key_drift")
        actual_fk = {
            (
                tuple(f["constrained_columns"]),
                f.get("referred_schema") or "public",
                f["referred_table"],
                tuple(f["referred_columns"]),
                f.get("options", {}).get("ondelete"),
            )
            for f in inspector.get_foreign_keys(
                name, schema="public", postgresql_ignore_search_path=True
            )
        }
        expected_fk = {
            (
                tuple(c.name for c in f.columns),
                f.referred_table.schema or "public",
                f.referred_table.name,
                tuple(e.column.name for e in f.elements),
                f.ondelete,
            )
            for f in table.foreign_key_constraints
        }
        if actual_fk != expected_fk:
            raise MigrationError("unsupported_legacy_foreign_key_drift")
        if connection.execute(
            text(
                "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_constraint "
                "WHERE conrelid=to_regclass(:name) AND NOT convalidated)"
            ),
            {"name": "public." + name},
        ).scalar_one():
            raise MigrationError("unsupported_legacy_unvalidated_constraint")
        actual_unique = {
            tuple(c["column_names"])
            for c in inspector.get_unique_constraints(name, schema="public")
        } | {
            tuple(c["column_names"])
            for c in inspector.get_indexes(name, schema="public")
            if c["unique"]
            and all(isinstance(v, str) for v in c["column_names"])
            and not c.get("dialect_options", {}).get("postgresql_where")
        }
        expected_unique = {
            tuple(c.name for c in constraint.columns)
            for constraint in table.constraints
            if isinstance(constraint, UniqueConstraint)
        } | {
            tuple(c.name for c in index.columns)
            for index in table.indexes
            if index.unique
        }
        if not expected_unique.issubset(actual_unique):
            raise MigrationError("unsupported_legacy_unique_key_drift")
        if connection.execute(
            text(
                "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_index "
                "WHERE indrelid=to_regclass(:name) AND NOT indisvalid)"
            ),
            {"name": "public." + name},
        ).scalar_one():
            raise MigrationError("unsupported_legacy_invalid_index")
        reference = (
            connection.execute(
                text(
                    "SELECT pg_get_constraintdef(oid,true) "
                    "FROM pg_catalog.pg_constraint "
                    "WHERE conrelid=to_regclass(:name) AND contype='c'"
                ),
                {"name": "pg_temp." + probe},
            )
            .scalars()
            .all()
        )
        current = (
            connection.execute(
                text(
                    "SELECT pg_get_constraintdef(oid,true) "
                    "FROM pg_catalog.pg_constraint "
                    "WHERE conrelid=to_regclass(:name) AND contype='c'"
                ),
                {"name": "public." + name},
            )
            .scalars()
            .all()
        )
        connection.exec_driver_sql("DROP TABLE pg_temp." + quote(probe))
        if sorted(reference) != sorted(current):
            raise MigrationError("unsupported_legacy_check_constraint_drift")


async def migrate_schema(dsn, *, mode):
    if mode not in {"new", "existing"} or not isinstance(dsn, str) or not dsn:
        raise MigrationError("migration_configuration_invalid")
    tables = validate_model_inventory()
    inputs = migration_inputs()

    async def connect():
        # Preserve asyncpg's TLS/DSN handling; no implicit ssl=False or runtime DSN.
        return await asyncpg.connect(dsn, timeout=10)

    engine = create_async_engine(
        "postgresql+asyncpg://",
        async_creator=connect,
        hide_parameters=True,
        poolclass=NullPool,
    )
    phase = "connect"
    try:
        async with engine.begin() as connection:
            phase = "preflight"
            raw = (await connection.get_raw_connection()).driver_connection
            # Ensure SQLAlchemy has opened the underlying PostgreSQL transaction.
            await connection.exec_driver_sql("SELECT 1")
            if await raw.fetchval(ROLE_CHECK) is not True:
                raise MigrationError("migration_owner_required")
            await raw.execute(
                "SET LOCAL lock_timeout='10s'; SET LOCAL statement_timeout='120s'"
            )
            await raw.execute("SELECT pg_catalog.pg_advisory_xact_lock(741923870112)")
            rows = await raw.fetch(APPLICATION_TABLES)
            names = {r["relname"] for r in rows}
            if any(r["owner"] != "flow_geo_migrate" for r in rows):
                raise MigrationError("unexpected_application_table_owner")
            if mode == "new":
                if names:
                    raise MigrationError("new_requires_empty_application_schema")
                from app.models.plot import Base

                phase = "create_legacy_schema"
                await connection.run_sync(
                    lambda c: Base.metadata.create_all(
                        c, tables=list(tables.values()), checkfirst=False
                    )
                )
            elif not set(tables).issubset(names):
                raise MigrationError("existing_requires_complete_legacy_schema")
            phase = "validate_legacy_schema"
            await connection.run_sync(lambda c: validate_legacy_schema(c, tables))
            phase = "migration_ledger"
            await raw.execute(f"""
CREATE TABLE IF NOT EXISTS public.{LEDGER} (
 name text PRIMARY KEY, sha256 text NOT NULL CHECK(sha256 ~ '^[0-9a-f]{{64}}$'),
 applied_at timestamptz NOT NULL DEFAULT clock_timestamp());
REVOKE ALL ON public.{LEDGER} FROM PUBLIC,flow_geo_app,flow_geo_worker;
ALTER TABLE public.{LEDGER} ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.{LEDGER} FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS agriculture_migration_owner ON public.{LEDGER};
CREATE POLICY agriculture_migration_owner ON public.{LEDGER}
 TO flow_geo_migrate USING(true) WITH CHECK(true);
""")
            existing = dict(await raw.fetch(f"SELECT name,sha256 FROM public.{LEDGER}"))
            if set(existing) - set(MIGRATIONS):
                raise MigrationError("unknown_migration_version")
            for name, digest, sql in inputs:
                phase = name
                if name in existing and existing[name] != digest:
                    raise MigrationError("migration_checksum_mismatch")
                # Reapply idempotent boundary policies, not arbitrary data transforms.
                await raw.execute(sql)
                await raw.execute(
                    f"INSERT INTO public.{LEDGER}(name,sha256) VALUES($1,$2) "
                    "ON CONFLICT(name) DO NOTHING",
                    name,
                    digest,
                )
            result = {
                "passed": True,
                "mode": mode,
                "legacy_tables": len(tables),
                "migrations": [{"name": n, "sha256": d} for n, d, _ in inputs],
            }
            phase = "commit"
        return result
    except MigrationError:
        raise
    except Exception as exc:
        # Never expose driver errors containing DSNs, values or credential material.
        state = getattr(exc, "sqlstate", None)
        if state is None:
            state = getattr(getattr(exc, "orig", None), "sqlstate", None)
        if not isinstance(state, str) or not re.fullmatch("[0-9A-Z]{5}", state):
            state = None
        raise MigrationError(
            "migration_failed_transaction_not_confirmed", phase=phase, sqlstate=state
        ) from None
    finally:
        await engine.dispose()


def main():
    parser = argparse.ArgumentParser(description="农业 GIS 专用 owner 显式迁移")
    parser.add_argument("--mode", choices=("new", "existing"), required=True)
    args = parser.parse_args()
    try:
        result = asyncio.run(
            migrate_schema(
                os.environ.get("FLOW_GEO_MIGRATION_DATABASE_URL"),
                mode=args.mode,
            )
        )
    except MigrationError as exc:
        print(
            json.dumps(
                {
                    "passed": False,
                    "code": str(exc),
                    "phase": exc.phase,
                    "sqlstate": exc.sqlstate,
                }
            )
        )
        return 1
    except Exception:
        print(json.dumps({"passed": False, "code": "migration_configuration_invalid"}))
        return 1
    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
