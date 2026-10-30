"""Approved-source transport with address pinning, not an arbitrary URL API."""

import hashlib
import http.client
import io
import ipaddress
import json
import os
import re
import socket
import ssl
import subprocess
import sys
import tempfile
import time
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from urllib.parse import urljoin, urlsplit

from .limits import IngestionDenied

ID = re.compile(r"[A-Za-z][A-Za-z0-9_-]{0,127}\Z")


@dataclass(frozen=True)
class ApprovedSource:
    source_id: str
    origin: str
    path_prefixes: tuple[str, ...]
    artifacts: tuple[tuple[str, str], ...]
    approval_ref: str
    expires_at: datetime
    methods: tuple[str, ...] = ("GET",)

    def __post_init__(self):
        if (
            not isinstance(self.source_id, str)
            or not ID.fullmatch(self.source_id)
            or not isinstance(self.approval_ref, str)
            or not self.approval_ref
            or not isinstance(self.expires_at, datetime)
            or self.expires_at.tzinfo is None
            or not isinstance(self.path_prefixes, tuple)
            or not self.path_prefixes
            or not all(isinstance(p, str) for p in self.path_prefixes)
            or not isinstance(self.methods, tuple)
            or not self.methods
            or not set(self.methods) <= {"GET", "POST"}
            or not isinstance(self.artifacts, tuple)
            or not self.artifacts
        ):
            raise IngestionDenied("invalid_source_registry")
        seen = set()
        for item in self.artifacts:
            if not isinstance(item, tuple) or len(item) != 2:
                raise IngestionDenied("invalid_source_registry")
            key, url = item
            if not isinstance(key, str) or not ID.fullmatch(key) or key in seen:
                raise IngestionDenied("invalid_source_registry")
            seen.add(key)
            validate_url(self, url)

    def url_for(self, artifact_id):
        values = dict(self.artifacts)
        if len(values) != len(self.artifacts) or artifact_id not in values:
            raise IngestionDenied("unknown_source_artifact")
        return values[artifact_id]


def public_address(value):
    try:
        ip = ipaddress.ip_address(value)
        if isinstance(ip, ipaddress.IPv6Address):
            if (
                ip.ipv4_mapped
                or ip.sixtofour
                or ip.teredo
                or ip.scope_id
                or ip.is_site_local
                or ip in ipaddress.ip_network("64:ff9b::/96")
                or ip in ipaddress.ip_network("64:ff9b:1::/48")
            ):
                return False
        return ip.is_global and not (
            ip.is_multicast
            or ip.is_unspecified
            or ip.is_loopback
            or ip.is_link_local
            or ip.is_reserved
            or ip.is_private
        )
    except ValueError:
        return False


def validate_url(source, url):
    if not isinstance(url, str) or not url.isascii() or len(url) > 4096:
        raise IngestionDenied("unsafe_source_url")
    if any(ord(c) <= 32 or ord(c) == 127 for c in url) or "\\" in url:
        raise IngestionDenied("unsafe_source_url")
    try:
        parsed, origin = urlsplit(url), urlsplit(source.origin)
        if (
            parsed.scheme != "https"
            or parsed.username is not None
            or parsed.password is not None
            or parsed.fragment
            or parsed.query
            or parsed.port not in {None, 443}
            or not parsed.hostname
            or parsed.hostname != origin.hostname
            or origin.scheme != "https"
            or origin.path not in {"", "/"}
            or origin.query
            or origin.fragment
            or origin.username is not None
            or origin.port not in {None, 443}
            or parsed.hostname.endswith(".")
            or "%" in parsed.path
            or any(p in {".", ".."} for p in parsed.path.split("/"))
            or not any(
                parsed.path.startswith(prefix) for prefix in source.path_prefixes
            )
        ):
            raise IngestionDenied("unsafe_source_url")
        if not all(
            prefix.startswith("/")
            and prefix.endswith("/")
            and "%" not in prefix
            and ".." not in prefix
            for prefix in source.path_prefixes
        ):
            raise IngestionDenied("unsafe_source_policy")
        return parsed
    except (ValueError, TypeError):
        raise IngestionDenied("unsafe_source_url") from None


def lookup_addresses(hostname, timeout):
    # libc DNS may block independently of socket timeouts. A fixed, clean child
    # gives DNS its own bounded lifetime without leaking abandoned threads.
    code = (
        "import json,socket,sys; "
        "rows=socket.getaddrinfo(sys.argv[1],443,type=socket.SOCK_STREAM); "
        "a=sorted({r[4][0] for r in rows}); "
        "print(json.dumps(a if len(a)<=64 else []))"
    )
    result = subprocess.run(
        [sys.executable, "-I", "-c", code, hostname],
        capture_output=True,
        timeout=min(timeout, 5),
        check=True,
        env={"PATH": os.defpath, "LANG": "C.UTF-8"},
    )
    if len(result.stdout) > 16384:
        raise IngestionDenied("source_dns_unavailable")
    return json.loads(result.stdout)


def resolve_public(hostname, timeout=5):
    try:
        addresses = lookup_addresses(hostname, timeout)
        if (
            not isinstance(addresses, list)
            or not addresses
            or not all(isinstance(ip, str) and public_address(ip) for ip in addresses)
        ):
            raise IngestionDenied("source_address_denied")
        return addresses
    except IngestionDenied:
        raise
    except Exception:
        raise IngestionDenied("source_dns_unavailable") from None


class PinnedHTTPSConnection(http.client.HTTPSConnection):
    def __init__(self, host, address, timeout):
        super().__init__(
            host, 443, timeout=timeout, context=ssl.create_default_context()
        )
        self.address = address

    def connect(self):
        # Connect only to the already checked numeric address: no second DNS
        # resolution. Certificate and SNI still use the original approved host.
        sock = socket.create_connection((self.address, 443), self.timeout)
        try:
            self.sock = self._context.wrap_socket(sock, server_hostname=self.host)
        except BaseException:
            sock.close()
            raise


class DeadlineReader(io.RawIOBase):
    def __init__(self, owner):
        self.owner = owner

    def readable(self):
        return True

    def readinto(self, buffer):
        remaining = self.owner.deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("response deadline")
        self.owner.socket.settimeout(remaining)
        return self.owner.socket.recv_into(buffer)

    def close(self):
        if not self.closed:
            self.owner.files -= 1
            self.owner.maybe_close()
        super().close()


class DeadlineSocket:
    """Keep the absolute budget even during slow-drip HTTP header parsing."""

    def __init__(self, sock, deadline):
        self.socket, self.deadline = sock, deadline
        self.files, self.closed = 0, False

    def makefile(self, mode):
        if mode != "rb":
            raise ValueError("read-only response socket")
        self.files += 1
        return io.BufferedReader(DeadlineReader(self))

    def maybe_close(self):
        if self.closed and not self.files:
            self.socket.close()

    def close(self):
        self.closed = True
        self.maybe_close()


class PublicHTTPS:
    def open(self, parsed, method, body, timeout):
        start = time.monotonic()
        addresses = resolve_public(parsed.hostname, timeout)
        remaining = timeout - (time.monotonic() - start)
        if remaining <= 0:
            raise IngestionDenied("source_timeout")
        connection = PinnedHTTPSConnection(parsed.hostname, addresses[0], remaining)
        try:
            connection.request(
                method,
                parsed.path or "/",
                body=body,
                headers={
                    "Accept": "application/json,application/octet-stream",
                    "Accept-Encoding": "identity",
                    "Connection": "close",
                    "Content-Type": "application/json",
                },
            )
            connection._agri_socket = connection.sock
            connection.sock = DeadlineSocket(connection.sock, start + timeout)
            return connection, connection.getresponse()
        except BaseException:
            connection.close()
            raise


class SafeDownloader:
    def __init__(
        self,
        sources,
        authorize_rights,
        *,
        transport=None,
        max_bytes=16 * 1024 * 1024,
        seconds=15,
    ):
        self.sources = {s.source_id: s for s in sources}
        if len(self.sources) != len(sources) or not callable(authorize_rights):
            raise IngestionDenied("invalid_source_registry")
        if (
            type(max_bytes) is not int
            or not 1 <= max_bytes <= 64 * 1024 * 1024
            or type(seconds) is not int
            or not 1 <= seconds <= 60
        ):
            raise IngestionDenied("invalid_download_limits")
        self.authorize_rights = authorize_rights
        # Test composition may provide a loopback fixture transport. There is
        # no production environment switch that relaxes PublicHTTPS IP policy.
        self.transport = transport or PublicHTTPS()
        self.max_bytes, self.seconds = max_bytes, seconds

    def check_rights(self, source_id):
        # Compatible with the existing source-rights.authorize result, never
        # interpret a callback that silently returns None/False as approval.
        result = self.authorize_rights(source_id, "ingest")
        if not isinstance(result, dict) or result.get("allowed") is not True:
            raise IngestionDenied("source_rights_denied")
        return result

    def download(
        self, source_id, artifact_id, directory, *, method="GET", payload=None
    ):
        if not all(
            isinstance(i, str) and ID.fullmatch(i) for i in (source_id, artifact_id)
        ):
            raise IngestionDenied("invalid_source_reference")
        source = self.sources.get(source_id)
        if (
            source is None
            or method not in source.methods
            or method not in {"GET", "POST"}
        ):
            raise IngestionDenied("source_not_approved")
        body = (
            None if payload is None else json.dumps(payload, allow_nan=False).encode()
        )
        if (method == "GET" and body is not None) or (
            body is not None and len(body) > 65536
        ):
            raise IngestionDenied("source_request_limit")
        directory = Path(directory)
        if (
            not directory.is_absolute()
            or directory.resolve() != directory
            or not directory.is_dir()
        ):
            raise IngestionDenied("invalid_download_directory")
        deadline = time.monotonic() + self.seconds
        url, temporary, completed = source.url_for(artifact_id), None, None
        published = completed_owned = False
        try:
            for hop in range(4):
                if method not in source.methods:
                    raise IngestionDenied("source_method_denied")
                if (
                    not isinstance(source.expires_at, datetime)
                    or source.expires_at.tzinfo is None
                    or datetime.now(UTC) >= source.expires_at
                    or not source.approval_ref
                ):
                    raise IngestionDenied("source_approval_expired")
                self.check_rights(source.source_id)
                parsed = validate_url(source, url)
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise IngestionDenied("source_timeout")
                connection, response = self.transport.open(
                    parsed, method, body, remaining
                )
                try:
                    if response.status in {301, 302, 303, 307, 308}:
                        location = response.getheader("Location")
                        if hop == 3 or not location:
                            raise IngestionDenied("source_redirect_limit")
                        url = urljoin(url, location)
                        validate_url(source, url)
                        if response.status == 303:
                            method, body = "GET", None
                        continue
                    if (
                        response.status != 200
                        or response.getheader("Content-Encoding", "identity")
                        != "identity"
                    ):
                        raise IngestionDenied("source_response_denied")
                    length = response.getheader("Content-Length")
                    if length is not None and (
                        not length.isdecimal() or int(length) > self.max_bytes
                    ):
                        raise IngestionDenied("source_body_limit")
                    fd, temporary = tempfile.mkstemp(
                        prefix="agri-download-", suffix=".part", dir=directory
                    )
                    total, digest = 0, hashlib.sha256()
                    with os.fdopen(fd, "wb") as output:
                        while True:
                            remaining = deadline - time.monotonic()
                            if remaining <= 0:
                                raise IngestionDenied("source_timeout")
                            sock = getattr(connection, "_agri_socket", None)
                            if sock is not None:
                                sock.settimeout(remaining)
                            chunk = response.read1(
                                min(65536, self.max_bytes - total + 1)
                            )
                            if not chunk:
                                break
                            total += len(chunk)
                            if total > self.max_bytes:
                                raise IngestionDenied("source_body_limit")
                            digest.update(chunk)
                            output.write(chunk)
                    if not total or (length is not None and total != int(length)):
                        raise IngestionDenied("source_body_incomplete")
                    # Recheck rights/approval before publishing a completed file.
                    rights = self.check_rights(source.source_id)
                    if datetime.now(UTC) >= source.expires_at:
                        raise IngestionDenied("source_approval_expired")
                    completed = str(temporary).removesuffix(".part") + ".complete"
                    os.link(temporary, completed)
                    completed_owned = True
                    os.unlink(temporary)
                    temporary = None
                    published = True
                    return {
                        "path": completed,
                        "size_bytes": total,
                        "content_sha256": digest.hexdigest(),
                        "source_id": source_id,
                        "approval_ref": source.approval_ref,
                        "rights": rights,
                    }
                finally:
                    response.close()
                    connection.close()
            raise IngestionDenied("source_redirect_limit")
        except IngestionDenied:
            raise
        except Exception:
            raise IngestionDenied("source_transport_failed") from None
        finally:
            if temporary is not None:
                Path(temporary).unlink(missing_ok=True)
            if completed_owned and not published:
                Path(completed).unlink(missing_ok=True)
