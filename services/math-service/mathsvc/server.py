# -*- coding: utf-8 -*-
"""HTTP entry: internal REST (platform adapter) + MCP Streamable HTTP (JSON responses).

Environment:
  MATH_SERVICE_KEY   shared HMAC key with the platform (>= 32 bytes), required
  MATH_DATA_DIR      job store + artifacts directory (default ./data)
  MATH_LISTEN        host:port (default 127.0.0.1:8790)
"""

import json
import os
import re
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from . import auth, catalog
from .errors import MathError
from .jobs import Store, Worker, isolated_stage

MAX_BODY = 64 * 1024
REQUEST_SCHEMA = "edu.math.request/1"
SERVICE_VERSION = "math-service/0.1.0"
TOOL_KIND = {"math_analyze_start": "analyze", "math_lesson_build_start": "lesson_build"}

TOOLS = [
    {"name": "math_analyze_start", "description": "Start an asynchronous analysis (validate, solve, verify) of a confirmed structured problem in a supported family. Returns a job; poll with math_job_get.",
     "inputSchema": {"type": "object", "required": ["problem"], "properties": {
         "problem": {"type": "object", "description": "Structured problem: family_id, params, unit, target, method_profile"},
         "problem_hash": {"type": "string"}, "idempotency_key": {"type": "string"}}}},
    {"name": "math_lesson_build_start", "description": "Start building a lesson, student projection and silent preview from a succeeded analysis job.",
     "inputSchema": {"type": "object", "required": ["analysis_job_id", "problem"], "properties": {
         "analysis_job_id": {"type": "string"}, "problem": {"type": "object"}, "problem_hash": {"type": "string"},
         "solution_hash": {"type": "string"}, "report_hash": {"type": "string"}, "idempotency_key": {"type": "string"}}}},
    {"name": "math_media_render_start", "description": "Start narration (TTS) or MP4 rendering for an approved lesson. Requires a configured provider.",
     "inputSchema": {"type": "object", "required": ["lesson_artifact_id"], "properties": {"lesson_artifact_id": {"type": "string"}, "media": {"type": "string", "enum": ["tts", "mp4"]}}}},
    {"name": "math_job_get", "description": "Get the status, stage, progress, result refs and error of a math job.",
     "inputSchema": {"type": "object", "required": ["job_id"], "properties": {"job_id": {"type": "string"}}}},
    {"name": "math_job_cancel", "description": "Request cancellation of a math job (cooperative, between stages).",
     "inputSchema": {"type": "object", "required": ["job_id"], "properties": {"job_id": {"type": "string"}}}},
    {"name": "math_call_reconcile", "description": "Look up what happened to a start call by idempotency key (never re-executes).",
     "inputSchema": {"type": "object", "required": ["idempotency_key"], "properties": {"idempotency_key": {"type": "string"}}}},
    {"name": "math_artifact_get", "description": "Get artifact metadata (and JSON content for small teacher JSON artifacts). HTML and media are retrieved through the platform's signed URLs only.",
     "inputSchema": {"type": "object", "required": ["artifact_id"], "properties": {"artifact_id": {"type": "string"}}}},
]


class App:
    def __init__(self, key, data_dir):
        if len(key) < 32:
            raise SystemExit("MATH_SERVICE_KEY must be at least 32 bytes")
        self.key = key
        self.store = Store(data_dir)
        self.worker = Worker(self.store)

    def start_job(self, tenant, kind, body):
        if body.get("schema_version", REQUEST_SCHEMA) != REQUEST_SCHEMA:
            raise MathError("REVISION_CONFLICT", "unsupported request schema_version")
        payload = body.get("payload")
        if not isinstance(payload, dict) or not isinstance(payload.get("problem"), dict):
            raise MathError("INPUT_AMBIGUOUS", "payload.problem is required")
        if kind == "lesson_build":
            self.worker.analysis_of(tenant, payload.get("analysis_job_id", ""))
        job, created = self.store.start(tenant, kind, body.get("idempotency_key", ""), payload,
                                        str(body.get("request_id", ""))[:64], str(body.get("trace_id", ""))[:64])
        self.worker.wake.set()
        return job, created

    def grade(self, body):
        """Synchronous objective judging, run in the same isolated subprocess as job stages."""
        out, ms = isolated_stage("grade", body if isinstance(body, dict) else {}, {}, 10)
        if not out.get("ok"):
            e = out["error"]
            raise MathError(e.get("code", "INTERNAL"), e.get("message", "grade failed"))
        return dict(out["result"], elapsed_ms=ms)

    def mcp_call(self, tenant, name, args, meta):
        if name in TOOL_KIND:
            key = (meta or {}).get("idempotency_key") or args.get("idempotency_key", "")
            payload = {k: v for k, v in args.items() if k != "idempotency_key"}
            job, _ = self.start_job(tenant, TOOL_KIND[name], {"idempotency_key": key, "payload": payload})
            return job
        if name == "math_media_render_start":
            raise MathError("PROVIDER_UNAVAILABLE", "no TTS/MP4 provider is configured for this service")
        if name == "math_job_get":
            return self.store.get(tenant, args.get("job_id", ""))
        if name == "math_job_cancel":
            return self.store.cancel(tenant, args.get("job_id", ""))
        if name == "math_call_reconcile":
            key = args.get("idempotency_key", "")
            for kind in TOOL_KIND.values():
                j = self.store.find_by_key(tenant, kind, key)
                if j:
                    return {"status": "succeeded", "output": json.dumps(j, ensure_ascii=False)}
            return {"status": "not_found"}
        if name == "math_artifact_get":
            meta_, data = self.store.get_artifact(tenant, args.get("artifact_id", ""))
            if meta_["audience"] == "teacher" and meta_["media_type"] == "application/json" and len(data) <= 12000:
                meta_["content"] = json.loads(data)
            return meta_
        raise MathError("UNSUPPORTED_FAMILY", "unknown tool")


class Handler(BaseHTTPRequestHandler):
    app = None
    server_version = SERVICE_VERSION

    def log_message(self, fmt, *args):  # quiet; no request bodies in logs
        pass

    def _send(self, code, obj, headers=None):
        raw = json.dumps(obj, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(raw)

    def _body(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n > MAX_BODY:
            raise MathError("PARAMETER_LIMIT", "request body too large")
        return self.rfile.read(n) if n else b""

    def _err(self, e, code=400):
        status = {"POLICY_REVOKED": 403, "REVISION_CONFLICT": 409, "INTERNAL": 500}.get(e.code, code)
        self._send(status, {"error": e.to_dict()})

    def do_GET(self):
        if self.path == "/healthz":
            return self._send(200, {"ok": True, "version": SERVICE_VERSION, "catalog_version": catalog.CATALOG_VERSION})
        try:
            ctx = auth.verify_rest(self.app.key, self.headers, "GET", self.path, b"")
            t = ctx["tenant_id"]
            if self.path == "/v1/catalog":
                return self._send(200, catalog.catalog())
            m = re.fullmatch(r"/v1/jobs/(mj_[0-9a-f]{32})", self.path)
            if m:
                return self._send(200, self.app.store.get(t, m.group(1)))
            m = re.fullmatch(r"/v1/artifacts/(ma_[0-9a-f]{32})", self.path)
            if m:
                meta, data = self.app.store.get_artifact(t, m.group(1))
                self.send_response(200)
                self.send_header("Content-Type", meta["media_type"])
                self.send_header("Content-Length", str(len(data)))
                self.send_header("X-Artifact-Sha256", meta["sha256"])
                self.send_header("X-Artifact-Audience", meta["audience"])
                self.send_header("X-Artifact-Kind", meta["kind"])
                self.end_headers()
                self.wfile.write(data)
                return
            self._send(404, {"error": {"code": "NOT_FOUND"}})
        except MathError as e:
            self._err(e)

    def do_POST(self):
        try:
            body = self._body()
            if self.path == "/mcp":
                return self._mcp(body)
            ctx = auth.verify_rest(self.app.key, self.headers, "POST", self.path, body)
            t = ctx["tenant_id"]
            req = json.loads(body or b"{}")
            if self.path == "/v1/jobs":
                kind = req.get("kind")
                if kind not in ("analyze", "lesson_build", "variant_set", "lesson_edit"):
                    raise MathError("UNSUPPORTED_FAMILY", "unknown job kind")
                job, created = self.app.start_job(t, kind, req)
                return self._send(202 if created else 200, job)
            m = re.fullmatch(r"/v1/jobs/(mj_[0-9a-f]{32})/cancel", self.path)
            if m:
                return self._send(200, self.app.store.cancel(t, m.group(1)))
            if self.path == "/v1/grade":
                return self._send(200, self.app.grade(req))
            if self.path == "/v1/reconcile":
                j = self.app.store.find_by_key(t, req.get("kind", ""), req.get("idempotency_key", ""))
                return self._send(200, {"found": bool(j), "job": j})
            self._send(404, {"error": {"code": "NOT_FOUND"}})
        except MathError as e:
            self._err(e)
        except ValueError:
            self._err(MathError("INPUT_AMBIGUOUS", "invalid JSON"))

    def _mcp(self, body):
        try:
            ctx = auth.verify_bearer(self.app.key, self.headers.get("Authorization", ""))
        except MathError as e:
            return self._send(401, {"error": e.to_dict()})
        try:
            msg = json.loads(body)
        except ValueError:
            return self._send(400, {"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "parse error"}})
        if isinstance(msg, list):
            return self._send(400, {"jsonrpc": "2.0", "id": None, "error": {"code": -32600, "message": "batch not supported"}})
        mid, method, params = msg.get("id"), msg.get("method"), msg.get("params") or {}
        if mid is None:  # notification
            self.send_response(202)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        if method == "initialize":
            res = {"protocolVersion": params.get("protocolVersion", "2025-06-18"), "capabilities": {"tools": {"listChanged": False}},
                   "serverInfo": {"name": "edu-math", "version": SERVICE_VERSION}}
        elif method == "ping":
            res = {}
        elif method == "tools/list":
            res = {"tools": TOOLS}
        elif method == "tools/call":
            try:
                out = self.app.mcp_call(ctx["tenant_id"], params.get("name", ""), params.get("arguments") or {}, params.get("_meta"))
                res = {"content": [{"type": "text", "text": json.dumps(out, ensure_ascii=False)}], "isError": False}
            except MathError as e:
                res = {"content": [{"type": "text", "text": json.dumps({"error": e.to_dict()}, ensure_ascii=False)}], "isError": True}
        else:
            return self._send(200, {"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": "method not found"}})
        self._send(200, {"jsonrpc": "2.0", "id": mid, "result": res})


def serve(key, data_dir, listen):
    app = App(key, data_dir)
    Handler.app = app
    host, port = listen.rsplit(":", 1)
    httpd = ThreadingHTTPServer((host, int(port)), Handler)
    threading.Thread(target=app.worker.loop, daemon=True).start()
    url = os.environ.get("MATH_EVENT_URL", "")
    if url:
        from .events import Notifier
        app.notifier = Notifier(app.store, url, key)
        threading.Thread(target=app.notifier.loop, daemon=True).start()
    return httpd, app


def main():
    key = os.environ.get("MATH_SERVICE_KEY", "").encode()
    httpd, _ = serve(key, os.environ.get("MATH_DATA_DIR", "./data"), os.environ.get("MATH_LISTEN", "127.0.0.1:8790"))
    httpd.serve_forever()


if __name__ == "__main__":
    main()
