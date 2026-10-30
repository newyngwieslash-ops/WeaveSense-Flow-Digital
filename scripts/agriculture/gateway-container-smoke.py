"""Build and run only a disposable local web image; never push or deploy.

This is static/error-routing container evidence, NOT authenticated end-to-end.
"""

import hashlib
import json
from pathlib import Path
import re
import subprocess
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]


def build_progress(stdout=None, stderr=None):
    """Bounded structural evidence only, never commands or BuildKit error text.

    Stage names match Dockerfile.web (its unnamed final stage is stage-2).
    Unknown lines are discarded. This describes observed progress, not success.
    """
    events = []
    truncated = False
    for raw in (stdout, stderr):
        if raw is None:
            continue
        if isinstance(raw, str):
            raw = raw.encode("utf-8")
        truncated |= len(raw) > 262144
        tail = raw[-262144:]
        if len(raw) > len(tail):
            # Do not interpret a partial first line as a real progress record.
            tail = tail.partition(b"\n")[2]
        for line in tail.splitlines():
            if len(line) > 4096:
                continue
            text = line.decode("ascii", errors="replace")
            step = re.match(
                r"^#([1-9][0-9]{0,3}) \[(bun|build|stage-2) "
                r"([1-9][0-9]{0,2})/([1-9][0-9]{0,2})\] "
                r"(FROM|RUN|COPY|WORKDIR|ENV|USER|EXPOSE|ENTRYPOINT|HEALTHCHECK)(?: |$)",
                text,
            )
            status = re.fullmatch(
                r"#([1-9][0-9]{0,3}) (DONE|CACHED|CANCELED)"
                r"(?: ([0-9]{1,5}(?:\.[0-9]{1,3})?)s)?", text
            )
            internal = re.match(
                r"^#([1-9][0-9]{0,3}) \[internal\] "
                r"(load build definition from|load metadata for|load build context|load \.dockerignore)(?: |$)",
                text,
            )
            if step:
                number, stage, index, total, operation = step.groups()
                if int(index) > int(total):
                    continue
                events.append({"step_id": int(number), "stage": stage,
                               "index": int(index), "total": int(total),
                               "operation": operation})
            elif internal:
                number, operation = internal.groups()
                events.append({"step_id": int(number), "stage": "internal",
                               "operation": {
                                   "load build definition from": "LOAD_DEFINITION",
                                   "load metadata for": "LOAD_METADATA",
                                   "load build context": "LOAD_CONTEXT",
                                   "load .dockerignore": "LOAD_IGNORE",
                               }[operation]})
            elif status:
                number, state, elapsed = status.groups()
                event = {"step_id": int(number), "status": state}
                if elapsed is not None:
                    event["elapsed_seconds"] = float(elapsed)
                events.append(event)
            if len(events) > 128:
                events.pop(0)
                truncated = True
    return {"events": events, "truncated": truncated}


def command(*args, timeout=60):
    return subprocess.run(
        args, cwd=ROOT, capture_output=True, timeout=timeout, check=True
    )


def fetch(origin, path):
    request = urllib.request.Request(origin + path)
    try:
        with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(
            request, timeout=5
        ) as response:
            return response.status, response.headers, response.read(2 << 20)
    except urllib.error.HTTPError as error:
        return error.code, error.headers, error.read(2 << 20)


def owned_container_port(raw):
    binding = json.loads(raw)["8080/tcp"]
    if len(binding) != 1 or binding[0]["HostIp"] != "127.0.0.1":
        raise ValueError("container must publish only loopback")
    port = int(binding[0]["HostPort"])
    if not 1024 <= port <= 65535:
        raise ValueError("invalid loopback port")
    return port


def hashed_script(body):
    paths = re.findall(
        rb'src="(/(?:agri/)?assets/[^"<>]+-[A-Za-z0-9_-]{8,}\.js)"', body
    )
    if not paths:
        raise ValueError("actual built hashed script absent")
    return paths[0].decode("ascii")


def main():
    identity = uuid.uuid4().hex
    tag, container = "weavesense-gateway-smoke:" + identity, None
    evidence = ROOT / ".local/evidence"
    evidence.mkdir(parents=True, exist_ok=True)
    report = {
        "kind": "disposable_local_docker_web_smoke",
        "authenticated_e2e": False,
        "production_deployment": False,
        "registry_push": False,
        "checks": [],
    }
    phase = "build"
    success = False
    try:
        result = command(
            "docker",
            "build",
            "--progress=plain",
            "--file",
            "deploy/agriculture/Dockerfile.web",
            "--tag",
            tag,
            ".",
            timeout=1800,
        )
        report["build_log_sha256"] = hashlib.sha256(
            result.stdout + result.stderr
        ).hexdigest()
        report["build_progress"] = build_progress(result.stdout, result.stderr)
        report["image_id"] = (
            command("docker", "image", "inspect", "--format", "{{.Id}}", tag)
            .stdout.decode()
            .strip()
        )
        phase = "run"
        # Retain the unique name before dispatch: a timeout may happen after
        # Docker created the container but before returning its ID to us.
        container = "weavesense-gateway-smoke-" + identity
        command(
            "docker",
            "run",
            "--name",
            container,
            "--detach",
            "--read-only",
            "--cap-drop=ALL",
            "--security-opt=no-new-privileges",
            "--tmpfs",
            "/tmp:rw,noexec,nosuid,size=32m",
            "--publish",
            "127.0.0.1::8080",
            "--env",
            "AGRI_PLATFORM_UPSTREAM=127.0.0.1:9",
            tag,
        )
        port = owned_container_port(
            command(
                "docker",
                "inspect",
                "--format",
                "{{json .NetworkSettings.Ports}}",
                container,
            ).stdout
        )
        origin = f"http://127.0.0.1:{port}"
        for attempt in range(60):
            try:
                if fetch(origin, "/")[0] == 200:
                    break
            except (OSError, urllib.error.URLError):
                pass
            time.sleep(0.5)
        else:
            raise RuntimeError("web container unavailable")
        phase = "http_matrix"
        for path in ("/", "/agri/", "/agri/workspaces", "/login"):
            status, headers, body = fetch(origin, path)
            assert status == 200 and "no-store" in headers.get("Cache-Control", "")
            report["checks"].append({"path": path, "status": status, "no_store": True})
            if path in ("/", "/agri/"):
                asset = hashed_script(body)
                status, headers, _ = fetch(origin, asset)
                assert status == 200 and "immutable" in headers.get("Cache-Control", "")
                report["checks"].append(
                    {"asset": "react" if path == "/" else "vue", "immutable": True}
                )
        for path in (
            "/assets/missing-12345678.js",
            "/agri/assets/missing-12345678.js",
            "/internal",
            "/internal/agri/v1/authority/check",
            "/api/internal",
            "/api/internal/test",
            "/api/v1/project-users",
            "/agri/api/x",
            "/.env",
        ):
            status, headers, _ = fetch(origin, path)
            assert status == 404 and "no-store" in headers.get("Cache-Control", "")
            report["checks"].append({"path": path, "status": status, "no_store": True})
        status, headers, _ = fetch(origin, "/api/auth/session")
        assert status == 502 and "no-store" in headers.get("Cache-Control", "")
        report["checks"].append(
            {"platform_intentionally_absent": True, "status": status}
        )
        success = True
    except Exception as error:
        report["failure"] = {"phase": phase, "type": type(error).__name__}
        if phase == "build" and isinstance(
            error, (subprocess.TimeoutExpired, subprocess.CalledProcessError)
        ):
            report["build_progress"] = build_progress(error.stdout, error.stderr)
        if isinstance(error, subprocess.CalledProcessError):
            report["failure"]["exit_code"] = error.returncode
        # Never upload captured container/build output or response bodies.
    finally:
        try:
            if container:
                command("docker", "rm", "--force", container, timeout=30)
            if "image_id" in report:
                command("docker", "image", "rm", "--force", tag, timeout=30)
        except (OSError, subprocess.SubprocessError):
            report["cleanup_failed"] = True
            success = False
        report["passed"] = success
        (evidence / "gateway-container-smoke.json").write_text(
            json.dumps(report, indent=2) + "\n"
        )
    print(
        json.dumps(
            {
                "passed": success,
                "checks": len(report["checks"]),
                "report": ".local/evidence/gateway-container-smoke.json",
            }
        )
    )
    raise SystemExit(0 if success else 1)


if __name__ == "__main__":
    main()
