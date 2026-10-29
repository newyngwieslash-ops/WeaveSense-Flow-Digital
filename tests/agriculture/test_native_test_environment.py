"""Parent connection settings and credentials must not reach native fixtures."""

import importlib.util
from pathlib import Path


spec = importlib.util.spec_from_file_location(
    "native_test_environment",
    Path(__file__).resolve().parents[2] / "scripts/agriculture/native_test_environment.py",
)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def test_only_process_basics_inherited_and_home_is_owned(tmp_path):
    sentinel = "synthetic-parent-secret-not-a-credential"
    parent = {key: sentinel for key in (
        "PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE", "PGPASSFILE", "PGOPTIONS", "PGPASSWORD",
        "DATABASE_URL", "CANVAS_DATABASE_DRIVER", "FLOW_INTERNAL_JWT_SIGNING_KEY",
        "AGRI_TEST_PG_RUNTIME_DSN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY",
        "LD_PRELOAD", "PYTHONPATH", "GOPROXY", "GOTOOLCHAIN", "HOME", "HTTP_PROXY")}
    parent.update(PATH="/usr/bin:/bin", LANG="C.UTF-8", TZ="UTC")
    env = module.native_test_environment(parent, tmp_path)
    assert env == {"PATH": "/usr/bin:/bin", "LANG": "C.UTF-8", "TZ": "UTC", "HOME": str(tmp_path / "home")}
    assert sentinel not in env.values()
    assert (tmp_path / "home").is_dir()
    assert parent["PGHOSTADDR"] == sentinel  # No mutation of caller environment.


def test_default_path_without_inherited_environment(tmp_path):
    assert module.native_test_environment({}, tmp_path) == {
        "PATH": "/usr/bin:/bin", "HOME": str(tmp_path / "home")}
