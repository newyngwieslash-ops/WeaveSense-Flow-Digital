"""SQLite component tests only; PostgreSQL RLS is tested separately."""

from datetime import UTC, datetime, timedelta

import pytest
from app.agriculture.actor_models import ActorAudit, ActorBinding, ProjectBinding
from app.agriculture.actor_session import AgricultureSession, AuditAdapter
from app.agriculture.actors import recheck_trusted_actor, resolve_trusted_actor
from app.agriculture.identity.verify import IdentityError, Verified
from app.models.workbench import MonitoringProject, ProjectUser
from sqlalchemy import create_engine, select, text, update
from sqlalchemy.exc import IntegrityError
from sqlalchemy.orm import Session


class SQLiteBridge:
    """Drive shared async resolver with actual synchronous SQLite SQL, not mocks."""

    def __init__(self, session):
        self.session, self.info, self.bind = session, session.info, session.bind

    async def execute(self, *args, **kwargs):
        return self.session.execute(*args, **kwargs)


class FixtureSession(AgricultureSession):
    """Test-only adapter: never installed by a production factory."""

    def __init__(self, *args, **kwargs):
        super().__init__(
            *args,
            audit_adapters=(
                AuditAdapter(
                    ProjectUser,
                    "agri.farm.update",
                    frozenset({"update"}),
                    lambda obj, actor: (("farm", "farm_a"),),
                ),
            ),
            **kwargs,
        )


def ticket(actor_id="user_a", tenant="tenant_a", workspace="workspace_a"):
    return Verified(
        {
            "actor": {"kind": "user", "id": actor_id},
            "tenant_id": tenant,
            "workspace_id": workspace,
            "policy_version": "agriculture-policy/1.0.0",
            "authorization_version": 1,
            "request_id": "request_a",
            "request": {"operation_id": "operation_a"},
            "resource_scope": [{"kind": "farm", "id": "farm_a"}],
            "scopes": ["agri.farm.update"],
        },
        datetime.now(UTC) + timedelta(seconds=30),
        {},
    )


@pytest.fixture
def engine():
    engine = create_engine("sqlite://")
    for table in (
        MonitoringProject.__table__,
        ProjectUser.__table__,
        ProjectBinding.__table__,
        ActorBinding.__table__,
        ActorAudit.__table__,
    ):
        table.create(engine)
    with Session(engine) as db:
        for i in (1, 2):
            db.add(
                MonitoringProject(
                    id=i,
                    project_code=f"p{i}",
                    project_name="fixture",
                    province="fixture",
                    monitor_year=2026,
                    status="active",
                    progress=0,
                )
            )
            db.add(
                ProjectUser(
                    id=i,
                    project_id=i,
                    user_code="legacy_same",
                    display_name="same",
                    role_code="interpreter",
                    role_name="fixture",
                    status="active",
                    is_default=True,
                )
            )
        db.add(
            ProjectBinding(
                tenant_id="tenant_a",
                workspace_id="workspace_a",
                project_id=1,
                state="active",
            )
        )
        db.add(
            ActorBinding(
                tenant_id="tenant_a",
                workspace_id="workspace_a",
                actor_id="user_a",
                project_id=1,
                legacy_member_id=1,
                state="active",
            )
        )
        db.commit()
    yield engine
    engine.dispose()


@pytest.mark.asyncio
async def test_explicit_mapping_no_name_code_default_claim(engine):
    with FixtureSession(engine) as db:
        bridge = SQLiteBridge(db)
        actor = await resolve_trusted_actor(bridge, ticket(), 1)
        assert (
            actor.stable_actor_id == "user_a"
            and actor.legacy_user_code == "legacy_same"
        )
        for signed, project in [
            (ticket("legacy_same"), 1),
            (ticket(), 2),
            (ticket(tenant="tenant_b"), 1),
            (ticket(workspace="other"), 1),
        ]:
            with pytest.raises(IdentityError):
                await resolve_trusted_actor(bridge, signed, project)


@pytest.mark.asyncio
async def test_actor_128_char_not_truncated_and_revocation_rechecked(engine):
    long_id = "7" + "a" * 127
    with Session(engine) as owner:
        binding = owner.get(ActorBinding, ("tenant_a", "workspace_a", "user_a"))
        binding.actor_id = long_id
        owner.commit()
    with FixtureSession(engine) as db:
        actor = await resolve_trusted_actor(SQLiteBridge(db), ticket(long_id), 1)
        assert (
            actor.stable_actor_id == long_id and actor.legacy_user_code == "legacy_same"
        )
        with Session(engine) as owner:
            owner.get(ProjectUser, 1).status = "disabled"
            owner.commit()
        with pytest.raises(IdentityError):
            await recheck_trusted_actor(SQLiteBridge(db), actor)


@pytest.mark.asyncio
async def test_audit_commits_atomically_and_context_cleared(engine):
    with FixtureSession(engine) as db:
        await resolve_trusted_actor(SQLiteBridge(db), ticket(), 1)
        db.get(ProjectUser, 1).display_name = "new"
        db.commit()  # Real legacy internal commit shape, no post-hoc audit.
        assert "agriculture_resolved_actor" not in db.info
        db.get(ProjectUser, 1).display_name = "untrusted"
        with pytest.raises(IdentityError):
            db.commit()
        db.rollback()
    with Session(engine) as db:
        rows = db.scalars(select(ActorAudit)).all()
        assert len(rows) == 1
        assert rows[0].actor_id == "user_a" and rows[0].legacy_role == "interpreter"
        assert rows[0].changes == [{"operation": "update", "table": "project_users"}]
        assert db.get(ProjectUser, 1).display_name == "new"


@pytest.mark.asyncio
async def test_audit_failure_rolls_back_business_and_business_failure_rolls_back_audit(
    engine,
):
    with engine.begin() as conn:
        conn.execute(
            text(
                "CREATE TRIGGER fail_audit BEFORE INSERT ON agriculture_actor_audit "
                "BEGIN SELECT RAISE(ABORT, 'fixture'); END"
            )
        )
    with FixtureSession(engine) as db:
        await resolve_trusted_actor(SQLiteBridge(db), ticket(), 1)
        db.get(ProjectUser, 1).display_name = "not_committed"
        with pytest.raises(IntegrityError):
            db.commit()
        db.rollback()
    with engine.begin() as conn:
        conn.execute(text("DROP TRIGGER fail_audit"))
    with FixtureSession(engine) as db:
        await resolve_trusted_actor(SQLiteBridge(db), ticket(), 1)
        db.get(ProjectUser, 1).display_name = None
        with pytest.raises(IntegrityError):
            db.commit()
        db.rollback()
    with Session(engine) as db:
        assert db.get(ProjectUser, 1).display_name == "same"
        assert not db.scalars(select(ActorAudit)).all()


@pytest.mark.asyncio
async def test_raw_bulk_and_mapping_writes_closed(engine):
    with FixtureSession(engine) as db:
        await resolve_trusted_actor(SQLiteBridge(db), ticket(), 1)
        for stmt in (
            text("UPDATE project_users SET status='disabled'"),
            update(ProjectUser).values(status="disabled"),
        ):
            with pytest.raises(IdentityError):
                db.execute(stmt)
        db.get(ActorBinding, ("tenant_a", "workspace_a", "user_a")).state = "revoked"
        with pytest.raises(IdentityError):
            db.flush()
        db.rollback()


@pytest.mark.asyncio
async def test_unknown_model_and_reparent_and_resource_mismatch_denied(engine):
    with AgricultureSession(engine) as db:
        await resolve_trusted_actor(SQLiteBridge(db), ticket(), 1)
        db.get(ProjectUser, 1).display_name = "unknown_adapter"
        with pytest.raises(IdentityError, match="unadapted_database_operation"):
            db.flush()
        db.rollback()
    with FixtureSession(engine) as db:
        await resolve_trusted_actor(SQLiteBridge(db), ticket(), 1)
        db.get(ProjectUser, 2).project_id = 1
        with pytest.raises(IdentityError, match="project_scope_mismatch"):
            db.flush()
        db.rollback()
    with FixtureSession(engine) as db:
        signed = ticket()
        signed.claims["resource_scope"] = [{"kind": "farm", "id": "farm_b"}]
        await resolve_trusted_actor(SQLiteBridge(db), signed, 1)
        db.get(ProjectUser, 1).display_name = "wrong_resource"
        with pytest.raises(IdentityError, match="resource_scope_mismatch"):
            db.flush()
        db.rollback()


@pytest.mark.asyncio
async def test_read_action_cannot_mutate_and_context_cannot_be_swapped(engine):
    with FixtureSession(engine) as db:
        signed = ticket()
        signed.claims["scopes"] = ["agri.farm.read"]
        await resolve_trusted_actor(SQLiteBridge(db), signed, 1)
        with pytest.raises(IdentityError, match="actor_already_bound"):
            await resolve_trusted_actor(SQLiteBridge(db), ticket(), 1)
        db.get(ProjectUser, 1).display_name = "write_with_read"
        with pytest.raises(IdentityError, match="unadapted_database_operation"):
            db.flush()
        db.rollback()
        with pytest.raises(IdentityError, match="fresh_verification_required"):
            await resolve_trusted_actor(SQLiteBridge(db), signed, 1)
