"""DT025 offline contracts; actual authorization/PostGIS need separate evidence."""
import copy
import json
from pathlib import Path

import pytest
from jsonschema import Draft202012Validator, FormatChecker, ValidationError

ROOT = Path(__file__).resolve().parents[2]
SCHEMA = json.loads((ROOT / 'contracts/agriculture/farms.schema.json').read_text())


def validate(value, definition=None):
    schema = SCHEMA if definition is None else {
        '$schema': SCHEMA['$schema'], '$defs': SCHEMA['$defs'],
        '$ref': '#/$defs/' + definition,
    }
    Draft202012Validator(schema, format_checker=FormatChecker()).validate(value)


def creation():
    return {
        'workspace_id': 'ws_fixture', 'preview_id': 'fop_fixture',
        'farm': {
            'external_code': 'DEMO_A', 'name': 'Synthetic farm',
            'operator_name': 'Fixture cooperative', 'region_code': 'region_demo',
            'dictionary_version': 'fixture_1', 'timezone': 'Asia/Shanghai',
            'area_unit': 'mu', 'source_crs': 'EPSG:4326',
            'coordinate_order': 'longitude_latitude', 'data_class': 'demo',
            'geometry': {'type': 'Polygon', 'coordinates': [
                [[110, 20], [110.01, 20], [110.01, 20.01], [110, 20]]]},
        },
        'initial_grants': [{'subject_user_id': 'user_fixture',
                            'capabilities': ['agri.farm.read']}],
    }


def test_complete_demo_create_and_preview_contract():
    value = creation()
    validate(value)
    value['initial_grants'][0]['subject_user_id'] = '206c82af7ae007f45da4c582f373fbabf0'
    validate(value)
    value.pop('preview_id')
    value['action'] = 'create'
    validate(value)


@pytest.mark.parametrize('field', ['tenant_id', 'actor_id', 'user_id', 'role', 'roles', 'operator_code', 'allowed_actions', 'farm_id'])
@pytest.mark.parametrize('location', ['request', 'farm'])
def test_client_identity_and_server_fields_rejected(field, location):
    value = creation()
    (value if location == 'request' else value['farm'])[field] = 'forged'
    with pytest.raises(ValidationError):
        validate(value)


@pytest.mark.parametrize('field', ['geometry', 'source_crs', 'coordinate_order', 'data_class', 'dictionary_version'])
def test_spatial_and_data_class_provenance_required(field):
    value = creation()
    del value['farm'][field]
    with pytest.raises(ValidationError):
        validate(value)


@pytest.mark.parametrize('field,bad', [('source_crs', ''), ('source_crs', 'unknown'),
    ('coordinate_order', 'latitude_longitude'), ('geometry', {'type': 'Point', 'coordinates': [110, 20]}),
    ('contact', None), ('area_unit', 'degrees_squared'), ('data_class', 'production')])
def test_unsupported_and_null_values_rejected(field, bad):
    value = creation()
    value['farm'][field] = bad
    with pytest.raises(ValidationError):
        validate(value)


@pytest.mark.parametrize('grants', [[], [{'subject_user_id': 'user_fixture', 'capabilities': ['*']}],
    [{'subject_user_id': 'user_fixture', 'capabilities': ['agri.authorization.update']}],
    [{'subject_user_id': 'user_fixture', 'capabilities': ['agri.farm.read', 'agri.farm.read']}],
    [{'subject_user_id': 'user_fixture', 'capabilities': ['agri.farm.read'], 'role': 'farm_manager'}]])
def test_initial_grants_are_explicit_closed_bounded(grants):
    value = creation()
    value['initial_grants'] = grants
    with pytest.raises(ValidationError):
        validate(value)


def test_mutation_requires_preview_revision_and_preserves_identity():
    value = {'workspace_id': 'ws_fixture', 'preview_id': 'fop_fixture',
             'expected_revision': 1, 'farm': {'name': 'New name'}}
    validate(value)
    for field in ('preview_id', 'expected_revision'):
        bad = copy.deepcopy(value)
        del bad[field]
        with pytest.raises(ValidationError):
            validate(bad)
    for field in ('external_code', 'data_class', 'workspace_id'):
        bad = copy.deepcopy(value)
        bad['farm'][field] = 'forged'
        with pytest.raises(ValidationError):
            validate(bad)


def test_receipt_closed_hashes_and_revision():
    value = dict(schema_version='1.0', producer='agri-gis', aggregate_revision=1, operation_id='fop_fixture', tenant_id='tenant_fixture',
        workspace_id='ws_fixture', request_hash='0' * 64, grant_plan_hash='1' * 64,
        farm_id='farm_' + 'a' * 32, revision=1, event_id='evt_fixture',
        event_type='agri.farm.created', payload_hash='2' * 64, state='applied')
    validate(value, 'CommandReceipt')
    for field, invalid in [('revision', 0), ('request_hash', 'not-measured'), ('state', 'pending'),
                           ('tenant_id', ''), ('contact', {'email': 'private@fixture.invalid'})]:
        bad = dict(value, **{field: invalid})
        with pytest.raises(ValidationError):
            validate(bad, 'CommandReceipt')


WORKSPACE = json.loads((ROOT / 'contracts/agriculture/farm-workspace-operation.schema.json').read_text())
PATHS = {
    'farm.preview.create': '/internal/agri/v1/farms/preview-create',
    'farm.create': '/internal/agri/v1/farms/commands/create',
    'farm.receipt': '/internal/agri/v1/farms/commands/receipt',
    'farm.context': '/internal/agri/v1/farms/context',
    'farm.list': '/internal/agri/v1/farms/list',
    'farm.history.read': '/internal/agri/v1/farms/history',
}


def workspace_claims(action):
    return dict(schema_version='1.0', issuer='flow-platform', subject='flow-platform',
        audience='agri-gis', actor={'kind': 'user', 'id': '206c82af_fixture'},
        tenant_id='tenant_fixture', workspace_id='ws_fixture', action=action,
        authorization_version=1, policy_version='agriculture-policy/1.0.0',
        operation_id='fop_fixture', request_hash='0' * 64, grant_plan_hash='0' * 64,
        farm_ids=['farm_fixture'] if action in {'farm.list', 'farm.history.read'} else [],
        issued_at='2026-11-01T00:00:00Z', expires_at='2026-11-01T00:00:30Z',
        jti='jti_fixture', trace_id='trace_fixture', request_id='req_fixture',
        request={'method': 'POST', 'path': PATHS[action], 'canonicalization_version': 'rfc8785-safe-v1',
                 'payload_hash': '1' * 64, 'operation_id': 'fop_fixture'})


@pytest.mark.parametrize('action', PATHS)
def test_workspace_profiles_bind_exact_fixed_path_and_identity(action):
    validator = Draft202012Validator(WORKSPACE, format_checker=FormatChecker())
    value = workspace_claims(action)
    validator.validate(value)
    for path in set(PATHS.values()) - {PATHS[action]}:
        bad = copy.deepcopy(value)
        bad['request']['path'] = path
        with pytest.raises(ValidationError):
            validator.validate(bad)
    for field, content in [('issuer', 'agri-gis'), ('audience', 'flow-platform'),
                           ('role', 'owner'), ('action', 'farm.*'), ('authorization_version', 0)]:
        bad = dict(value, **{field: content})
        with pytest.raises(ValidationError):
            validator.validate(bad)


@pytest.mark.parametrize('action', PATHS)
def test_workspace_profile_farm_set_cannot_turn_into_wildcard(action):
    validator = Draft202012Validator(WORKSPACE)
    value = workspace_claims(action)
    value['farm_ids'] = [] if value['farm_ids'] else ['farm_fixture']
    with pytest.raises(ValidationError):
        validator.validate(value)
    value['farm_ids'] = ['*']
    with pytest.raises(ValidationError):
        validator.validate(value)


def test_farm_callback_has_exact_service_direction_and_path():
    service = json.loads((ROOT / 'contracts/agriculture/service-request.schema.json').read_text())
    validator = Draft202012Validator(service, format_checker=FormatChecker())
    value = workspace_claims('farm.context')
    value = {key: value[key] for key in service['required'] if key in value}
    value.update(issuer='agri-gis', subject='agri-gis', audience='flow-platform', capability='farm.operation.check')
    value['request'] = dict(method='POST', path='/internal/agri/v1/farms/authority/check',
                            canonicalization_version='rfc8785-safe-v1', payload_hash='1' * 64)
    validator.validate(value)
    for field, content in [('issuer', 'flow-platform'), ('audience', 'agri-gis')]:
        with pytest.raises(ValidationError):
            validator.validate(dict(value, **{field: content}))
    value['request']['path'] = '/internal/agri/v1/authority/check'
    with pytest.raises(ValidationError):
        validator.validate(value)
