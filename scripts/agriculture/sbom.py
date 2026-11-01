"""Regenerate an evidence inventory, not a legal opinion or final image SBOM.

Only reads installed metadata, lockfiles, build graphs and configured sources.
No dependency solving/download, application execution, or invented SPDX grants.
"""

import argparse
import hashlib
import importlib.metadata
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
MANDATORY = (
    "LICENSE",
    "NOTICE",
    "THIRD_PARTY_UPSTREAMS.md",
    "services/agri-gis/LICENSE",
    "services/agri-gis/NOTICE",
    "services/loom-engine/LICENSE",
)
DRAWING = re.compile(r"(?:^|[/@])tldraw(?:[/@]|$)", re.I)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def validate_boundary(root, react, vue):
    for name in MANDATORY:
        p = root / name
        if not p.is_file() or not p.read_bytes().strip():
            raise ValueError("Missing mandatory attribution: " + name)
    if react.get("product_profile") != "agriculture" or not react.get("modules"):
        raise ValueError("Missing actual React agriculture graph")
    if vue.get("profile") not in {"vue-session-boundary", "vue-agriculture-farms"} or not vue.get("modules"):
        raise ValueError("Missing actual Vue agriculture graph")
    if vue["profile"] == "vue-agriculture-farms" and (
        vue.get("business_api") != "same-origin-platform-canonical-v1"
        or vue.get("map") != "openlayers-vector-only-no-external-basemap"
    ):
        raise ValueError("Unreviewed Vue farm API or map boundary")
    values = [value.replace("\\", "/") for value in (
        react["modules"] + vue["modules"] + [p["name"] for p in react["packages"]]
    )]
    if any(DRAWING.search(value) for value in values):
        raise ValueError("Unapproved drawing dependency is reachable")
    if any(re.search(r"(?:^|/)src/", value.replace("\\", "/")) for value in vue["modules"]):
        raise ValueError("Unreviewed Vue legacy business module is reachable")
    if any(re.search(r"(?:^|[/@])cesium(?:[/@]|$)", value, re.I) for value in values):
        raise ValueError("Unreviewed 3D dependency is reachable")
    for package in react["packages"]:
        if not package.get("license") or package["license"] in {
            "UNKNOWN",
            "UNLICENSED",
            "REVIEW_REQUIRED",
        }:
            raise ValueError("Reachable React dependency lacks declared license")


class Inventory:
    def __init__(self, root, output):
        self.root, self.output = root, output
        self.notices = output / "notices"
        self.notices.mkdir(parents=True, exist_ok=True)

    def relative(self, path):
        try:
            return str(path.relative_to(self.root))
        except ValueError:
            return path.name

    def preserve(self, path):
        digest = sha(path)
        target = self.notices / (digest + ".txt")
        raw = path.read_bytes()
        if target.exists() and target.read_bytes() != raw:
            raise ValueError("Notice digest collision")
        target.write_bytes(raw)
        return {
            "source": self.relative(path),
            "sha256": digest,
            "preserved": "notices/" + target.name,
        }

    def texts(self, directory):
        if not directory or not directory.is_dir():
            return []
        candidates = []
        for p in directory.iterdir():
            if p.is_file() and re.match(
                r"^(?:licen[sc]e|copying|notice|authors)(?:[._-]|$)", p.name, re.I
            ):
                candidates.append(p)
            elif p.is_dir() and p.name.lower() in {"license", "licenses", "licences"}:
                candidates.extend(x for x in p.rglob("*") if x.is_file())
            elif p.is_file() and p.name.lower().startswith("readme"):
                body = p.read_text(errors="replace")
                if (
                    "Permission is hereby granted" in body
                    and "THE SOFTWARE IS PROVIDED" in body
                ):
                    candidates.append(p)
        return [self.preserve(p) for p in sorted(candidates)]

    def node(self, directory, reachable=()):
        records = []
        modules = directory / "node_modules"
        if not modules.is_dir():
            return records
        for path in sorted(modules.rglob("package.json")):
            if not re.search(
                r"(?:^|/)node_modules/(?:@[^/]+/)?[^/]+$", str(path.parent)
            ):
                continue
            try:
                meta = json.loads(path.read_text())
            except (ValueError, UnicodeError):
                continue
            name, version = meta.get("name"), meta.get("version")
            if not name or not version:
                continue
            declared = meta.get("license") or meta.get("licenses")
            records.append(
                {
                    "name": name,
                    "version": version,
                    "path": self.relative(path.parent),
                    "package_json_sha256": sha(path),
                    "license_declared": declared,
                    "license_status": "declared_not_legal_certification"
                    if declared
                    else "unresolved",
                    "notices": self.texts(path.parent),
                    "agriculture_reachable": self.relative(path.parent) in reachable,
                }
            )
        return records

    def node_lock(self, path):
        data = json.loads(path.read_text())
        return {
            "path": self.relative(path),
            "sha256": sha(path),
            "production_image_verified": False,
            "packages": [
                {
                    "path": name,
                    "version": p.get("version"),
                    "integrity": p.get("integrity"),
                    "license_declared": p.get("license"),
                    "dev": p.get("dev", False),
                    "optional": p.get("optional", False),
                    "installed_verified": False,
                }
                for name, p in sorted(data["packages"].items())
                if name
            ],
        }

    def go(self):
        env = dict(
            os.environ,
            GOMODCACHE=str(self.root / ".local/go-mod"),
            GOCACHE=str(self.root / ".local/go-cache"),
            GOPROXY="off",
            GOTOOLCHAIN="local",
            GOSUMDB="off",
        )
        command = [
            str(self.root / ".local/toolchains/go/bin/go"),
            "list",
            "-m",
            "-json",
            "all",
        ]
        result = subprocess.run(
            command,
            cwd=self.root / "backend",
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode:
            raise ValueError(
                "Offline locked Go inventory unavailable; populate reviewed cache first"
            )
        imported = subprocess.run(
            [
                command[0],
                "list",
                "-deps",
                "-f",
                "{{if .Module}}{{.Module.Path}}{{end}}",
                "./cmd/server",
            ],
            cwd=self.root / "backend",
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        if imported.returncode:
            raise ValueError("Actual server import graph unavailable")
        runtime_modules = set(imported.stdout.splitlines()) - {""}
        decoder, remaining, modules = json.JSONDecoder(), result.stdout, []
        while remaining.strip():
            item, end = decoder.raw_decode(remaining.lstrip())
            remaining = remaining.lstrip()[end:]
            if item.get("Main"):
                continue
            actual = item.get("Replace", item)
            directory = Path(actual["Dir"]) if actual.get("Dir") else None

            # Go may omit Dir for graph-only modules even when exact sources
            # were downloaded. Use Go's case-escaped immutable cache location,
            # and keep absence explicit; this does not infer package licensing.
            def escape(value):
                return "".join("!" + c.lower() if c.isupper() else c for c in value)

            if directory is None and actual.get("Version"):
                cached = (
                    self.root
                    / ".local/go-mod"
                    / (escape(actual["Path"]) + "@" + escape(actual["Version"]))
                )
                if cached.is_dir():
                    directory = cached
            modules.append(
                {
                    "path": item["Path"],
                    "version": item.get("Version"),
                    "replacement": item.get("Replace"),
                    "sum": actual.get("Sum"),
                    "indirect": item.get("Indirect", False),
                    "source_available": directory is not None and directory.is_dir(),
                    "server_import_graph_reachable": item["Path"] in runtime_modules,
                    "notices": self.texts(directory),
                    "license_expression": None,
                    "license_status": "actual_notice_text_requires_package_review",
                    "runtime_link_graph_verified": False,
                }
            )
        return {
            "command": "GOPROXY=off GOTOOLCHAIN=local go list -m -json all",
            "exit_code": result.returncode,
            "go_mod_sha256": sha(self.root / "backend/go.mod"),
            "server_import_command": "go list -deps -f {{if .Module}}{{.Module.Path}}{{end}} ./cmd/server",
            "server_import_graph_exit_code": imported.returncode,
            "server_import_graph_is_not_final_linker_or_container_sbom": True,
            "go_sum_sha256": sha(self.root / "backend/go.sum"),
            "modules": modules,
        }

    def python(self):
        records = []
        for dist in sorted(
            importlib.metadata.distributions(),
            key=lambda x: (x.metadata["Name"].lower(), x.version),
        ):
            notice_paths = []
            metadata_path = None
            for name in dist.files or []:
                p = Path(dist.locate_file(name))
                if str(name).endswith(".dist-info/METADATA"):
                    metadata_path = p
                if (
                    re.search(
                        r"(?:^|/)(?:licenses?/|licen[sc]e[^/]*$|copying[^/]*$|notice[^/]*$)",
                        str(name),
                        re.I,
                    )
                    and p.is_file()
                ):
                    notice_paths.append(p)
            classifiers = [
                c
                for c in dist.metadata.get_all("Classifier", [])
                if c.startswith("License ::")
            ]
            records.append(
                {
                    "name": dist.metadata["Name"],
                    "version": dist.version,
                    "license_expression": dist.metadata.get("License-Expression"),
                    "license_declared": dist.metadata.get("License"),
                    "license_classifiers": classifiers,
                    "metadata_sha256": sha(metadata_path) if metadata_path else None,
                    "notices": [self.preserve(p) for p in sorted(set(notice_paths))],
                    "installed_environment": "recovery_validation_main_dev_and_test_tools",
                    "production_image_verified": False,
                }
            )
        import rasterio

        with rasterio.Env() as environment:
            drivers = environment.drivers()
        return {
            "interpreter": sys.version.split()[0],
            "packages": records,
            "rasterio_version": rasterio.__version__,
            "gdal_version": rasterio.__gdal_version__,
            "gdal_drivers_registered": dict(sorted(drivers.items())),
            "driver_codec_linking_rights": "unresolved_release_gate_not_inferred_from_GDAL_license",
            "poetry_lock_sha256": sha(
                self.root / "services/agri-gis/backend/poetry.lock"
            ),
        }


def generate(root, output):
    inventory = Inventory(root, output)
    react_path = root / "web/dist-agriculture/agriculture-build-graph.json"
    vue_path = (
        root / "services/agri-gis/frontend/dist-agriculture/agriculture-profile.json"
    )
    react, vue = json.loads(react_path.read_text()), json.loads(vue_path.read_text())
    validate_boundary(root, react, vue)

    def reached_packages(prefix, modules):
        result = set()
        for module in modules:
            normalized = module.removeprefix("./")
            matches = list(
                re.finditer(r"(?:^|/)node_modules/(?:@[^/]+/)?[^/]+", normalized)
            )
            if matches:
                result.add(prefix + "/" + normalized[: matches[-1].end()])
        return result

    react_set = reached_packages("web", react["modules"])
    vue_set = reached_packages("services/agri-gis/frontend", vue["modules"])
    report = {
        "schema_version": "1.0.0",
        "classification": "initial_W00_inventory_regenerated_after_executor_reset",
        "final_commercial_sbom": False,
        "baseline": {
            "flow": "206c82af7ae007f45da4c582f373fbabf027f1b8",
            "agriscope": "571a2dcd9abf8b8a22b66c750ee710b61d482051",
        },
        "mandatory_attributions": [inventory.preserve(root / p) for p in MANDATORY],
        "additional_build_notice_evidence": [
            inventory.preserve(p)
            for p in (
                root / "web/dist-agriculture/THIRD_PARTY_NOTICES.txt",
                root
                / "docs/agriculture/profile/licenses/ant-design-icons-svg-4.5.0.LICENSE",
            )
            if p.is_file()
        ],
        "builds": {
            "react": {"manifest_sha256": sha(react_path), "manifest": react},
            "vue": {"manifest_sha256": sha(vue_path), "manifest": vue},
        },
        "built_artifacts": [
            {"path": str(p.relative_to(root)), "sha256": sha(p)}
            for directory in (
                root / "web/dist-agriculture",
                root / "services/agri-gis/frontend/dist-agriculture",
            )
            for p in sorted(directory.rglob("*"))
            if p.is_file()
        ],
        "node_installed": {
            "react": inventory.node(root / "web", react_set),
            "vue": inventory.node(root / "services/agri-gis/frontend", vue_set),
        },
        "node_declared_not_deployed": {
            "pi": inventory.node_lock(
                root / "backend/agent-runtime/pi/package-lock.json"
            ),
            "loom": inventory.node_lock(
                root / "services/loom-engine/package-lock.json"
            ),
        },
        "go": inventory.go(),
        "python": inventory.python(),
        "source_rights_config": {
            "path": "deploy/agriculture/sources.json",
            "sha256": sha(root / "deploy/agriculture/sources.json"),
            "configuration": json.loads(
                (root / "deploy/agriculture/sources.json").read_text()
            ),
        },
        "test_only_tools": [
            "Bun compiler",
            "Go toolchain",
            "native PostgreSQL Debian packages; not production distribution",
        ],
        "release_gates": [
            "Final container reachability and native linked-library inventory",
            "Per-module review of actual Go LICENSE/NOTICE text; no root-license inheritance",
            "Missing or absent package notices and unresolved declarations",
            "GDAL drivers/codecs and bundled library licensing",
            "Future Vue business profile reachability differs from current session-only build",
            "Fonts, data, model weights, hosted APIs and redistribution rights independent of code licenses",
        ],
    }
    (output / "initial-sbom.json").write_text(
        json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    )
    gaps = {
        "commercial_release_approved": False,
        "node_missing_declaration": [
            {
                "profile": k,
                "name": p["name"],
                "version": p["version"],
                "path": p["path"],
                "agriculture_reachable": p["agriculture_reachable"],
            }
            for k, rows in report["node_installed"].items()
            for p in rows
            if not p["license_declared"]
        ],
        "node_without_package_notice": [
            {
                "profile": k,
                "name": p["name"],
                "version": p["version"],
                "path": p["path"],
                "agriculture_reachable": p["agriculture_reachable"],
            }
            for k, rows in report["node_installed"].items()
            for p in rows
            if not p["notices"]
        ],
        "go_without_module_notice": [
            {
                "path": p["path"],
                "version": p["version"],
                "source_available": p["source_available"],
                "server_import_graph_reachable": p["server_import_graph_reachable"],
            }
            for p in report["go"]["modules"]
            if not p["notices"]
        ],
        "python_without_notice": [
            p["name"] for p in report["python"]["packages"] if not p["notices"]
        ],
        "pi_loom_package_notices_not_installed_or_inspected_in_this_recovery": True,
        "remaining_release_gates": report["release_gates"],
    }
    (output / "inventory-gaps.json").write_text(
        json.dumps(gaps, ensure_ascii=False, indent=2) + "\n"
    )
    return report


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    parser.add_argument(
        "--output", type=Path, default=ROOT / "docs/agriculture/dependencies"
    )
    args = parser.parse_args()
    result = generate(args.root.resolve(), args.output.resolve())
    print(
        json.dumps(
            {
                "status": "generated_initial_inventory",
                "commercial_release": False,
                "go_modules": len(result["go"]["modules"]),
                "python_packages": len(result["python"]["packages"]),
                "node_installed": {
                    k: len(v) for k, v in result["node_installed"].items()
                },
            }
        )
    )
