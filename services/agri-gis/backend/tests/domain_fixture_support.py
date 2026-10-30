"""Explicit isolated database provisioning; never imported by production code."""

from sqlalchemy import text
from test_agriculture_actors_postgres import engine_for

from app.agriculture.domain_boundary import validate_model_inventory
from app.agriculture.migrate import migrate_schema
from app.models.plot import Base


async def install_domain_schema(owner_dsn):
    tables = validate_model_inventory()
    engine = engine_for(owner_dsn)
    try:
        async with engine.begin() as connection:
            role = (
                await connection.execute(text("SELECT current_user,current_database()"))
            ).one()
            if tuple(role) != ("flow_geo_migrate", "flow_geo"):
                raise ValueError("isolated geo migration role required")
            # Identity tables are owned by their SQL migrations, whose checks
            # must not be bypassed by ORM create_all(IF NOT EXISTS).
            await connection.run_sync(
                lambda c: Base.metadata.create_all(c, tables=list(tables.values()))
            )
    finally:
        await engine.dispose()
    # The earlier isolated component phase may have created only project/member
    # tables. Completing this explicit test baseline is not a production upgrade.
    # Every current boundary/ledger is installed by the actual owner command.
    await migrate_schema(owner_dsn, mode="existing")


async def bind_fixture_plot(owner, fixture):
    """Only explicit owner configuration can admit an existing fixture plot."""
    existing = await owner.fetchrow(
        "SELECT tenant_id,workspace_id,project_id,state FROM "
        "public.agriculture_legacy_plot_ownership WHERE plot_code=$1",
        fixture["plot_code"],
    )
    expected = (fixture["tenant"], fixture["workspace"], fixture["project"], "active")
    if existing is not None:
        if tuple(existing) != expected:
            raise ValueError("fixture ownership conflict; never auto-reassign")
        return
    await owner.execute(
        "INSERT INTO public.agriculture_legacy_plot_ownership "
        "(plot_code,tenant_id,workspace_id,projec"
        "t_id,state) VALUES($1,$2,$3,$4,'active')",
        fixture["plot_code"],
        fixture["tenant"],
        fixture["workspace"],
        fixture["project"],
    )
