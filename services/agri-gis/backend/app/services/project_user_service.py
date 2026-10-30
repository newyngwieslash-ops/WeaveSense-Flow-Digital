"""项目用户、角色和业务能力服务。"""

from sqlalchemy.ext.asyncio import AsyncSession

from app.agriculture.actor_session import AgricultureSession
from app.agriculture.actors import TrustedActor, recheck_trusted_actor
from app.agriculture.identity.verify import require
from app.agriculture.operation_authorization import authorize_operation
from app.core.exceptions import PermissionDeniedException
from app.dao.project_user_dao import ProjectUserDAO
from app.models.workbench import ProjectUser
from app.schemas.project_user import (
    ProjectUserListResponse,
    ProjectUserResponse,
)

ROLE_CAPABILITIES: dict[str, tuple[str, ...]] = {
    "interpreter": (
        "edit_plots",
        "export_plot_attributes",
        "import_plot_attributes",
        "manage_imagery",
        "process_imagery",
        "import_disaster",
        "run_plot_quality_check",
        "rectify_supervision_finding",
        "review_change_candidate",
        "submit_self_check",
        "review_self_check",
        "view_services",
        "request_service_access",
        "view_thematic_maps",
        "view_monitoring_network",
        "view_uav",
        "download_uav_artifacts",
        "view_field_evidence",
        "view_growth_monitoring",
        "generate_growth_monitoring",
        "download_growth_monitoring",
    ),
    "field_inspector": (
        "upload_field_data",
        "view_services",
        "request_service_access",
        "view_monitoring_network",
        "ingest_monitoring_data",
        "report_device_fault",
        "view_uav",
        "operate_uav_missions",
        "download_uav_artifacts",
        "view_field_evidence",
        "view_growth_monitoring",
    ),
    "quality_inspector": (
        "export_plot_attributes",
        "import_plot_attributes",
        "run_quality_check",
        "run_plot_quality_check",
        "review_quality",
        "resolve_review_issue",
        "rematch_field_data",
        "resolve_field_issue",
        "review_disaster",
        "generate_disaster_report",
        "download_disaster_report",
        "review_change_candidate",
        "rectify_supervision_finding",
        "rollback_plot",
        "view_services",
        "request_service_access",
        "download_thematic_maps",
        "view_thematic_maps",
        "view_monitoring_network",
        "review_model_output",
        "review_county_pest_report",
        "answer_pest_consultation",
        "download_pest_report",
        "view_uav",
        "review_uav_findings",
        "download_uav_artifacts",
        "view_field_evidence",
        "view_growth_monitoring",
        "download_growth_monitoring",
    ),
    "project_manager": (
        "manage_project",
        "manage_production",
        "manage_datasets",
        "run_change_detection",
        "rectify_supervision_finding",
        "review_change_candidate",
        "download_supervision_report",
        "manage_services",
        "run_service_health_check",
        "view_services",
        "request_service_access",
        "manage_devices",
        "view_monitoring_network",
        "ingest_monitoring_data",
        "report_device_fault",
        "manage_monitoring_models",
        "review_model_output",
        "deliver_pest_alert",
        "manage_pest_reports",
        "review_prefecture_pest_report",
        "download_pest_report",
        "view_uav",
        "manage_uav_aircraft",
        "manage_uav_missions",
        "operate_uav_missions",
        "review_uav_findings",
        "download_uav_artifacts",
        "edit_plots",
        "export_plot_attributes",
        "import_plot_attributes",
        "manage_plot_attribute_fields",
        "manage_imagery",
        "process_imagery",
        "import_disaster",
        "run_quality_check",
        "run_plot_quality_check",
        "generate_delivery",
        "download_delivery",
        "generate_acceptance_report",
        "download_acceptance_report",
        "export_statistics",
        "import_statistics_history",
        "generate_statistics_report",
        "download_statistics_report",
        "generate_vector_export",
        "download_vector_export",
        "resolve_review_issue",
        "manage_rules",
        "rematch_field_data",
        "resolve_field_issue",
        "review_disaster",
        "generate_disaster_report",
        "download_disaster_report",
        "rollback_plot",
        "manage_thematic_maps",
        "generate_thematic_maps",
        "download_thematic_maps",
        "view_thematic_maps",
        "view_field_evidence",
        "view_growth_monitoring",
        "generate_growth_monitoring",
        "download_growth_monitoring",
    ),
    "client_reviewer": (
        "review_client",
        "download_supervision_report",
        "download_delivery",
        "download_acceptance_report",
        "download_statistics_report",
        "download_vector_export",
        "download_disaster_report",
        "resolve_review_issue",
        "approve_services",
        "view_services",
        "request_service_access",
        "download_thematic_maps",
        "view_thematic_maps",
        "view_monitoring_network",
        "review_province_pest_report",
        "answer_pest_consultation",
        "download_pest_report",
        "view_uav",
        "download_uav_artifacts",
        "view_field_evidence",
        "view_growth_monitoring",
        "download_growth_monitoring",
    ),
    "independent_supervisor": (
        "supervise_project",
        "download_supervision_report",
        "view_production",
        "view_datasets",
        "view_services",
        "request_service_access",
        "view_thematic_maps",
        "view_monitoring_network",
        "download_pest_report",
        "view_uav",
        "download_uav_artifacts",
        "view_field_evidence",
        "view_growth_monitoring",
        "download_growth_monitoring",
    ),
}


CAPABILITY_ACTIONS: dict[str, frozenset[str]] = {
    "edit_plots": frozenset(
        {"agri.parcel.create", "agri.parcel.update", "agri.parcel.delete"}
    ),
    "process_imagery": frozenset(
        {"agri.analysis.read", "agri.analysis.submit", "agri.asset_download.read"}
    ),
    "manage_datasets": frozenset({"agri.asset.create", "agri.asset_download.read"}),
    "view_growth_monitoring": frozenset({"agri.analysis.read"}),
    "generate_growth_monitoring": frozenset({"agri.analysis.submit"}),
    "run_change_detection": frozenset({"agri.analysis.submit"}),
    "view_field_evidence": frozenset({"agri.asset_download.read"}),
}
for _capability in (
    "download_statistics_report",
    "download_vector_export",
    "download_supervision_report",
    "download_delivery",
    "download_disaster_report",
    "download_pest_report",
    "download_uav_artifacts",
    "download_thematic_maps",
    "download_acceptance_report",
    "download_growth_monitoring",
    "export_plot_attributes",
    "export_statistics",
):
    CAPABILITY_ACTIONS[_capability] = frozenset({"agri.asset_download.read"})


async def require_bound_actor(db: AsyncSession, project_id: int) -> TrustedActor:
    require(
        isinstance(getattr(db, "sync_session", None), AgricultureSession),
        "trusted_actor_required",
        403,
    )
    actor = db.info.get("agriculture_resolved_actor")
    require(
        isinstance(actor, TrustedActor) and actor.project_id == project_id,
        "trusted_actor_required",
        403,
    )
    return actor


class ProjectUserService:
    """解析项目身份并执行服务端角色能力校验。"""

    def __init__(self, dao: ProjectUserDAO | None = None) -> None:
        """初始化项目用户服务。

        Args:
            dao: 项目用户 DAO。

        Returns:
            None: 无返回值。
        """
        self.dao = dao or ProjectUserDAO()

    @staticmethod
    def get_capabilities(role_code: str) -> list[str]:
        """获取角色对应的业务能力。

        Args:
            role_code: 角色编码。

        Returns:
            list[str]: 角色能力编码列表。
        """
        return list(ROLE_CAPABILITIES.get(role_code, ()))

    def to_response(self, user: ProjectUser) -> ProjectUserResponse:
        """将数据库用户转换为包含能力的响应。

        Args:
            user: 项目成员模型。

        Returns:
            ProjectUserResponse: 可供前端权限控制的用户信息。
        """
        return ProjectUserResponse(
            user_code=user.user_code,
            display_name=user.display_name,
            role_code=user.role_code,
            role_name=user.role_name,
            status=user.status,
            is_default=user.is_default,
            capabilities=self.get_capabilities(user.role_code),
        )

    async def list_project_users(
        self, db: AsyncSession, project_code: str
    ) -> ProjectUserListResponse:
        # No existence query before rejection, and no alternate identity directory.
        raise PermissionDeniedException("旧项目身份目录未开放，请使用平台成员接口")

    async def require_capability(
        self,
        db: AsyncSession,
        project_id: int,
        user_code: str,
        capability: str,
        *,
        expected_action: str | None = None,
        operation: str | None = None,
        targets: dict[str, str] | None = None,
    ) -> ProjectUser:
        actor = await require_bound_actor(db, project_id)
        require(user_code == actor.legacy_user_code, "actor_code_mismatch", 403)
        actions = CAPABILITY_ACTIONS.get(capability, frozenset())
        if expected_action is None and len(actions) == 1:
            expected_action = next(iter(actions))
        require(
            expected_action in actions
            and actor.signed_action == expected_action
            and db.info.get("agriculture_action") == actor.signed_action,
            "action_capability_mismatch",
            403,
        )
        user = await recheck_trusted_actor(db, actor)
        if capability not in ROLE_CAPABILITIES.get(user.role_code, ()):
            raise PermissionDeniedException(f"{user.role_name}无权执行当前业务节点")
        await authorize_operation(
            db, actor, capability, expected_action, operation, targets
        )
        return await recheck_trusted_actor(db, actor)
