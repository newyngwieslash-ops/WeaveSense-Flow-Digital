# -*- coding: utf-8 -*-
import base64
import hashlib
import hmac
import json
import tempfile
import threading
import time
import unittest
import urllib.error
import urllib.request

from mathsvc import server

KEY = b"k" * 40
PROB = {"family_id": "SOLID-ANGLE-02", "params": {"a": "2", "h": "1"}, "unit": "cm"}


def ctx(tenant, ttl=60):
    c = {"tenant_id": tenant, "actor_id": "u1", "aud": "math-service", "exp": int(time.time()) + ttl}
    return base64.urlsafe_b64encode(json.dumps(c).encode()).decode().rstrip("=")


class ServerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.httpd, cls.app = server.serve(KEY, tempfile.mkdtemp(), "127.0.0.1:0")
        cls.base = "http://127.0.0.1:%d" % cls.httpd.server_address[1]
        threading.Thread(target=cls.httpd.serve_forever, daemon=True).start()

    @classmethod
    def tearDownClass(cls):
        cls.app.worker.stop.set()
        cls.httpd.shutdown()

    def call(self, method, path, body=None, tenant="t1", sign=True, key=KEY):
        raw = json.dumps(body).encode() if body is not None else b""
        req = urllib.request.Request(self.base + path, data=raw if method == "POST" else None, method=method)
        if sign:
            c = ctx(tenant)
            msg = "\n".join([c, method, path, hashlib.sha256(raw).hexdigest()]).encode()
            req.add_header("X-Math-Context", c)
            req.add_header("X-Math-Signature", hmac.new(key, msg, hashlib.sha256).hexdigest())
        try:
            with urllib.request.urlopen(req) as r:
                return r.status, r.read(), dict(r.headers)
        except urllib.error.HTTPError as e:
            return e.code, e.read(), dict(e.headers)

    def mcp(self, method, params=None, tenant="t1", mid=1):
        c = ctx(tenant, 3600)
        tok = "v1.%s.%s" % (c, hmac.new(KEY, ("mcp\n" + c).encode(), hashlib.sha256).hexdigest())
        req = urllib.request.Request(self.base + "/mcp", data=json.dumps({"jsonrpc": "2.0", "id": mid, "method": method, "params": params or {}}).encode(),
                                     method="POST", headers={"Authorization": "Bearer " + tok, "Content-Type": "application/json"})
        with urllib.request.urlopen(req) as r:
            return json.loads(r.read())

    def wait(self, jid, tenant="t1"):
        for _ in range(200):
            st, raw, _ = self.call("GET", "/v1/jobs/" + jid, tenant=tenant)
            v = json.loads(raw)
            if v["status"] in ("succeeded", "failed", "cancelled"):
                return v
            time.sleep(0.1)
        self.fail("job did not finish")

    def test_auth_required_and_bad_signature(self):
        st, _, _ = self.call("GET", "/v1/catalog", sign=False)
        self.assertEqual(st, 403)
        st, _, _ = self.call("GET", "/v1/catalog", key=b"z" * 40)
        self.assertEqual(st, 403)
        st, raw, _ = self.call("GET", "/v1/catalog")
        self.assertEqual(st, 200)
        fams = {f["id"]: f["admission"] for f in json.loads(raw)["families"]}
        self.assertEqual(fams["SOLID-ANGLE-02"], "dev")

    def test_rest_end_to_end(self):
        body = {"schema_version": "edu.math.request/1", "kind": "analyze", "idempotency_key": "rest-1", "request_id": "r1", "payload": {"problem": PROB}}
        st, raw, _ = self.call("POST", "/v1/jobs", body)
        self.assertEqual(st, 202)
        j = json.loads(raw)
        st2, raw2, _ = self.call("POST", "/v1/jobs", body)
        self.assertEqual((st2, json.loads(raw2)["job_id"]), (200, j["job_id"]))
        v = self.wait(j["job_id"])
        self.assertEqual(v["status"], "succeeded")
        st, raw, _ = self.call("POST", "/v1/reconcile", {"kind": "analyze", "idempotency_key": "rest-1"})
        self.assertTrue(json.loads(raw)["found"])
        aid = [r for r in v["result_refs"] if r["kind"] == "solution"][0]["artifact_id"]
        st, raw, hd = self.call("GET", "/v1/artifacts/" + aid)
        self.assertEqual(st, 200)
        self.assertEqual(json.loads(raw)["answer"]["exact"], "2*sqrt(22)/11")
        st, _, _ = self.call("GET", "/v1/artifacts/" + aid, tenant="t9")
        self.assertEqual(st, 403)

    def test_mcp_protocol(self):
        init = self.mcp("initialize", {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "t", "version": "1"}})
        self.assertEqual(init["result"]["serverInfo"]["name"], "edu-math")
        names = [t["name"] for t in self.mcp("tools/list")["result"]["tools"]]
        for n in ["math_analyze_start", "math_lesson_build_start", "math_media_render_start", "math_job_get", "math_job_cancel", "math_call_reconcile", "math_artifact_get"]:
            self.assertIn(n, names)
        r = self.mcp("tools/call", {"name": "math_analyze_start", "arguments": {"problem": PROB}, "_meta": {"idempotency_key": "mcp-1"}})
        job = json.loads(r["result"]["content"][0]["text"])
        rec = json.loads(self.mcp("tools/call", {"name": "math_call_reconcile", "arguments": {"idempotency_key": "mcp-1"}})["result"]["content"][0]["text"])
        self.assertEqual(rec["status"], "succeeded")
        self.assertEqual(json.loads(rec["output"])["job_id"], job["job_id"])
        nf = json.loads(self.mcp("tools/call", {"name": "math_call_reconcile", "arguments": {"idempotency_key": "nope"}})["result"]["content"][0]["text"])
        self.assertEqual(nf["status"], "not_found")
        media = self.mcp("tools/call", {"name": "math_media_render_start", "arguments": {"lesson_artifact_id": "x"}})
        self.assertTrue(media["result"]["isError"])
        bad = self.mcp("tools/call", {"name": "math_analyze_start", "arguments": {"problem": dict(PROB, family_id="HYPERBOLA-ECC-01")}, "_meta": {"idempotency_key": "mcp-2"}})
        j2 = json.loads(bad["result"]["content"][0]["text"])
        v = self.wait(j2["job_id"])
        self.assertEqual(v["error"]["code"], "UNSUPPORTED_FAMILY")


if __name__ == "__main__":
    unittest.main()
