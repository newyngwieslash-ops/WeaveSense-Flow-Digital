"""Recovered candidate: real SQLite binding tests, not production acceptance."""

import asyncio
from datetime import UTC, datetime, timedelta

import pytest
from sqlalchemy import Column, Integer, MetaData, String, Table, select
from sqlalchemy.orm import Session
from test_agriculture_actors import SQLiteBridge, ticket
from test_agriculture_actors import engine as actor_engine

from app.agriculture.actor_session import AgricultureSession
from app.agriculture.actors import resolve_trusted_actor
from app.agriculture.identity.verify import IdentityError, Verified
from app.agriculture.operation_authorization import OperationAdapter, OperationFacts
from app.core.exceptions import PermissionDeniedException
from app.services.project_user_service import ProjectUserService

mapping = Table(
    "fixture_resource_binding",
    MetaData(),
    Column("plot_code", String, primary_key=True),
    Column("tenant", String),
    Column("workspace", String),
    Column("project", Integer),
    Column("resource", String),
)


@pytest.fixture
def engine():
    base = actor_engine.__wrapped__()
    engine = next(base)
    mapping.create(engine)
    with engine.begin() as db:
        db.execute(
            mapping.insert(),
            [
                dict(
                    plot_code="old_a",
                    tenant="tenant_a",
                    workspace="workspace_a",
                    project=1,
                    resource="parcel_a",
                ),
                dict(
                    plot_code="old_b",
                    tenant="tenant_a",
                    workspace="workspace_a",
                    project=1,
                    resource="parcel_b",
                ),
            ],
        )
    yield engine
    next(base, None)


class CapabilityBridge(SQLiteBridge):
    @property
    def sync_session(self):
        return self.session


async def resolve_fixture(db, actor, targets):
    row = (
        await db.execute(
            select(mapping).where(mapping.c.plot_code == targets["plot_code"])
        )
    ).one()
    return OperationFacts(
        row.tenant, row.workspace, row.project, (("parcel", row.resource),)
    )


async def bind(session, action="agri.parcel.update", expires_at=None):
    bridge = CapabilityBridge(session)
    verified = ticket()
    if expires_at is not None:
        verified = Verified(verified.claims, expires_at, verified.body)
    verified.claims["scopes"] = [action]
    verified.claims["resource_scope"] = [{"kind": "parcel", "id": "parcel_a"}]
    await resolve_trusted_actor(bridge, verified, 1)
    bridge.info["agriculture_operation_adapters"] = (
        OperationAdapter(
            "fixture.update", "edit_plots", "agri.parcel.update", resolve_fixture
        ),
    )
    return bridge


@pytest.mark.asyncio
async def test_exact_actor_and_action_intersection(engine):
    with AgricultureSession(engine) as session:
        db = await bind(session)
        user = await ProjectUserService().require_capability(
            db,
            1,
            "legacy_same",
            "edit_plots",
            expected_action="agri.parcel.update",
            operation="fixture.update",
            targets={"plot_code": "old_a"},
        )
        assert user.id == 1


@pytest.mark.asyncio
async def test_other_actual_resource_denied_even_with_same_actor_project(engine):
    with AgricultureSession(engine) as session:
        db = await bind(session)
        with pytest.raises(IdentityError, match="operation_scope_mismatch"):
            await ProjectUserService().require_capability(
                db,
                1,
                "legacy_same",
                "edit_plots",
                expected_action="agri.parcel.update",
                operation="fixture.update",
                targets={"plot_code": "old_b"},
            )


@pytest.mark.asyncio
async def test_missing_adapter_is_not_authorized_by_ticket_alone(engine):
    with AgricultureSession(engine) as session:
        db = await bind(session)
        db.info.pop("agriculture_operation_adapters")
        with pytest.raises(IdentityError, match="operation_adapter_unavailable"):
            await ProjectUserService().require_capability(
                db,
                1,
                "legacy_same",
                "edit_plots",
                expected_action="agri.parcel.update",
                operation="fixture.update",
                targets={"plot_code": "old_a"},
            )


@pytest.mark.asyncio
async def test_signed_download_does_not_override_legacy_domain_capability(engine):
    with AgricultureSession(engine) as session:
        db = await bind(session, action="agri.asset_download.read")
        with pytest.raises(PermissionDeniedException, match="无权执行当前业务节点"):
            await ProjectUserService().require_capability(
                db, 1, "legacy_same", "download_delivery"
            )


@pytest.mark.asyncio
@pytest.mark.parametrize("code", ["admin", "", "legacy_same_other"])
async def test_supplied_identity_cannot_select_actor(engine, code):
    with AgricultureSession(engine) as session:
        db = await bind(session)
        with pytest.raises(IdentityError, match="actor_code_mismatch"):
            await ProjectUserService().require_capability(
                db, 1, code, "edit_plots", expected_action="agri.parcel.update"
            )


@pytest.mark.asyncio
@pytest.mark.parametrize(
    "capability,expected",
    [
        ("edit_plots", None),
        ("edit_plots", "agri.parcel.delete"),
        ("manage_rules", "agri.farm.update"),
        ("review_client", "agri.report.review"),
        ("download_delivery", "agri.asset_download.read"),
    ],
)
async def test_unknown_or_wrong_action_is_not_legacy_role_authority(
    engine, capability, expected
):
    with AgricultureSession(engine) as session:
        db = await bind(session)
        with pytest.raises(IdentityError, match="action_capability_mismatch"):
            await ProjectUserService().require_capability(
                db, 1, "legacy_same", capability, expected_action=expected
            )


@pytest.mark.asyncio
async def test_direct_unscoped_service_and_cross_project_denied(engine):
    with Session(engine) as session:
        db = await bind(session)
        with pytest.raises(IdentityError, match="trusted_actor_required"):
            await ProjectUserService().require_capability(
                db, 1, "legacy_same", "edit_plots", expected_action="agri.parcel.update"
            )
    with AgricultureSession(engine) as session:
        db = await bind(session)
        with pytest.raises(IdentityError, match="trusted_actor_required"):
            await ProjectUserService().require_capability(
                db, 2, "legacy_same", "edit_plots", expected_action="agri.parcel.update"
            )


@pytest.mark.asyncio
async def test_session_action_mutation_cannot_escalate(engine):
    with AgricultureSession(engine) as session:
        db = await bind(session)
        db.info["agriculture_action"] = "agri.parcel.delete"
        with pytest.raises(IdentityError, match="action_capability_mismatch"):
            await ProjectUserService().require_capability(
                db, 1, "legacy_same", "edit_plots", expected_action="agri.parcel.delete"
            )


@pytest.mark.asyncio
async def test_resolver_wait_cannot_extend_ticket_lifetime(engine):
    async def delayed_resolver(db, actor, targets):
        await asyncio.sleep(0.08)
        return await resolve_fixture(db, actor, targets)

    with AgricultureSession(engine) as session:
        db = await bind(session, expires_at=datetime.now(UTC) + timedelta(seconds=0.05))
        db.info["agriculture_operation_adapters"] = (
            OperationAdapter(
                "fixture.update", "edit_plots", "agri.parcel.update", delayed_resolver
            ),
        )
        with pytest.raises(IdentityError, match="delegation_expired"):
            await ProjectUserService().require_capability(
                db,
                1,
                "legacy_same",
                "edit_plots",
                expected_action="agri.parcel.update",
                operation="fixture.update",
                targets={"plot_code": "old_a"},
            )


@pytest.mark.asyncio
async def test_legacy_identity_directory_rejects_before_project_lookup():
    class ForbiddenLookup:
        def __getattr__(self, name):
            raise AssertionError("identity directory must not query project existence")

    service = ProjectUserService(dao=ForbiddenLookup())
    for project_code in ("existing_project", "missing_project"):
        with pytest.raises(PermissionDeniedException):
            await service.list_project_users(ForbiddenLookup(), project_code)
