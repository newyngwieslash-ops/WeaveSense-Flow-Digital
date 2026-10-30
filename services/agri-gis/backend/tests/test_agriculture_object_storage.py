"""Actual temporary private files, never public/customer data."""

import hashlib

import pytest

from app.agriculture.identity.verify import IdentityError
from app.agriculture.object_storage import PrivateLocalStorage, byte_range, object_key


def fixture(tmp_path):
    body = b"synthetic-private-object\x00\xff"
    kwargs = dict(
        tenant="tenant_a",
        workspace="workspace_a",
        object_id="obj_a",
        version=1,
        size=len(body),
        sha256=hashlib.sha256(body).hexdigest(),
    )
    kwargs["key"] = object_key("tenant_a", "workspace_a", "obj_a", 1)
    file = tmp_path / kwargs["key"]
    file.parent.mkdir(parents=True)
    file.write_bytes(body)
    return PrivateLocalStorage(tmp_path), kwargs, file, body


def test_private_bytes_and_version_binding(tmp_path):
    provider, kwargs, file, body = fixture(tmp_path)
    assert provider.read(**kwargs) == body
    for field, value in [
        ("version", 2),
        ("tenant", "tenant_b"),
        ("workspace", "workspace_b"),
        ("object_id", "obj_b"),
    ]:
        with pytest.raises(IdentityError):
            provider.read(**(kwargs | {field: value}))
    file.write_bytes(b"x" * len(body))
    with pytest.raises(IdentityError):
        provider.read(**kwargs)


@pytest.mark.parametrize(
    "key",
    [
        "../outside",
        "/etc/passwd",
        "tenants//a",
        "a%2fb",
        "a\\b",
        "https://example.test/a",
        "a/./b",
    ],
)
def test_key_not_a_user_path(tmp_path, key):
    provider, kwargs, _, _ = fixture(tmp_path)
    with pytest.raises(IdentityError):
        provider.read(**(kwargs | {"key": key}))


def test_file_and_parent_symlinks_refused(tmp_path):
    provider, kwargs, file, body = fixture(tmp_path)
    outside = tmp_path / "outside"
    outside.write_bytes(body)
    file.unlink()
    file.symlink_to(outside)
    with pytest.raises(IdentityError):
        provider.read(**kwargs)
    file.unlink()
    parent = file.parent
    parent.rmdir()
    parent.symlink_to(tmp_path, target_is_directory=True)
    with pytest.raises(IdentityError):
        provider.read(**kwargs)


@pytest.mark.parametrize(
    "value,expected",
    [
        (None, (0, 10)),
        ("bytes=0-3", (0, 4)),
        ("bytes=4-", (4, 10)),
        ("bytes=-3", (7, 10)),
        ("bytes=8-99", (8, 10)),
    ],
)
def test_single_range(value, expected):
    assert byte_range(value, 10) == expected


@pytest.mark.parametrize(
    "value",
    ["bytes=0-1,3-4", "bytes=-", "bytes=-0", "bytes=10-", "bytes=3-1", "items=0-3"],
)
def test_invalid_ranges(value):
    with pytest.raises(IdentityError) as error:
        byte_range(value, 10)
    assert error.value.status == 416


@pytest.mark.parametrize("value", ["../tenant", "a/b", "a%2fb", "a\\b", "", "a.b"])
def test_canonical_ids(value):
    with pytest.raises(ValueError):
        object_key(value, "workspace", "object", 1)
