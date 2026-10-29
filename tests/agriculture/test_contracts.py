"""W00 offline machine-contract tests, not endpoint or signature acceptance."""

import json
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator, FormatChecker, ValidationError
from referencing import Registry, Resource

ROOT = Path(__file__).resolve().parents[2] / "contracts/agriculture"
SCHEMAS = {p.name: json.loads(p.read_text()) for p in ROOT.glob("*.schema.json")}
REGISTRY = Registry().with_resources(
    (s["$id"], Resource.from_contents(s)) for s in SCHEMAS.values()
)


def example(name):
    return json.loads((ROOT / "examples" / (name + ".json")).read_text())


def validate(name, value):
    Draft202012Validator(SCHEMAS[name + ".schema.json"], registry=REGISTRY,
                         format_checker=FormatChecker()).validate(value)


def test_all_schemas_are_valid_and_closed():
    for name, schema in SCHEMAS.items():
        Draft202012Validator.check_schema(schema)
        if name != "common.schema.json":
            assert schema["additionalProperties"] is False


@pytest.mark.parametrize("name,schema", [
    ("delegation-read", "delegation"), ("delegation-write", "delegation"),
    ("event-envelope", "event-envelope"), ("manifest", "manifest"),
    ("job", "job"), ("result", "result"), ("analysis-submit", "analysis-submit"),
])
def test_examples_validate(name, schema):
    validate(schema, example(name))


@pytest.mark.parametrize("name", ["delegation", "event-envelope", "manifest", "job", "result"])
def test_unsupported_schema_version_rejected(name):
    value = example("delegation-read" if name == "delegation" else name)
    value["schema_version"] = "2.0"
    with pytest.raises(ValidationError):
        validate(name, value)


@pytest.mark.parametrize("name", ["delegation", "event-envelope", "manifest", "job"])
def test_internal_missing_tenant_rejected(name):
    value = example("delegation-read" if name == "delegation" else name)
    del value["tenant_id"]
    with pytest.raises(ValidationError):
        validate(name, value)


@pytest.mark.parametrize("bad", ["", "*", "tenant=other", "agri.*", "agri.parcel.admin"])
def test_unbounded_or_invalid_scope_rejected(bad):
    value = example("delegation-read")
    value["scopes"] = [bad]
    with pytest.raises(ValidationError):
        validate("delegation", value)


@pytest.mark.parametrize("field", ["payload_hash", "operation_id", "canonicalization_version"])
def test_write_delegation_requires_request_binding(field):
    value = example("delegation-write")
    del value["request"][field]
    with pytest.raises(ValidationError):
        validate("delegation", value)


@pytest.mark.parametrize("field,bad", [
    ("resource_scope", []), ("scopes", []), ("audience", "flow-platform"),
    ("issuer", "browser"), ("issued_at", "not-a-date"),
    ("expires_at", "2026-01-01T00:00:00+08:00"), ("authorization_version", 0),
])
def test_delegation_scope_and_header_constraints(field, bad):
    value = example("delegation-read")
    value[field] = bad
    with pytest.raises(ValidationError):
        validate("delegation", value)


def test_delegation_cannot_bind_unrestricted_url_or_arbitrary_route():
    value = example("delegation-read")
    for path in ["https://example.invalid", "/internal/agri/v1/../admin", "/internal/agri/v1/jobs?token=secret"]:
        value["request"]["path"] = path
        with pytest.raises(ValidationError):
            validate("delegation", value)


@pytest.mark.parametrize("field", ["tenant_id", "user_id", "operator_code", "requester_code", "role", "price", "object_key", "signed_url"])
def test_client_cannot_supply_identity_price_or_storage_locator(field):
    value = example("analysis-submit")
    value[field] = "malicious"
    with pytest.raises(ValidationError):
        validate("analysis-submit", value)


@pytest.mark.parametrize("bad", ["hash_of_manifest", "A" * 64, "a" * 63, "https://bucket?token=secret"])
def test_payload_hash_strict_hex(bad):
    value = example("event-envelope")
    value["payload_hash"] = bad
    with pytest.raises(ValidationError):
        validate("event-envelope", value)


def test_event_producer_and_payload_cannot_be_relabelled():
    value = example("event-envelope")
    value["producer"] = "flow-platform"
    with pytest.raises(ValidationError):
        validate("event-envelope", value)
    value = example("event-envelope")
    value["payload"]["arbitrary_url"] = "https://example.invalid?token=secret"
    with pytest.raises(ValidationError):
        validate("event-envelope", value)


@pytest.mark.parametrize("field", ["object_key", "signed_url", "token", "bucket"])
def test_public_artifact_no_internal_storage_locator(field):
    value = example("manifest")
    value["artifacts"][0][field] = "secret"
    with pytest.raises(ValidationError):
        validate("manifest", value)


def test_manifest_cannot_embed_self_hash():
    value = example("manifest")
    value["manifest_sha256"] = "a" * 64
    with pytest.raises(ValidationError):
        validate("manifest", value)


@pytest.mark.parametrize("status,outcome", [("cancelled", "valid_result"), ("failed", None), ("running", "valid_result"), ("succeeded", "execution_failed")])
def test_job_state_and_outcome_are_separate_but_consistent(status, outcome):
    value = example("job")
    value.update(status=status, outcome=outcome)
    with pytest.raises(ValidationError):
        validate("job", value)


def test_running_job_requires_attempt():
    value = example("job")
    value["attempt"] = None
    with pytest.raises(ValidationError):
        validate("job", value)


def test_insufficient_data_cannot_have_normal_summary_or_artifacts():
    value = example("result")
    value["summary"] = {"mean_ndvi": 0.5, "valid_pixel_count": 5, "area_m2": "10000"}
    with pytest.raises(ValidationError):
        validate("result", value)
    value = example("result")
    value["artifacts"] = example("manifest")["artifacts"]
    with pytest.raises(ValidationError):
        validate("result", value)


@pytest.mark.parametrize("quantity", [-1, 0.5, "1", 9007199254740992])
def test_meter_is_bounded_nonnegative_integer(quantity):
    value = example("event-envelope")
    value["event_type"] = "agri.usage.measured"
    value["payload"] = {"operation_id": "op_fixture", "job_id": "job_fixture",
                        "meter": "pixels", "quantity": quantity,
                        "price_version": "price/1.0.0", "dedupe_key": "meter_fixture"}
    with pytest.raises(ValidationError):
        validate("event-envelope", value)
    value["payload"]["quantity"] = 0
    validate("event-envelope", value)


def test_minimum_openapi_is_explicitly_unimplemented_and_uses_real_platform_cookie():
    api = json.loads((ROOT / "openapi.json").read_text())
    assert api["openapi"] == "3.1.0"
    assert api["x-implementation-status"] == "contract_only"
    assert set(api["paths"]) == {"/analyses", "/jobs/{id}"}
    assert api["components"]["securitySchemes"]["platformSession"]["name"] == "open_ai_canvas_session"
    for path in api["paths"].values():
        for operation in path.values():
            assert operation["security"] == [{"platformSession": []}]
