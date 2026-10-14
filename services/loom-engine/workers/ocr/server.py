"""OCR worker for WeaveSense-Flow (§6.4, compose profile "extract").

POST /v1/ocr  {"object_key", "tenant_id", "workspace_id"}  (Bearer OCR_SERVICE_TOKEN)
  -> 200 {"object_key", "text", "pages", "sha256"}

The object must live under tenants/<tenant>/workspaces/<workspace>/ of the caller's scope
(§10.4), is read from the private bucket with the worker's own credentials, size-capped,
and recognised with the tesseract CLI (default languages chi_sim+eng). Only images are
accepted; anything else is 415 "unsupported". No network egress besides object storage.
"""
from __future__ import annotations

import hashlib
import hmac
import json
import os
import re
import subprocess
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOKEN = os.environ.get("OCR_SERVICE_TOKEN", "")
BUCKET = os.environ.get("MINIO_BUCKET", "flow-media")
MAX_BYTES = int(os.environ.get("OCR_MAX_BYTES", str(20 * 1024 * 1024)))
LANGS = os.environ.get("OCR_LANGS", "chi_sim+eng")
TIMEOUT = int(os.environ.get("OCR_TIMEOUT_SECONDS", "90"))
SCOPE_ID = re.compile(r"[A-Za-z0-9_-]{1,64}")
KEY = re.compile(r"tenants/([A-Za-z0-9_-]{1,64})/workspaces/([A-Za-z0-9_-]{1,64})/[A-Za-z0-9_./-]{1,300}")
IMAGE_MAGIC = (b"\x89PNG", b"\xff\xd8\xff", b"GIF8", b"RIFF", b"II*\x00", b"MM\x00*", b"BM")


class OcrError(Exception):
    def __init__(self, code: str, status: int):
        super().__init__(code)
        self.code, self.status = code, status


def default_store():
    from minio import Minio  # lazy: tests and --help do not need the SDK

    return Minio(os.environ.get("MINIO_ENDPOINT", "minio:9000"), access_key=os.environ.get("MINIO_ACCESS_KEY", ""),
                 secret_key=os.environ.get("MINIO_SECRET_KEY", ""), secure=os.environ.get("MINIO_SECURE", "false") == "true")


def read_object(store, key: str) -> bytes:
    try:
        resp = store.get_object(BUCKET, key)
    except Exception as exc:  # NoSuchKey and friends
        raise OcrError("not_found", 404) from exc
    try:
        data = resp.read(MAX_BYTES + 1)
    finally:
        resp.close()
        resp.release_conn()
    if len(data) > MAX_BYTES:
        raise OcrError("too_large", 413)
    return data


def run_tesseract(data: bytes) -> str:
    try:
        out = subprocess.run(["tesseract", "stdin", "stdout", "-l", LANGS], input=data, capture_output=True, timeout=TIMEOUT, check=False)
    except subprocess.TimeoutExpired as exc:
        raise OcrError("timeout", 504) from exc
    except FileNotFoundError as exc:
        raise OcrError("unavailable", 503) from exc
    if out.returncode != 0:
        raise OcrError("unsupported", 415)
    return out.stdout.decode("utf-8", "replace").strip()


def ocr(body, store, runner=run_tesseract) -> dict:
    if not isinstance(body, dict) or set(body) != {"object_key", "tenant_id", "workspace_id"} or not all(isinstance(body[k], str) for k in body):
        raise OcrError("invalid_request", 400)
    if not SCOPE_ID.fullmatch(body["tenant_id"]) or not SCOPE_ID.fullmatch(body["workspace_id"]):
        raise OcrError("invalid_request", 400)
    m = KEY.fullmatch(body["object_key"])
    if not m or ".." in body["object_key"] or m.group(1) != body["tenant_id"] or m.group(2) != body["workspace_id"]:
        raise OcrError("forbidden", 403)
    data = read_object(store, body["object_key"])
    if not data.startswith(IMAGE_MAGIC):
        raise OcrError("unsupported", 415)
    text = runner(data)[:200_000]
    return {"object_key": body["object_key"], "text": text, "pages": 1, "sha256": hashlib.sha256(data).hexdigest()}


def make_handler(store_factory=default_store, runner=run_tesseract, token: str | None = None):
    state: dict = {}

    class Handler(BaseHTTPRequestHandler):
        def _send(self, status: int, payload: dict):
            raw = json.dumps(payload, ensure_ascii=False).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def log_message(self, *args):  # no request logging (object keys are tenant data)
            pass

        def do_GET(self):
            self._send(200, {"ok": True}) if self.path == "/healthz" else self._send(404, {"error": "not_found"})

        def do_POST(self):
            tok = TOKEN if token is None else token
            if self.path != "/v1/ocr":
                return self._send(404, {"error": "not_found"})
            if not tok:
                return self._send(503, {"error": "unavailable"})
            if not hmac.compare_digest(self.headers.get("Authorization", "").encode(), f"Bearer {tok}".encode()):
                return self._send(401, {"error": "unauthorized"})
            try:
                length = int(self.headers.get("Content-Length", "0"))
                body = json.loads(self.rfile.read(min(length, 8192)) or b"null") if 0 < length <= 8192 else None
                if "store" not in state:
                    state["store"] = store_factory()
                self._send(200, ocr(body, state["store"], runner))
            except OcrError as exc:
                self._send(exc.status, {"error": exc.code})
            except (ValueError, json.JSONDecodeError):
                self._send(400, {"error": "invalid_request"})

    return Handler


if __name__ == "__main__":
    port = int(os.environ.get("OCR_PORT", "8796"))
    ThreadingHTTPServer(("0.0.0.0", port), make_handler()).serve_forever()
