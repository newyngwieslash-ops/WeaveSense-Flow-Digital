"""Private test composition is not a production entity publication API."""

import hashlib
import json
import os
import time
from concurrent.futures import ThreadPoolExecutor

import farm_http_fixture as fixture
import pytest

from app.agriculture.identity.verify import IdentityError
from app.agriculture.object_storage import object_key


def configuration(tmp_path):
    tmp_path.chmod(0o700)
    path = tmp_path / "farm-gis.json"
    value = {
        "platform": "http://127.0.0.1:18080",
        "platform_keys": [],
        "callback_key": {},
        "geo_migration_dsn": "postgres://fixture@127.0.0.1:18081/flow_geo?sslmode=disable",
        "geo_runtime_dsn": "postgres://fixture@127.0.0.1:18081/flow_geo?sslmode=disable",
        "dictionary": {},
    }
    path.write_text(json.dumps(value))
    path.chmod(0o600)
    return path, value


def storage(tmp_path):
    tmp_path.chmod(0o700)
    root = tmp_path / "asset-storage"
    root.mkdir(mode=0o700)
    key = object_key("tenant_fixture", "workspace_fixture", "object_fixture", 1)
    path = root / key
    path.parent.mkdir(parents=True)
    body = b"Synthetic actual local bytes; not production imagery."
    path.write_bytes(body)
    path.chmod(0o600)
    return (
        fixture.OwnedBarrierStorage(tmp_path),
        body,
        dict(
            tenant="tenant_fixture",
            workspace="workspace_fixture",
            object_id="object_fixture",
            version=1,
            key=key,
            size=len(body),
            sha256=hashlib.sha256(body).hexdigest(),
        ),
    )


def private(path, body):
    path.write_bytes(body)
    path.chmod(0o600)


def test_asset_fixture_opt_in_is_exact_and_default_unchanged(tmp_path):
    path, value = configuration(tmp_path)
    assert fixture.read_config(path) == value
    value["asset_object_fixture"] = True
    path.write_text(json.dumps(value))
    assert fixture.read_config(path)["asset_object_fixture"] is True
    for invalid in (False, 1, "true", {}, None):
        path.write_text(json.dumps({**value, "asset_object_fixture": invalid}))
        with pytest.raises(ValueError):
            fixture.read_config(path)
    path.write_text(json.dumps({**value, "seed_ready": True}))
    with pytest.raises(ValueError):
        fixture.read_config(path)


def test_asset_fixture_config_rejects_duplicate_keys_and_nonobjects(tmp_path):
    path, _ = configuration(tmp_path)
    for raw in (
        "null",
        "[]",
        '{"asset_object_fixture":true,"asset_object_fixture":true}',
    ):
        path.write_text(raw)
        with pytest.raises(ValueError):
            fixture.read_config(path)


@pytest.mark.parametrize(
    "mutation", ["public_file", "public_directory", "symlink", "hardlink", "oversized"]
)
def test_asset_fixture_configuration_files_fail_closed(tmp_path, mutation):
    path, _ = configuration(tmp_path)
    if mutation == "public_file":
        path.chmod(0o644)
    elif mutation == "public_directory":
        tmp_path.chmod(0o755)
    elif mutation == "symlink":
        link = tmp_path / "linked.json"
        link.symlink_to(path)
        path = link
    elif mutation == "hardlink":
        os.link(path, tmp_path / "linked.json")
    else:
        path.write_bytes(b" " * 65537)
    with pytest.raises((ValueError, OSError)):
        fixture.read_config(path)


def test_asset_fixture_storage_checks_real_hash_and_has_no_implicit_barrier(tmp_path):
    provider, body, args = storage(tmp_path)
    assert provider.read(**args) == body
    assert not (tmp_path / "asset-read-started").exists()
    with pytest.raises(IdentityError):
        provider.read(**{**args, "sha256": "0" * 64})


def test_asset_fixture_real_read_waits_for_private_release(tmp_path):
    provider, body, args = storage(tmp_path)
    private(tmp_path / "asset-read-arm", b"armed")
    with ThreadPoolExecutor(max_workers=1) as pool:
        future = pool.submit(provider.read, **args)
        deadline = time.monotonic() + 3
        while not (tmp_path / "asset-read-started").exists():
            assert not future.done()
            assert time.monotonic() < deadline
            time.sleep(0.01)
        assert not future.done()
        assert (
            fixture.private_fixture_path(tmp_path, "asset-read-started").read_bytes()
            == b"read"
        )
        private(tmp_path / "asset-read-release", b"release")
        assert future.result(timeout=3) == body


def test_asset_fixture_missing_release_times_out_without_returning_bytes(
    tmp_path, monkeypatch
):
    provider, _body, args = storage(tmp_path)
    private(tmp_path / "asset-read-arm", b"armed")
    clock = iter([0, 16])
    monkeypatch.setattr(fixture.time, "monotonic", lambda: next(clock))
    with pytest.raises(ValueError, match="read barrier deadline"):
        provider.read(**args)


def test_asset_fixture_links_or_public_storage_never_escape_owned_paths(tmp_path):
    provider, _body, args = storage(tmp_path)
    target = tmp_path / "arm-target"
    private(target, b"armed")
    (tmp_path / "asset-read-arm").symlink_to(target)
    with pytest.raises(ValueError):
        provider.read(**args)
    (tmp_path / "asset-storage").chmod(0o755)
    with pytest.raises(ValueError):
        fixture.OwnedBarrierStorage(tmp_path)
