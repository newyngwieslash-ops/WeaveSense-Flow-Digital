"""Explicit legacy-domain inventory and migration readiness, not route enablement."""

import importlib
import inspect
import json
import pkgutil
from pathlib import Path

from .identity.verify import IdentityError

INVENTORY = json.loads(
    Path(__file__).with_name("domain_table_inventory.json").read_text()
)["legacy_tables"]
LEGACY_TABLES = frozenset(row["table"] for row in INVENTORY)
READ_ONLY = frozenset(
    {
        "monitoring_projects",
        "project_users",
        "project_plot_attribute_fields",
        "task_plots",
    }
)
UPDATABLE = frozenset({"monitoring_tasks", "farmland_plots", "plot_edit_operations"})
INSERTABLE = frozenset({"plot_versions", "review_records"})
OPEN_TABLES = READ_ONLY | UPDATABLE | INSERTABLE
IDENTITY_TABLES = frozenset(
    {
        "agriculture_project_bindings",
        "agriculture_actor_bindings",
        "agriculture_actor_audit",
        "agriculture_identity_replay",
    }
)
OWNERSHIP_TABLE = "agriculture_legacy_plot_ownership"


def actual_model_inventory():
    import app.models

    tables = {}
    for module in pkgutil.iter_modules(app.models.__path__):
        loaded = importlib.import_module("app.models." + module.name)
        for _, model in inspect.getmembers(loaded, inspect.isclass):
            table = getattr(model, "__table__", None)
            if model.__module__.startswith("app.models.") and table is not None:
                tables[table.name] = table
    return tables


def validate_model_inventory():
    tables = actual_model_inventory()
    if set(tables) != LEGACY_TABLES or len(INVENTORY) != len(LEGACY_TABLES):
        raise ValueError(
            "GIS model inventory changed; explicit ownership review required"
        )
    for row in INVENTORY:
        actual = sorted(
            c.name + "->" + fk.target_fullname
            for c in tables[row["table"]].columns
            for fk in c.foreign_keys
        )
        if actual != row["foreign_keys"]:
            raise ValueError("GIS foreign-key ownership changed; review required")
    return tables


async def require_domain_boundary(connection, role="flow_geo_app"):
    """Ordinary web connection must see the explicit migrated privilege boundary."""
    try:
        if role not in {"flow_geo_app", "flow_geo_worker"}:
            raise ValueError("unknown domain runtime role")
        if await connection.fetchval("SELECT current_user") != role:
            raise ValueError("domain runtime role mismatch")
        validate_model_inventory()
        expected = sorted(LEGACY_TABLES | IDENTITY_TABLES | {OWNERSHIP_TABLE})
        rows = await connection.fetch(
            "SELECT c.relname,c.relrowsecurity,c.relforcerowsecurity,"
            "pg_get_userbyid(c.relowner) AS owner,"
            "has_table_privilege(current_user,c.oid,'SELECT') AS can_read,"
            "has_table_privilege(current_user,c.oid,'INSERT') AS can_insert,"
            "has_table_privilege(current_user,c.oid,'UPDATE') AS can_update,"
            "has_table_privilege(current_user,c.oid,'DELETE,TRUNCATE') AS can_delete "
            "FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n "
            "ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relname=ANY($1)",
            expected,
        )
        if {r["relname"] for r in rows} != set(expected):
            raise ValueError("missing domain migration")
        for row in rows:
            name = row["relname"]
            if not row["relrowsecurity"] or not row["relforcerowsecurity"]:
                raise ValueError("domain RLS required")
            if row["owner"] != "flow_geo_migrate":
                raise ValueError("unexpected domain owner")
            if role == "flow_geo_worker":
                if any(
                    row[key]
                    for key in ("can_read", "can_insert", "can_update", "can_delete")
                ):
                    raise ValueError("worker has no domain CRUD in this phase")
                continue
            if name in LEGACY_TABLES:
                if (
                    row["can_read"],
                    row["can_insert"],
                    row["can_update"],
                    row["can_delete"],
                ) != (
                    name in OPEN_TABLES,
                    name in INSERTABLE,
                    name in UPDATABLE,
                    False,
                ):
                    raise ValueError("domain privilege boundary mismatch")
            if name == OWNERSHIP_TABLE and (
                not row["can_read"]
                or row["can_insert"]
                or row["can_update"]
                or row["can_delete"]
            ):
                raise ValueError("ownership must be read only")
        count = await connection.fetchval(
            "SELECT count(*) FROM pg_catalog.pg_policy p "
            "JOIN pg_catalog.pg_class c ON c.oid=p.polrelid "
            "JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace "
            "WHERE n.nspname='public' AND c.relname=ANY($1) "
            "AND NOT p.polpermissive AND p.polname='agriculture_domain_scope'",
            sorted(OPEN_TABLES | IDENTITY_TABLES | {OWNERSHIP_TABLE}),
        )
        if count != len(OPEN_TABLES) + len(IDENTITY_TABLES) + 1:
            raise ValueError("restrictive domain policy required")
        unsafe_extra = await connection.fetchval(
            "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_class c "
            "JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace "
            "WHERE n.nspname='public' AND c.relkind IN ('r','p') "
            "AND NOT c.relname=ANY($1) AND NOT EXISTS("
            "SELECT 1 FROM pg_catalog.pg_depend d WHERE d.objid=c.oid "
            "AND d.classid='pg_catalog.pg_class'::regclass "
            "AND d.refclassid='pg_catalog.pg_extension'::regclass AND d.deptype='e') "
            "AND (NOT c.relrowsecurity OR NOT c.relforcerowsecurity OR "
            "has_table_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE')))",
            expected,
        )
        if unsafe_extra:
            raise ValueError("unreviewed application table is not denied")
    except Exception:
        raise IdentityError("gis_domain_boundary_unavailable", 503) from None
