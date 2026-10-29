"""GIS-owned explicit identity bindings; never inferred from legacy names/codes."""

from datetime import datetime

from app.models.plot import Base
from sqlalchemy import (
    JSON,
    BigInteger,
    DateTime,
    ForeignKeyConstraint,
    Integer,
    String,
    UniqueConstraint,
    func,
)
from sqlalchemy.orm import Mapped, mapped_column


class ProjectBinding(Base):
    __tablename__ = "agriculture_project_bindings"
    __table_args__ = (
        UniqueConstraint("project_id"),
        UniqueConstraint("tenant_id", "workspace_id", "project_id"),
    )
    tenant_id: Mapped[str] = mapped_column(String(128), primary_key=True)
    workspace_id: Mapped[str] = mapped_column(String(128), primary_key=True)
    project_id: Mapped[int] = mapped_column(Integer, nullable=False)
    state: Mapped[str] = mapped_column(String(16), nullable=False, default="active")


class ActorBinding(Base):
    __tablename__ = "agriculture_actor_bindings"
    __table_args__ = (
        ForeignKeyConstraint(
            ["tenant_id", "workspace_id", "project_id"],
            [
                "agriculture_project_bindings.tenant_id",
                "agriculture_project_bindings.workspace_id",
                "agriculture_project_bindings.project_id",
            ],
        ),
        UniqueConstraint("project_id", "legacy_member_id"),
    )
    tenant_id: Mapped[str] = mapped_column(String(128), primary_key=True)
    workspace_id: Mapped[str] = mapped_column(String(128), primary_key=True)
    actor_id: Mapped[str] = mapped_column(String(128), primary_key=True)
    project_id: Mapped[int] = mapped_column(Integer, nullable=False)
    legacy_member_id: Mapped[int] = mapped_column(Integer, nullable=False)
    state: Mapped[str] = mapped_column(String(16), nullable=False, default="active")


class ActorAudit(Base):
    __tablename__ = "agriculture_actor_audit"
    id: Mapped[str] = mapped_column(String(36), primary_key=True)
    tenant_id: Mapped[str] = mapped_column(String(128), nullable=False)
    workspace_id: Mapped[str] = mapped_column(String(128), nullable=False)
    actor_id: Mapped[str] = mapped_column(String(128), nullable=False)
    project_id: Mapped[int] = mapped_column(Integer, nullable=False)
    legacy_member_id: Mapped[int] = mapped_column(Integer, nullable=False)
    legacy_user_code: Mapped[str] = mapped_column(String(50), nullable=False)
    legacy_role: Mapped[str] = mapped_column(String(40), nullable=False)
    policy_version: Mapped[str] = mapped_column(String(64), nullable=False)
    authorization_version: Mapped[int] = mapped_column(BigInteger, nullable=False)
    request_id: Mapped[str] = mapped_column(String(128), nullable=False)
    operation_id: Mapped[str | None] = mapped_column(String(128))
    action: Mapped[str] = mapped_column(String(80), nullable=False)
    resources: Mapped[list] = mapped_column(JSON, nullable=False)
    changes: Mapped[list] = mapped_column(JSON, nullable=False)
    created_at: Mapped[datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), nullable=False
    )
