"""Export original GIS Poetry lock using pinned official Poetry exporter.

Never solves or rewrites the upstream lock; markers and artifact hashes are
emitted by Poetry, not reconstructed from a flattened package list.
"""
import argparse
import hashlib
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]
POETRY = "2.2.1"
PLUGIN = "1.9.0"


def validate_export(body):
    lines = body.replace("\\\n", " ").splitlines()
    requirements = [line.strip() for line in lines if line.strip() and not line.startswith("#")]
    if not requirements:
        raise ValueError("Empty export refused")
    for line in requirements:
        if not re.match(r"^[A-Za-z0-9][A-Za-z0-9_.-]*(?:\[[A-Za-z0-9_,.-]+\])?==[^ ;]+", line):
            raise ValueError("Unpinned or unexpected export directive")
        if not re.search(r"--hash=sha256:[0-9a-f]{64}(?:\s|$)", line):
            raise ValueError("Requirement without SHA-256 hash")
    return len(requirements)


def export(project, group, *, runner=subprocess.run):
    if group not in {"main", "dev"}:
        raise ValueError("Unsupported group")
    original = {name: (project / name).read_bytes() for name in ["pyproject.toml", "poetry.lock"]}
    cache = ROOT / ".local/cache/uv"
    tools = ROOT / ".local/tools/uv"
    env = dict(os.environ, UV_CACHE_DIR=str(cache), UV_TOOL_DIR=str(tools),
               POETRY_CACHE_DIR=str(ROOT / ".local/cache/poetry"),
               POETRY_VIRTUALENVS_CREATE="false", POETRY_NO_INTERACTION="1")
    with tempfile.TemporaryDirectory(prefix="agri-poetry-export-") as tmp:
        directory = Path(tmp)
        for name, data in original.items():
            (directory / name).write_bytes(data)
        command = ["uv", "tool", "run", "--from", f"poetry=={POETRY}", "--with",
                   f"poetry-plugin-export=={PLUGIN}", "poetry", "export", "--format",
                   "requirements.txt", "--only", group, "--without-urls", "--no-interaction"]
        result = runner(command, cwd=directory, env=env, capture_output=True, text=True, check=True)
        for name, data in original.items():
            if (directory / name).read_bytes() != data or (project / name).read_bytes() != data:
                raise ValueError("Exporter changed source lock or project; refusing output")
        body = result.stdout
        validate_export(body)
    digest = hashlib.sha256(original["poetry.lock"]).hexdigest()
    return (f"# Original poetry.lock SHA-256: {digest}\n"
            f"# poetry=={POETRY}; poetry-plugin-export=={PLUGIN}; group={group}\n"
            "# Original markers/hashes retained. Index URLs omitted; use explicit trusted index.\n" + body)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project", type=Path, default=ROOT / "services/agri-gis/backend")
    parser.add_argument("--group", required=True, choices=["main", "dev"])
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if shutil.which("uv") is None:
        parser.error("uv is required; see docs/agriculture/PYTHON_ENVIRONMENT.md")
    if args.output.exists():
        parser.error("Output exists; choose a new path instead of overwriting")
    body = export(args.project.resolve(), args.group)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    with args.output.open("x") as output:
        output.write(body)


if __name__ == "__main__":
    main()
