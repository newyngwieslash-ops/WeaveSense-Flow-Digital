"""Explicit synthetic deployment dictionaries; no official region dataset."""

import copy
import json

import pytest

from app.agriculture.farms_config import FarmDictionary
from app.agriculture.identity.verify import IdentityError


def dictionary_document():
    return {
        "schema_version": "agriculture-farm-dictionary/1",
        "dictionary_version": "fixture-1",
        "regions": [
            {
                "code": "north",
                "name": "North fixture",
                "timezones": ["Asia/Shanghai"],
                "area_units": ["m2", "ha", "mu"],
            },
            {
                "code": "south",
                "name": "South fixture",
                "timezones": ["Asia/Shanghai", "UTC"],
                "area_units": ["ha"],
            },
        ],
    }


def test_farm_dictionary_versioned_multi_region_immutable_snapshot(tmp_path):
    source = dictionary_document()
    path = tmp_path / "dictionary.json"
    path.write_text(json.dumps(source))
    dictionary = FarmDictionary.from_file(path)
    assert dictionary.dictionary_version == "fixture-1"
    assert len(dictionary.sha256) == 64
    assert (
        dictionary.validate_selection("fixture-1", "south", "UTC", "ha")["code"]
        == "south"
    )
    result = dictionary.snapshot()
    result["regions"][0]["name"] = "mutated"
    assert dictionary.snapshot() == source
    assert (
        FarmDictionary.from_document(copy.deepcopy(source)).sha256 == dictionary.sha256
    )


@pytest.mark.parametrize(
    "mutation",
    [
        lambda x: x.update(tenant_id="forged"),
        lambda x: x.update(schema_version="unknown"),
        lambda x: x.update(dictionary_version=""),
        lambda x: x.update(regions=[]),
        lambda x: x["regions"].append(copy.deepcopy(x["regions"][0])),
        lambda x: x["regions"][0].update(default_actor="fake"),
        lambda x: x["regions"][0].update(timezones=["Not/A_Timezone"]),
        lambda x: x["regions"][0].update(timezones=["UTC", "UTC"]),
        lambda x: x["regions"][0].update(area_units=["degrees_squared"]),
        lambda x: x["regions"][0].update(area_units=["ha", "ha"]),
        lambda x: x["regions"][0].update(code=[]),
        lambda x: x["regions"][0].update(name=" leading"),
    ],
)
def test_farm_dictionary_bad_deployment_fails_closed(mutation):
    source = dictionary_document()
    mutation(source)
    with pytest.raises(IdentityError) as error:
        FarmDictionary.from_document(source)
    assert (error.value.code, error.value.status) == (
        "farm_dictionary_unavailable",
        503,
    )


def test_farm_dictionary_missing_duplicate_json_and_symlink_fail_closed(tmp_path):
    path = tmp_path / "dictionary.json"
    for missing in (None, "", tmp_path / "absent"):
        with pytest.raises(IdentityError) as error:
            FarmDictionary.from_file(missing)
        assert error.value.status == 503
    path.write_text('{"schema_version":"a","schema_version":"b"}')
    with pytest.raises(IdentityError):
        FarmDictionary.from_file(path)
    path.write_text(json.dumps(dictionary_document()))
    link = tmp_path / "link.json"
    link.symlink_to(path)
    with pytest.raises(IdentityError):
        FarmDictionary.from_file(link)


@pytest.mark.parametrize(
    "version,region,timezone,unit,status",
    [
        ("old", "north", "Asia/Shanghai", "ha", 409),
        ("fixture-1", "unconfigured", "Asia/Shanghai", "ha", 422),
        ("fixture-1", "south", "Asia/Shanghai", "mu", 422),
        ("fixture-1", "north", "UTC", "ha", 422),
        ("fixture-1", [], "UTC", "ha", 422),
    ],
)
def test_farm_dictionary_selection_is_explicit(version, region, timezone, unit, status):
    dictionary = FarmDictionary.from_document(dictionary_document())
    with pytest.raises(IdentityError) as error:
        dictionary.validate_selection(version, region, timezone, unit)
    assert error.value.status == status
