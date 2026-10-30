"""Private immutable byte access. No URL fetching or user-selected filesystem paths."""

import hashlib
import os
import re
import stat
from pathlib import Path

from .identity.verify import IdentityError

IDENTIFIER = re.compile(r"[A-Za-z0-9_-]{1,128}\Z")
SHA256 = re.compile(r"[0-9a-f]{64}\Z")
OBJECT_ID = re.compile(r"[A-Za-z][A-Za-z0-9_-]{0,127}\Z")
MAX_OBJECT_BYTES = 64 * 1024 * 1024


def object_key(tenant, workspace, object_id, version):
    if not all(
        isinstance(v, str) and IDENTIFIER.fullmatch(v)
        for v in (tenant, workspace, object_id)
    ):
        raise ValueError("invalid object identity")
    if not OBJECT_ID.fullmatch(object_id):
        raise ValueError("invalid object identity")
    if type(version) is not int or not 1 <= version <= 9007199254740991:
        raise ValueError("invalid object version")
    return f"tenants/{tenant}/workspaces/{workspace}/objects/{object_id}/v{version}"


class PrivateLocalStorage:
    """Bounded local provider; use explicit adapters for large COG/S3 workloads.

    Opening every component with O_NOFOLLOW prevents symlink/path replacement
    from escaping the private root. The checked bytes, not a reopened pathname,
    are returned to the HTTP layer. Root provisioning is an operator action.
    """

    def __init__(self, root):
        root = Path(root)
        if not root.is_absolute() or root.resolve() != root or not root.is_dir():
            raise ValueError("private object root must be an absolute real path")
        self.root = root

    def read(self, *, tenant, workspace, object_id, version, key, size, sha256):
        expected = object_key(tenant, workspace, object_id, version)
        if (
            key != expected
            or type(size) is not int
            or not 0 <= size <= MAX_OBJECT_BYTES
        ):
            raise IdentityError("object_unavailable", 503)
        if not isinstance(sha256, str) or not SHA256.fullmatch(sha256):
            raise IdentityError("object_unavailable", 503)
        fds = []
        try:
            fd = os.open("/", os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
            fds.append(fd)
            # Include the root's ancestors: replacing one after initialization
            # must not turn a previously checked absolute path into an escape.
            parts = list(self.root.parts[1:]) + expected.split("/")
            for part in parts[:-1]:
                fd = os.open(
                    part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd
                )
                fds.append(fd)
            fd = os.open(
                parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=fd
            )
            fds.append(fd)
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_size != size:
                raise ValueError("invalid object bytes")
            chunks, remaining = [], size + 1
            while remaining:
                chunk = os.read(fd, min(remaining, 1024 * 1024))
                if not chunk:
                    break
                chunks.append(chunk)
                remaining -= len(chunk)
            body = b"".join(chunks)
            if len(body) != size or hashlib.sha256(body).hexdigest() != sha256:
                raise ValueError("object integrity mismatch")
            return body
        except (OSError, ValueError):
            raise IdentityError("object_unavailable", 503) from None
        finally:
            for fd in reversed(fds):
                os.close(fd)


def byte_range(value, length):
    """One inclusive range only; no multipart amplification or silent fallback."""
    if value is None:
        return 0, length
    if not isinstance(value, str) or len(value) > 80:
        raise IdentityError("invalid_object_range", 416)
    match = re.fullmatch(r"bytes=([0-9]*)-([0-9]*)", value)
    if not match or not any(match.groups()) or not length:
        raise IdentityError("invalid_object_range", 416)
    first, last = match.groups()
    if not first:
        count = int(last)
        if not count:
            raise IdentityError("invalid_object_range", 416)
        return max(0, length - count), length
    start = int(first)
    end = min(length, int(last) + 1) if last else length
    if start >= end or start >= length:
        raise IdentityError("invalid_object_range", 416)
    return start, end
