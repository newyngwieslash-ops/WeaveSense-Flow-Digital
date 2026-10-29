"""Allowlist inherited process basics; fixture connection settings are caller-owned."""

from pathlib import Path
from typing import Mapping


PARENT_KEYS = frozenset({"PATH", "LANG", "LANGUAGE", "LC_ALL", "LC_CTYPE", "TZ"})


def native_test_environment(parent: Mapping[str, str], run: Path) -> dict[str, str]:
    env = {key: value for key, value in parent.items() if key in PARENT_KEYS}
    home = run / "home"
    home.mkdir(mode=0o700, exist_ok=False)
    env["HOME"] = str(home)
    env.setdefault("PATH", "/usr/bin:/bin")
    return env
