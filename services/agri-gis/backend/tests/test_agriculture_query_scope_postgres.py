"""Real DAO queries through current resource RLS; all farms below are fixtures."""

from contextlib import asynccontextmanager
from decimal import Decimal
from uuid import uuid4

import asyncpg
import pytest
from domain_fixture_support import bind_fixture_plot, bind_fixture_reference
from sqlalchemy import func, select, text
from sqlalchemy.orm import aliased
from test_agriculture_actors_postgres import pg_config
from test_agriculture_domain_rls_postgres import fixtures, scope

from app.agriculture.database import GISRuntimeDatabase
from app.agriculture.identity.verify import IdentityError
from app.core.exceptions import NotFoundException
from app.dao.plot_dao import FarmlandPlotDAO
from app.dao.review_dao import ReviewDAO
from app.dao.statistics_dao import StatisticsDAO
from app.dao.vector_export_dao import VectorExportDAO
from app.models.plot import FarmlandPlot
from app.models.workbench import MonitoringTask, PlotVersion, ReviewRecord


@asynccontextmanager
async def query_fixture():
    async with fixtures() as (items, connection, owner_dsn):
        a = items[0]
        owner = a["owner"]
        other = {
            **a,
            "plot_code": "plot_extra_" + uuid4().hex,
            "parcel_id": "parcel_extra_" + uuid4().hex,
            "farm_id": "farm_extra_" + uuid4().hex,
        }
        await owner.execute(
            "INSERT INTO farmland_plots(plot_code,geom,area_ha,land_class,crop_type,"
            "custom_attributes,interpretation_status,version,district_code) "
            "SELECT $1,geom,9,'fixture_other','other','{}','interpreting',1,'fixture' "
            "FROM farmland_plots WHERE plot_code=$2",
            other["plot_code"],
            a["plot_code"],
        )
        await owner.execute(
            "UPDATE farmland_plots SET area_ha=1,district_code='fixture' "
            "WHERE plot_code=$1",
            a["plot_code"],
        )
        await bind_fixture_plot(owner, other)
        await bind_fixture_reference(owner, other)
        unreferenced = {**a, "plot_code": "plot_unmapped_" + uuid4().hex}
        await owner.execute(
            "INSERT INTO farmland_plots(plot_code,geom,custom_attributes,"
            "interpretation_status,version) SELECT $1,geom,'{}','interpreting',1 "
            "FROM farmland_plots WHERE plot_code=$2",
            unreferenced["plot_code"],
            a["plot_code"],
        )
        await bind_fixture_plot(owner, unreferenced)
        await owner.execute(
            "INSERT INTO monitoring_projects(project_code,project_name,province,"
            "monitor_year,status,progress) VALUES($1,'unmapped fixture','fixture',"
            "2026,'active',0)",
            "project_unmapped_" + uuid4().hex,
        )
        await owner.execute(
            "INSERT INTO task_plots(task_id,plot_code,assigned_by) "
            "VALUES($1,$2,'fixture')",
            a["ids"][0],
            other["plot_code"],
        )
        await owner.execute(
            "UPDATE monitoring_tasks SET total_plots=2,completed_plots=0 WHERE id=$1",
            a["ids"][0],
        )
        _, runtime_dsn = pg_config()
        factory = await GISRuntimeDatabase.open(runtime_dsn, pool_size=1)
        try:
            yield a, other, items[1:], factory, connection
        finally:
            await factory.close()


def read_proof(fixture):
    value = fixture["proof"]()
    value.claims["scopes"] = ["agri.parcel.read"]
    return value


@pytest.mark.asyncio
async def test_postgres_query_scope_same_bbox_and_history():
    async with query_fixture() as (a, other, foreign, factory, _):
        async with factory.session(read_proof(a)) as db:
            plots = FarmlandPlotDAO()
            assert [
                r["plot_code"] for r in await plots.get_by_bbox(db, 109, 29, 111, 31)
            ] == [a["plot_code"]]
            assert await plots.task_exists(db, a["task_code"])
            assert (await plots.get_by_point(db, 110.005, 30.005, a["task_code"]))[
                "plot_code"
            ] == a["plot_code"]
            assert (
                await plots.get_by_plot_code(db, other["plot_code"], a["task_code"])
                is None
            )
            assert (
                await plots.count_task_by_bbox(db, a["task_code"], 109, 29, 111, 31)
                == 1
            )
            assert await StatisticsDAO().get_totals(db, a["ids"][0]) == (
                1,
                Decimal("1"),
            )
            assert (
                len(await StatisticsDAO().get_land_class_groups(db, a["ids"][0])) == 1
            )
            export = VectorExportDAO()
            assert await export.count_features(db, a["ids"][0]) == 1
            assert [
                r.plot_code
                for r in await export.get_export_rows(db, a["ids"][0], [], [])
            ] == [a["plot_code"]]
            assert len(await ReviewDAO().get_versions(db, a["plot_code"])) == 1
            for hidden in [other, *foreign]:
                assert await ReviewDAO().get_version(db, hidden["plot_code"], 1) is None
                assert (
                    await plots.get_by_plot_code(
                        db, hidden["plot_code"], a["task_code"]
                    )
                    is None
                )
            for hidden in foreign:
                assert await StatisticsDAO().get_totals(db, hidden["ids"][0]) == (0, 0)
                assert not await plots.task_exists(db, hidden["task_code"])
            assert await StatisticsDAO().get_totals(db, -1) == (0, 0)
            from app.models.workbench import MonitoringProject

            assert (await db.execute(select(MonitoringProject.id))).scalars().all() == [
                a["project"]
            ]


@pytest.mark.asyncio
async def test_postgres_query_scope_mixed_task_and_core():
    async with query_fixture() as (a, other, foreign, factory, raw):
        async with factory.session(read_proof(a)) as db:
            assert (
                await db.execute(
                    select(MonitoringTask).where(MonitoringTask.id == a["ids"][0])
                )
            ).scalar_one_or_none() is None
            assert (
                await db.execute(
                    select(ReviewRecord).where(ReviewRecord.task_id == a["ids"][0])
                )
            ).all() == []
            alias = aliased(FarmlandPlot)
            assert (await db.execute(select(alias.plot_code))).scalars().all() == [
                a["plot_code"]
            ]
            assert (
                await db.execute(
                    select(func.count()).select_from(FarmlandPlot.__table__)
                )
            ).scalar_one() == 1
            assert (
                await db.execute(select(PlotVersion.__table__.c.plot_code))
            ).scalars().all() == [a["plot_code"]]
        # Raw ordinary-role SQL is also resource bounded, not just ORM rewriting.
        async with raw.transaction():
            await scope(raw, a["tenant"], a["workspace"], a)
            assert await raw.fetchval("SELECT count(*) FROM monitoring_tasks") == 0
            assert (
                await raw.fetchval(
                    "SELECT count(*) FROM agriculture_query_task_locators"
                )
                == 1
            )
            with pytest.raises(asyncpg.InsufficientPrivilegeError):
                async with raw.transaction():
                    await raw.fetch("SELECT progress FROM monitoring_projects")
            with pytest.raises(asyncpg.InsufficientPrivilegeError):
                async with raw.transaction():
                    await raw.execute("SET ROLE flow_geo_query_guard")
        async with raw.transaction():
            await scope(raw, a["tenant"], a["workspace"])
            assert await raw.fetchval("SELECT count(id) FROM monitoring_projects") == 0
        before = await a["snapshot"]()
        with pytest.raises(NotFoundException):
            await a["mutate"]()
        assert await a["snapshot"]() == before
        # Explicitly granted complete resource set may read the real full count.
        value = read_proof(a)
        value.claims["resource_scope"].append(
            {"kind": "parcel", "id": other["parcel_id"]}
        )
        value.claims["farm_ids"].append(other["farm_id"])
        async with factory.session(value) as db:
            assert (
                await db.execute(select(MonitoringTask.total_plots))
            ).scalar_one() == 2


@pytest.mark.asyncio
async def test_postgres_query_scope_missing_mapping_and_rebinding():
    async with query_fixture() as (a, other, foreign, factory, _):
        for mutation in ("missing", "foreign", "empty", "wrong_parent"):
            value = read_proof(a)
            if mutation == "missing":
                value.claims["resource_scope"].append(
                    {"kind": "parcel", "id": "parcel_unmapped"}
                )
            elif mutation == "foreign":
                value.claims["resource_scope"].append(
                    {"kind": "parcel", "id": foreign[0]["parcel_id"]}
                )
            elif mutation == "empty":
                value.claims["resource_scope"] = []
            else:
                value.claims["farm_ids"] = [other["farm_id"]]
            with pytest.raises(IdentityError):
                async with factory.session(value):
                    pytest.fail("invalid complete resource scope admitted")
        value = read_proof(a)
        async with factory.session(value) as db:
            value.claims["resource_scope"] = [
                {"kind": "parcel", "id": other["parcel_id"]}
            ]
            assert (
                await db.execute(select(FarmlandPlot.plot_code))
            ).scalars().all() == [a["plot_code"]]
            with pytest.raises(IdentityError):
                await db.execute(
                    select(func.set_config("app.query_farms", '["other"]', True))
                )
            for statement in (
                select(text("set_config('app.query_parcels','[]',true)")),
                select(func.count()).where(
                    text("set_config('app.tenant_ids','other',true)='other'")
                ),
            ):
                with pytest.raises(IdentityError):
                    await db.execute(statement)
            await db.rollback()
            assert (
                await db.execute(select(FarmlandPlot.plot_code))
            ).scalars().all() == [a["plot_code"]]
        await a["owner"].execute(
            "UPDATE agriculture_legacy_plot_ownership SET state='quarantined' "
            "WHERE plot_code=$1",
            a["plot_code"],
        )
        try:
            with pytest.raises(IdentityError) as error:
                async with factory.session(read_proof(a)):
                    pytest.fail("quarantined ownership admitted")
            assert error.value.code == "query_resource_scope_denied"
        finally:
            await a["owner"].execute(
                "UPDATE agriculture_legacy_plot_ownership SET state='active' "
                "WHERE plot_code=$1",
                a["plot_code"],
            )
