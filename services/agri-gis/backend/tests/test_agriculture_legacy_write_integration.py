"""Real PostGIS legacy service/commit, with explicit test-only resource adapters.

No public route or production fixture switch is installed by these tests.
Stable parcel mapping is a fixture, not W03 domain implementation/acceptance.
"""

from datetime import UTC, datetime, timedelta
from uuid import uuid4

import pytest
import pytest_asyncio
from geoalchemy2.elements import WKTElement
from sqlalchemy import inspect, select
from sqlalchemy.exc import DBAPIError
from sqlalchemy.ext.asyncio import AsyncSession
from test_agriculture_actors import ticket
from test_agriculture_actors_postgres import engine_for, pg_config, prepare

from app.agriculture.actor_session import AgricultureAsyncSession, AuditAdapter
from app.agriculture.actors import resolve_trusted_actor
from app.agriculture.identity.verify import IdentityError, require
from app.agriculture.operation_authorization import OperationAdapter, OperationFacts
from app.models.plot import Base, FarmlandPlot
from app.models.plot_attribute_field import ProjectPlotAttributeField
from app.models.workbench import (
    MonitoringTask,
    PlotEditOperation,
    PlotVersion,
    ReviewRecord,
    TaskPlot,
)
from app.schemas.workbench import PlotAttributeMutationRequest
from app.services.workbench_service import WorkbenchService


@pytest_asyncio.fixture
async def legacy_fixture():
    owner_dsn, runtime_dsn = pg_config()
    owner, tenant, workspace, actor, project, member = await prepare(owner_dsn)
    owner_engine, runtime = engine_for(owner_dsn), engine_for(runtime_dsn)
    suffix = uuid4().hex
    plot_code, task_code, parcel_id = (
        "plot_" + suffix,
        "task_" + suffix,
        "parcel_" + suffix,
    )
    needed = set()

    def include(table):
        if table in needed:
            return
        needed.add(table)
        for fk in table.foreign_keys:
            include(fk.column.table)

    for model in (
        FarmlandPlot,
        MonitoringTask,
        TaskPlot,
        PlotVersion,
        ReviewRecord,
        PlotEditOperation,
        ProjectPlotAttributeField,
    ):
        include(model.__table__)
    async with owner_engine.begin() as connection:
        for table in Base.metadata.sorted_tables:
            if table in needed:
                await connection.run_sync(
                    lambda c, table=table: table.create(c, checkfirst=True)
                )
    geom = WKTElement(
        "POLYGON((110 30,110.01 30,110.01 30.01,110 30.01,110 30))", srid=4326
    )
    async with AsyncSession(owner_engine, expire_on_commit=False) as db:
        plot = FarmlandPlot(
            plot_code=plot_code,
            geom=geom,
            land_class="耕地",
            crop_type="水稻",
            version=1,
        )
        task = MonitoringTask(
            project_id=project,
            task_code=task_code,
            task_name="fixture",
            administrative_region="fixture",
            status="interpreting",
        )
        db.add_all([plot, task])
        await db.flush()
        db.add(
            TaskPlot(
                task_id=task.id,
                plot_code=plot_code,
                assigned_by="historical",
                assigned_by_code="old_code",
                assigned_by_role="interpreter",
            )
        )
        old_version = PlotVersion(
            plot_code=plot_code,
            version=1,
            geom=geom,
            land_class="耕地",
            crop_type="水稻",
            interpretation_status="interpreting",
            created_by="historical",
            created_by_code="old_code",
            created_by_role="interpreter",
        )
        old_review = ReviewRecord(
            task_id=task.id,
            review_level="interpretation",
            action="historical",
            reviewer="historical",
            reviewer_code="old_code",
            reviewer_role="interpreter",
            comment="untouched history",
        )
        operation = PlotEditOperation(
            task_id=task.id,
            operation_code="operation_" + suffix,
            operation_type="split",
            source_plot_codes=[plot_code],
            result_plot_codes=[plot_code],
            status="reverted",
            operator="historical",
            operator_code="old_code",
            operator_role="interpreter",
            comment="fixture",
        )
        db.add_all([old_version, old_review, operation])
        await db.commit()
        ids = task.id, plot.id, old_version.id, old_review.id

    # Mapping facts are fixed by fixture setup, never derived from signed claims.
    resources = (("parcel", parcel_id),)
    parcel_mapping = {plot_code: parcel_id}

    def resources_for(obj, trusted):
        if isinstance(obj, PlotEditOperation):
            codes = set(obj.source_plot_codes) | set(obj.result_plot_codes)
            require(
                bool(codes) and codes <= parcel_mapping.keys(),
                "resource_scope_mismatch",
                403,
            )
            return tuple(sorted(("parcel", parcel_mapping[code]) for code in codes))
        return resources

    def project_for(session, obj, trusted):
        if isinstance(obj, FarmlandPlot | PlotVersion):
            column, value = "plot_code", obj.plot_code
            require(value == plot_code, "resource_scope_mismatch", 403)
            actual = session.execute(
                select(MonitoringTask.project_id)
                .join(TaskPlot, TaskPlot.task_id == MonitoringTask.id)
                .where(TaskPlot.plot_code == value, MonitoringTask.id == ids[0])
            ).scalar_one()
        elif isinstance(obj, ReviewRecord | PlotEditOperation):
            column, value = "task_id", obj.task_id
            require(value == ids[0], "resource_scope_mismatch", 403)
            actual = session.execute(
                select(MonitoringTask.project_id).where(MonitoringTask.id == value)
            ).scalar_one()
        else:
            column, value, actual = "project_id", obj.project_id, obj.project_id
            require(obj.id == ids[0], "resource_scope_mismatch", 403)
        state = inspect(obj)
        if state.persistent:
            table = state.mapper.local_table
            old = (
                session.connection()
                .execute(select(table.c[column]).where(table.c.id == state.identity[0]))
                .scalar_one()
            )
            require(old == value, "resource_scope_mismatch", 403)
            if isinstance(obj, PlotEditOperation):
                original = (
                    session.connection()
                    .execute(
                        select(
                            table.c.source_plot_codes, table.c.result_plot_codes
                        ).where(table.c.id == state.identity[0])
                    )
                    .one()
                )
                require(
                    tuple(original) == (obj.source_plot_codes, obj.result_plot_codes),
                    "resource_scope_mismatch",
                    403,
                )
        return actual

    adapters = tuple(
        AuditAdapter(
            model,
            "agri.parcel.update",
            frozenset(operations),
            resources_for,
            project=project_for,
        )
        for model, operations in (
            (FarmlandPlot, {"update"}),
            (MonitoringTask, {"update"}),
            (PlotVersion, {"insert"}),
            (ReviewRecord, {"insert"}),
            (PlotEditOperation, {"update"}),
        )
    )

    async def resolve_operation(db, trusted, targets):
        require(
            set(targets) == {"task_code", "plot_code"}, "operation_scope_mismatch", 403
        )
        require(
            targets["plot_code"] == plot_code and targets["task_code"] == task_code,
            "operation_scope_mismatch",
            403,
        )
        actual = (
            await db.execute(
                select(MonitoringTask.project_id)
                .join(TaskPlot, TaskPlot.task_id == MonitoringTask.id)
                .where(
                    MonitoringTask.task_code == targets["task_code"],
                    TaskPlot.plot_code == targets["plot_code"],
                )
            )
        ).scalar_one()
        return OperationFacts(tenant, workspace, actual, resources)

    registry = (
        OperationAdapter(
            "workbench.update_plot_attributes",
            "edit_plots",
            "agri.parcel.update",
            resolve_operation,
        ),
    )

    def proof():
        value = ticket(actor, tenant, workspace)
        value.claims["scopes"] = ["agri.parcel.update"]
        value.claims["resource_scope"] = [{"kind": "parcel", "id": parcel_id}]
        value.claims["request_id"] = "request_" + uuid4().hex
        return value

    async def snapshot():
        async with owner.transaction():
            await owner.execute("SELECT set_config('app.tenant_ids',$1,true)", tenant)
            return (
                tuple(
                    await owner.fetchrow(
                        "SELECT crop_type,version FROM farmland_plots WHERE id=$1",
                        ids[1],
                    )
                ),
                await owner.fetchval(
                    "SELECT count(*) FROM plot_versions WHERE plot_code=$1", plot_code
                ),
                await owner.fetchval(
                    "SELECT count(*) FROM review_records WHERE task_id=$1", ids[0]
                ),
                await owner.fetchval(
                    "SELECT count(*) FROM agriculture_actor_audit WHERE tenant_id=$1",
                    tenant,
                ),
                await owner.fetchval(
                    "SELECT to_jsonb(v) FROM plot_versions v WHERE id=$1", ids[2]
                ),
                await owner.fetchval(
                    "SELECT to_jsonb(r) FROM review_records r WHERE id=$1", ids[3]
                ),
                tuple(
                    await owner.fetchrow(
                        "SELECT status,total_plots,completed_plots "
                        "FROM monitoring_tasks WHERE id=$1",
                        ids[0],
                    )
                ),
                await owner.fetchval(
                    "SELECT jsonb_agg(to_jsonb(o) ORDER BY id) "
                    "FROM plot_edit_operations o WHERE task_id=$1",
                    ids[0],
                ),
            )

    async def mutate(value=None, operator="legacy_a"):
        async with AgricultureAsyncSession(
            runtime, audit_adapters=adapters, expire_on_commit=False
        ) as db:
            db.info["agriculture_operation_adapters"] = registry
            await resolve_trusted_actor(db, value or proof(), project)
            result = await WorkbenchService().update_plot_attributes(
                db,
                task_code,
                plot_code,
                PlotAttributeMutationRequest(
                    land_class="耕地", crop_type="玉米", operator_code=operator
                ),
            )
            assert "agriculture_resolved_actor" not in db.info  # actual internal commit
            return result

    try:
        yield locals()
    finally:
        await runtime.dispose()
        await owner_engine.dispose()
        await owner.close()


@pytest.mark.asyncio
async def test_postgres_real_workbench_write_commit_and_stable_audit(legacy_fixture):
    f = legacy_fixture
    before = await f["snapshot"]()
    await f["mutate"]()
    after = await f["snapshot"]()
    assert after[4:6] == before[4:6]  # Complete historical records unchanged.
    assert (
        after[0] == ("玉米", 2)
        and after[1] == before[1] + 1
        and after[2] == before[2] + 1
        and after[3] > before[3]
    )
    owner, tenant = f["owner"], f["tenant"]
    async with owner.transaction():
        await owner.execute("SELECT set_config('app.tenant_ids',$1,true)", tenant)
        rows = await owner.fetch(
            "SELECT actor_id,legacy_user_code,legacy_role "
            "FROM agriculture_actor_audit WHERE tenant_id=$1",
            tenant,
        )
        assert rows and all(
            tuple(r) == (f["actor"], "legacy_a", "interpreter") for r in rows
        )
        assert (
            await owner.fetchval(
                "SELECT created_by_code FROM plot_versions WHERE id=$1", f["ids"][2]
            )
            == "old_code"
        )
        assert (
            await owner.fetchval(
                "SELECT reviewer_code FROM review_records WHERE id=$1", f["ids"][3]
            )
            == "old_code"
        )
        assert (
            await owner.fetchval(
                "SELECT status FROM plot_edit_operations WHERE task_id=$1", f["ids"][0]
            )
            == "superseded"
        )
    # Forged administrator codes and mismatched signed scopes cannot authorize.
    with pytest.raises(IdentityError):
        await f["mutate"](operator="admin")
    wrong = f["proof"]()
    wrong.claims["resource_scope"] = [{"kind": "parcel", "id": "parcel_other"}]
    with pytest.raises(IdentityError):
        await f["mutate"](wrong)
    expired = f["proof"]()
    from dataclasses import replace

    with pytest.raises(IdentityError):
        await f["mutate"](
            replace(expired, expires_at=datetime.now(UTC) - timedelta(seconds=1))
        )
    # Task-wide undo invalidation must not mutate B's history under A's ticket.
    other_plot = "plot_" + uuid4().hex
    f["parcel_mapping"][other_plot] = "parcel_" + uuid4().hex
    async with AsyncSession(f["owner_engine"]) as db:
        db.add(
            FarmlandPlot(
                plot_code=other_plot,
                geom=f["geom"],
                land_class="耕地",
                crop_type="水稻",
                version=1,
            )
        )
        await db.flush()
        db.add(
            TaskPlot(task_id=f["ids"][0], plot_code=other_plot, assigned_by="fixture")
        )
        db.add(
            PlotEditOperation(
                task_id=f["ids"][0],
                operation_code="operation_" + uuid4().hex,
                operation_type="split",
                source_plot_codes=[other_plot],
                result_plot_codes=[other_plot],
                status="reverted",
                operator="historical",
                operator_code="old_code",
                operator_role="interpreter",
                comment="B history must remain unchanged",
            )
        )
        await db.commit()
    after = await f["snapshot"]()
    with pytest.raises(IdentityError, match="resource_scope_mismatch"):
        await f["mutate"]()
    assert await f["snapshot"]() == after
    await owner.execute(
        "UPDATE project_users SET status='disabled' WHERE id=$1", f["member"]
    )
    with pytest.raises(IdentityError):
        await f["mutate"]()
    assert await f["snapshot"]() == after


@pytest.mark.asyncio
async def test_postgres_real_workbench_audit_failure_rolls_back(legacy_fixture):
    f = legacy_fixture
    before = await f["snapshot"]()
    # Actual database rejection, not a mocked audit insert or service exception.
    name = "fixture_audit_reject_" + uuid4().hex
    owner = f["owner"]
    await owner.execute(
        f"CREATE FUNCTION public.{name}() RETURNS trigger LANGUAGE plpgsql AS $$ "
        f"BEGIN IF NEW.tenant_id = '{f['tenant']}' THEN "
        "RAISE EXCEPTION 'fixture audit unavailable' USING ERRCODE='23514'; "
        "END IF; RETURN NEW; END $$"
    )
    await owner.execute(
        f"CREATE TRIGGER {name} BEFORE INSERT ON agriculture_actor_audit "
        f"FOR EACH ROW EXECUTE FUNCTION public.{name}()"
    )
    try:
        with pytest.raises(DBAPIError):
            await f["mutate"]()
        assert await f["snapshot"]() == before
    finally:
        await owner.execute(f"DROP TRIGGER {name} ON agriculture_actor_audit")
        await owner.execute(f"DROP FUNCTION public.{name}()")
