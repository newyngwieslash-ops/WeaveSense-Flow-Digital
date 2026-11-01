"""Fixed isolated parser supervision; never fall back from Docker to Popen."""

import hashlib
import io
import json
import os
import re
import selectors
import shutil
import signal
import stat
import subprocess
import tempfile
import time
from pathlib import Path
from uuid import uuid4

from .limits import IngestionDenied, ParseLimits


class DockerSandbox:
    isolation = "container-network-none-readonly"

    def __init__(self, image):
        if not isinstance(image, str) or not re.fullmatch(
            r"(?:[a-z0-9][a-z0-9./:_-]*@)?sha256:[0-9a-f]{64}", image
        ):
            raise IngestionDenied("parser_sandbox_unavailable")
        self.binary = shutil.which("docker")
        if not self.binary or os.geteuid() == 0:
            raise IngestionDenied("parser_sandbox_unavailable")
        self.image = image

    def command(self, directory, limits, operation_id):
        # The only writable paths are size-limited anonymous tmpfs mounts.
        return [
            self.binary,
            "run",
            "--rm",
            "--pull=never",
            "--name",
            operation_id,
            "--network",
            "none",
            "--read-only",
            "--cap-drop",
            "ALL",
            "--security-opt",
            "no-new-privileges",
            "--pids-limit",
            "32",
            "--memory",
            str(limits.memory_bytes),
            "--memory-swap",
            str(limits.memory_bytes),
            "--cpus",
            "1",
            "--user",
            f"{os.geteuid()}:{os.getegid()}",
            "--mount",
            f"type=bind,source={directory},target=/input,readonly",
            "--tmpfs",
            f"/tmp:rw,noexec,nosuid,nodev,size={limits.expanded_bytes}",
            "--env",
            "OPENBLAS_NUM_THREADS=1",
            "--env",
            "OMP_NUM_THREADS=1",
            "--env",
            "PROJ_NETWORK=OFF",
            "--env",
            "PYTHONDONTWRITEBYTECODE=1",
            self.image,
            "/input/request.json",
        ]

    def cleanup(self, operation_id):
        # Only the unpredictable name generated for this exact invocation.
        removed = subprocess.run(
            [self.binary, "rm", "-f", operation_id],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            timeout=10,
            check=False,
            env={"PATH": os.defpath},
        )
        if removed.returncode:
            healthy = subprocess.run(
                [self.binary, "info"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=10,
                check=False,
                env={"PATH": os.defpath},
            )
            exists = subprocess.run(
                [self.binary, "container", "inspect", operation_id],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=10,
                check=False,
                env={"PATH": os.defpath},
            )
            if healthy.returncode or exists.returncode == 0:
                raise IngestionDenied("parser_cleanup_failed")


def configured_parser(root):
    """Operator-only configuration. Missing isolation is a dependency failure."""
    return SafeParser(root, DockerSandbox(os.environ.get("AGRI_PARSER_IMAGE")))


class SafeParser:
    def __init__(self, root, sandbox, limits=None):
        self.root = Path(root)
        if not self.root.is_absolute() or self.root.resolve() != self.root:
            raise IngestionDenied("invalid_parser_scratch_root")
        if sandbox is None:
            raise IngestionDenied("parser_sandbox_unavailable")
        self.sandbox, self.limits = sandbox, limits or ParseLimits()

    def inspect_stream(self, stream, *, asset_type, filename, operation="inspect"):
        # Upload transport is separate. Never spool an arbitrary network/pipe
        # reader here before the parser's wall-clock supervision has started.
        if not isinstance(stream, io.BytesIO):
            try:
                regular = stat.S_ISREG(os.fstat(stream.fileno()).st_mode)
            except (OSError, AttributeError, ValueError):
                regular = False
            if not regular:
                raise IngestionDenied("parser_requires_completed_local_input")
        if (
            not isinstance(filename, str)
            or len(filename) > 180
            or Path(filename).name != filename
            or any(c in filename for c in "/\\\x00")
            or operation not in {"inspect", "field_workbook"}
            or asset_type
            not in {
                "imagery",
                "dem",
                "vector",
                "table",
                "control",
                "weather",
                "management",
                "uav",
                "iot",
            }
        ):
            raise IngestionDenied("invalid_parser_request")
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        limits = self.limits
        operation_id = "agri-parser-" + uuid4().hex
        with tempfile.TemporaryDirectory(
            prefix=operation_id + "-", dir=self.root
        ) as name:
            directory = Path(name)
            input_path = directory / ("input" + Path(filename).suffix.lower())
            size, digest = 0, hashlib.sha256()
            with input_path.open("xb") as output:
                while chunk := stream.read(65536):
                    size += len(chunk)
                    if size > limits.input_bytes:
                        raise IngestionDenied("parser_input_limit")
                    digest.update(chunk)
                    output.write(chunk)
            if not size:
                raise IngestionDenied("parser_input_limit")
            (directory / "request.json").write_text(
                json.dumps(
                    {
                        "operation": operation,
                        "asset_type": asset_type,
                        "filename": filename,
                        "limits": limits.wire(),
                    }
                )
            )
            try:
                value = self._execute(directory, operation_id)
            finally:
                try:
                    self.sandbox.cleanup(operation_id)
                except Exception:
                    raise IngestionDenied("parser_cleanup_failed") from None
            if (
                not isinstance(value, dict)
                or set(value) != {"ok", "result"}
                or value["ok"] is not True
            ):
                raise IngestionDenied("invalid_parser_response")
            return {
                "inspection": value["result"],
                "content_sha256": digest.hexdigest(),
                "size_bytes": size,
                "isolation": self.sandbox.isolation,
            }

    def _execute(self, directory, operation_id):
        limits = self.limits
        command = self.sandbox.command(directory, limits, operation_id)
        # No ambient proxy, database, cloud, model or signing credentials.
        environment = {
            "PATH": os.defpath,
            "LANG": "C.UTF-8",
            "HOME": str(directory),
            "TMPDIR": str(directory),
            "OPENBLAS_NUM_THREADS": "1",
            "OMP_NUM_THREADS": "1",
            "PROJ_NETWORK": "OFF",
            "PYTHONDONTWRITEBYTECODE": "1",
        }
        try:
            process = subprocess.Popen(
                command,
                cwd=directory,
                env=environment,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                start_new_session=True,
                close_fds=True,
            )
        except OSError:
            raise IngestionDenied("parser_sandbox_unavailable") from None
        output = bytearray()
        try:
            with selectors.DefaultSelector() as selector:
                selector.register(process.stdout, selectors.EVENT_READ)
                deadline = time.monotonic() + limits.wall_seconds
                while selector.get_map():
                    remaining = deadline - time.monotonic()
                    if remaining <= 0:
                        raise IngestionDenied("parser_timeout")
                    for key, _ in selector.select(min(remaining, 0.2)):
                        chunk = os.read(key.fileobj.fileno(), 4096)
                        if not chunk:
                            selector.unregister(key.fileobj)
                            continue
                        output.extend(chunk)
                        if len(output) > limits.output_bytes:
                            raise IngestionDenied("parser_output_limit")
                code = process.wait(timeout=max(0.01, deadline - time.monotonic()))
            try:
                result = json.loads(output)
            except Exception:
                raise IngestionDenied("parser_process_failed") from None
            if code != 0:
                reason = result.get("code") if isinstance(result, dict) else None
                if not isinstance(reason, str) or not re.fullmatch(
                    r"[a-z_]{1,80}", reason
                ):
                    reason = "parser_process_failed"
                raise IngestionDenied(reason)
            return result
        except subprocess.TimeoutExpired:
            raise IngestionDenied("parser_timeout") from None
        finally:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
            process.wait(timeout=5)
            process.stdout.close()
