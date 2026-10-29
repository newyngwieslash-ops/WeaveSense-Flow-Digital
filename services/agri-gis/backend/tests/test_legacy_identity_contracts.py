"""Fresh post-reset contracts: actual gate/SQLite and ASGI, not live GIS proof."""

import importlib.util
import re
from datetime import date
from pathlib import Path

import httpx
import pytest
from test_legacy_actor_gate import bind
from test_legacy_actor_gate import engine as gate_engine

from app.agriculture.actor_session import AgricultureSession
from app.agriculture.identity.verify import IdentityError
from app.schemas.production import WorkPackageCreateRequest
from app.schemas.review import ReviewActionRequest
from app.schemas.service_sharing import ServiceUsageRecordRequest
from app.schemas.workbench import TaskSubmitRequest
from app.services.project_user_service import ProjectUserService
from app.services.service_sharing_service import ServiceSharingService

ACTOR_FIELDS = (
    "operator_code",
    "reviewer_code",
    "uploader_code",
    "requester_code",
    "user_code",
    "investigator_code",
)
DOWNLOADS = (
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
)


@pytest.fixture
def engine():
    yield from gate_engine.__wrapped__()


@pytest.mark.asyncio
@pytest.mark.parametrize("field", ACTOR_FIELDS)
async def test_request_actor_spelling_never_selects_trusted_actor(engine, field):
    with AgricultureSession(engine) as session:
        db = await bind(session)
        supplied = {field: "forged_other_user"}
        with pytest.raises(IdentityError, match="actor_code_mismatch"):
            await ProjectUserService().require_capability(
                db,
                1,
                supplied[field],
                "edit_plots",
                expected_action="agri.parcel.update",
                operation="fixture.update",
                targets={"plot_code": "old_a"},
            )


@pytest.mark.asyncio
@pytest.mark.parametrize("capability", DOWNLOADS)
async def test_all_download_capabilities_reject_forged_actor(engine, capability):
    with AgricultureSession(engine) as session:
        db = await bind(session)
        with pytest.raises(IdentityError, match="actor_code_mismatch"):
            await ProjectUserService().require_capability(
                db,
                1,
                "forged_other_user",
                capability,
                expected_action="agri.asset_download.read",
            )
        with pytest.raises(IdentityError, match="action_capability_mismatch"):
            await ProjectUserService().require_capability(
                db,
                1,
                "legacy_same",
                capability,
                expected_action="agri.asset_download.read",
            )


@pytest.mark.asyncio
async def test_actual_review_and_assignment_schema_values_are_not_identity(engine):
    review = ReviewActionRequest(action="pass", reviewer_code="forged_reviewer")
    submit = TaskSubmitRequest(reviewer_code="forged_submitter")
    assignment = WorkPackageCreateRequest(
        region_codes=["fixture_region"],
        assignee_code="forged_assignee",
        operator_code="legacy_same",
        deadline=date(2030, 1, 1),
    )
    with AgricultureSession(engine) as session:
        db = await bind(session)
        for untrusted in (
            review.reviewer_code,
            submit.reviewer_code,
            assignment.assignee_code,
        ):
            with pytest.raises(IdentityError, match="actor_code_mismatch"):
                await ProjectUserService().require_capability(
                    db,
                    1,
                    untrusted,
                    "edit_plots",
                    expected_action="agri.parcel.update",
                    operation="fixture.update",
                    targets={"plot_code": "old_a"},
                )
        assert db.info["agriculture_resolved_actor"].legacy_user_code == "legacy_same"


@pytest.mark.asyncio
async def test_service_credential_is_not_human_actor_adapter(engine):
    request = ServiceUsageRecordRequest(
        credential_code="fixture_key",
        credential_secret="not_a_real_secret",
        request_method="GET",
        request_path="/fixture",
        response_status=200,
        duration_ms=1,
        response_bytes=0,
    )
    with AgricultureSession(engine) as session:
        db = await bind(session)
        with pytest.raises(IdentityError, match="service_consumer_adapter_unavailable"):
            await ServiceSharingService().record_usage(
                db, "fixture_project", "fixture_service", request
            )


@pytest.mark.asyncio
async def test_actual_old_app_rejects_all_declared_business_routes():
    from app.main import app

    root = Path(__file__).resolve().parents[4]
    script = root / "scripts/agriculture/legacy-identity-inventory.py"
    spec = importlib.util.spec_from_file_location("legacy_inventory_contract", script)
    inventory = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(inventory)
    routes = [
        r
        for r in inventory.scan(root)["routes"]
        if "/api/v1/endpoints/" in r["path"] and not r["path"].endswith("/health.py")
    ]
    assert (
        len(routes) >= 172
    ), "business surface unexpectedly disappeared from inventory"
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=app), base_url="http://test"
    ) as client:
        for route in routes:
            assert "<dynamic>" not in route["route"], route
            path = re.sub(r"\{[^}]+\}", "fixture", route["route"])
            if not path.startswith("/api/v1/"):
                path = "/api/v1" + path
            for method in route["methods"]:
                assert method in {
                    "GET",
                    "POST",
                    "PUT",
                    "PATCH",
                    "DELETE",
                    "HEAD",
                    "OPTIONS",
                }
                response = await client.request(
                    method,
                    path,
                    params={key: "forged_actor" for key in ACTOR_FIELDS},
                    json={key: "forged_actor" for key in ACTOR_FIELDS},
                    headers={
                        "Authorization": "Bearer fixture_invalid",
                        "X-API-Key": "fixture_invalid",
                    },
                )
                assert response.status_code == 404, (method, path, response.status_code)
