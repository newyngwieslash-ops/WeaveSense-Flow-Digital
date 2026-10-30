"""Local owned HTTP fixture; production transport never permits loopback."""

import hashlib
import http.client
import importlib.util
import json
import socket
import threading
import time
from datetime import UTC, datetime, timedelta
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import pytest

from app.agriculture.ingestion.download import (
    ApprovedSource,
    DeadlineSocket,
    SafeDownloader,
    public_address,
    resolve_public,
    validate_url,
)
from app.agriculture.ingestion.limits import IngestionDenied
from app.agriculture.ingestion.source_policy import RegistryRights


def source(path="ok"):
    return ApprovedSource(
        "fixture",
        "https://approved.example",
        ("/data/",),
        (("artifact", "https://approved.example/data/" + path),),
        "test-process-only",
        datetime.now(UTC) + timedelta(minutes=5),
    )


@pytest.mark.parametrize(
    "address",
    [
        "127.0.0.1",
        "10.1.2.3",
        "169.254.169.254",
        "0.0.0.0",
        "224.0.0.1",
        "::1",
        "fc00::1",
        "fe80::1",
        "::ffff:8.8.8.8",
        "2002:0808:0808::1",
        "2130706433",
        "127.1",
        "fe80::1%eth0",
        "fec0::1",
        "64:ff9b::a00:1",
        "64:ff9b:1::a00:1",
    ],
)
def test_non_public_addresses(address):
    assert not public_address(address)


@pytest.mark.parametrize(
    "url",
    [
        "http://approved.example/data/a",
        "https://user:pass@approved.example/data/a",
        "https://approved.example:444/data/a",
        "https://approved.example/data/a?token=x",
        "https://approved.example/data/a#x",
        "https://approved.example/data/%2e%2e/a",
        "https://approved.example/data/../a",
        "https://approved.example/other/a",
        "https://approved.example.evil/data/a",
        "https://approved.example./data/a",
        "https://approved.example/data/\\evil",
        "https://approved.example/data/a\n",
    ],
)
def test_url_aliases_and_credentials_denied(url):
    with pytest.raises(IngestionDenied):
        validate_url(source(), url)


def test_dns_rebinding_mixed_answers_fail_closed(monkeypatch):
    monkeypatch.setattr(
        "app.agriculture.ingestion.download.lookup_addresses",
        lambda *a, **k: ["8.8.8.8", "127.0.0.1"],
    )
    with pytest.raises(IngestionDenied, match="source_address_denied"):
        resolve_public("approved.example")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def do_GET(self):
        suffix = self.path.rsplit("/", 1)[-1]
        if suffix.startswith("redirect"):
            self.send_response(302)
            self.send_header(
                "Location",
                {
                    "redirect": "/data/ok",
                    "redirect_private": "https://169.254.169.254/data/a",
                    "redirect_path": "/private/a",
                    "redirect_loop": "/data/redirect_loop",
                }[suffix],
            )
            self.end_headers()
            return
        self.send_response(200)
        if suffix == "large":
            self.send_header("Content-Length", "100000")
        self.end_headers()
        try:
            if suffix == "slow":
                for _ in range(30):
                    self.wfile.write(b"x")
                    self.wfile.flush()
                    time.sleep(0.1)
            else:
                self.wfile.write(
                    b"real-local-bytes" if suffix != "overflow" else b"x" * 100
                )
        except (BrokenPipeError, ConnectionResetError):
            pass


@pytest.fixture
def transport():
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()

    class ExplicitLoopbackFixture:
        calls = 0

        def open(self, parsed, method, body, timeout):
            self.calls += 1
            conn = http.client.HTTPConnection(
                "127.0.0.1", server.server_port, timeout=timeout
            )
            conn.request(method, parsed.path, body)
            conn._agri_socket = conn.sock
            return conn, conn.getresponse()

    yield ExplicitLoopbackFixture()
    server.shutdown()
    server.server_close()
    thread.join()


@pytest.mark.parametrize("path", ["ok", "redirect"])
def test_real_stream_atomic_publication(path, transport, tmp_path):
    result = SafeDownloader(
        [source(path)], lambda *a: {"allowed": True}, transport=transport
    ).download("fixture", "artifact", tmp_path)
    assert Path(result["path"]).read_bytes() == b"real-local-bytes"
    assert result["content_sha256"] == hashlib.sha256(b"real-local-bytes").hexdigest()
    assert len(list(tmp_path.iterdir())) == 1


@pytest.mark.parametrize(
    "path",
    ["redirect_private", "redirect_path", "redirect_loop", "large", "overflow", "slow"],
)
def test_real_http_failure_cleans_owned_temp(path, transport, tmp_path):
    started = time.monotonic()
    with pytest.raises(IngestionDenied):
        SafeDownloader(
            [source(path)],
            lambda *a: {"allowed": True},
            transport=transport,
            max_bytes=32,
            seconds=1,
        ).download("fixture", "artifact", tmp_path)
    assert not list(tmp_path.iterdir())

    assert time.monotonic() - started < 2.5


def test_existing_w00_rights_engine_reused_and_revocation_reloaded():
    root = Path(__file__).resolve().parents[4]
    spec = importlib.util.spec_from_file_location(
        "ingestion_existing_rights", root / "contracts/agriculture/source-rights.py"
    )
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    manifest = {
        "schema_version": "1.0.0",
        "sources": [
            {
                "id": "fixture",
                "kind": "imagery",
                "enabled": True,
                "data_class": "operational",
                "source_ref": "synthetic-policy-test-only",
                "parent_ids": [],
                "rights": {
                    "status": "approved",
                    "owner": "SYNTHETIC TEST ONLY",
                    "license_ref": "test-only",
                    "commercial_use": "allowed",
                    "redistribution": "prohibited",
                    "permitted_uses": ["ingest"],
                    "attribution": {"required": False, "text": None},
                    "valid_from": (datetime.now(UTC) - timedelta(days=1)).isoformat(),
                    "expiry": (datetime.now(UTC) + timedelta(days=1)).isoformat(),
                    "review_ref": "test-only",
                    "evidence": [{"ref": "test-only", "sha256": "a" * 64}],
                },
            }
        ],
    }
    adapter = RegistryRights(lambda: manifest, module.authorize)
    assert adapter("fixture", "ingest")["allowed"] is True
    manifest["sources"][0]["rights"]["status"] = "revoked"
    with pytest.raises(IngestionDenied, match="source_rights_denied"):
        adapter("fixture", "ingest")
    actual = json.loads((root / "deploy/agriculture/sources.json").read_text())
    assert not actual["requested_uses"]


def test_pinned_connection_preserves_original_tls_host(monkeypatch):
    from app.agriculture.ingestion.download import PinnedHTTPSConnection

    calls = []

    class Socket:
        def close(self):
            calls.append("closed")

    class Context:
        def wrap_socket(self, sock, *, server_hostname):
            calls.append(("tls", server_hostname))
            return sock

    monkeypatch.setattr(
        "socket.create_connection",
        lambda address, timeout: calls.append((address, timeout)) or Socket(),
    )
    connection = PinnedHTTPSConnection("approved.example", "8.8.8.8", 2)
    connection._context = Context()
    connection.connect()
    assert calls == [(("8.8.8.8", 443), 2), ("tls", "approved.example")]
    connection.close()


def test_actual_slow_drip_headers_obey_absolute_deadline():
    receiver, sender = socket.socketpair()
    stopped = threading.Event()

    def drip():
        try:
            for byte in b"HTTP/1.1 200 OK\r\nContent-Length: 1\r\n\r\nx":
                if stopped.is_set():
                    break
                sender.sendall(bytes([byte]))
                time.sleep(0.08)
        except OSError:
            pass
        finally:
            sender.close()

    thread = threading.Thread(target=drip, daemon=True)
    thread.start()
    started = time.monotonic()
    wrapper = DeadlineSocket(receiver, started + 0.4)
    response = http.client.HTTPResponse(wrapper)
    try:
        with pytest.raises(TimeoutError):
            response.begin()
        assert time.monotonic() - started < 1
    finally:
        response.close()
        wrapper.close()
        stopped.set()
        thread.join(timeout=2)


@pytest.mark.parametrize("approval", [None, False, {}, {"allowed": False}])
def test_missing_rights_never_connect(approval, transport, tmp_path):
    with pytest.raises(IngestionDenied, match="source_rights_denied"):
        SafeDownloader([source()], lambda *a: approval, transport=transport).download(
            "fixture", "artifact", tmp_path
        )
    assert transport.calls == 0
    assert not list(tmp_path.iterdir())


def test_rights_revoked_after_actual_bytes_not_published(transport, tmp_path):
    count = 0

    def rights(*args):
        nonlocal count
        count += 1
        return {"allowed": count == 1}

    with pytest.raises(IngestionDenied, match="source_rights_denied"):
        SafeDownloader([source()], rights, transport=transport).download(
            "fixture", "artifact", tmp_path
        )
    assert transport.calls == 1 and count == 2
    assert not list(tmp_path.iterdir())


def test_collision_never_removes_preexisting_complete(monkeypatch, transport, tmp_path):
    def occupied(source, destination):
        Path(destination).write_bytes(b"preexisting-do-not-delete")
        raise FileExistsError()

    monkeypatch.setattr("app.agriculture.ingestion.download.os.link", occupied)
    with pytest.raises(IngestionDenied, match="source_transport_failed"):
        SafeDownloader(
            [source()], lambda *a: {"allowed": True}, transport=transport
        ).download("fixture", "artifact", tmp_path)
    files = list(tmp_path.iterdir())
    assert len(files) == 1 and files[0].suffix == ".complete"
    assert files[0].read_bytes() == b"preexisting-do-not-delete"
