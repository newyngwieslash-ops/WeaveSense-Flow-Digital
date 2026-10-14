import io
import json
import shutil
import threading
import unittest
import urllib.request
from http.server import ThreadingHTTPServer

import server

PNG = b"\x89PNG\r\n\x1a\n" + b"0" * 32


class Resp:
    def __init__(self, data):
        self.data = data

    def read(self, n):
        return self.data[:n]

    def close(self):
        pass

    def release_conn(self):
        pass


class Store:
    def __init__(self, objects):
        self.objects, self.reads = objects, []

    def get_object(self, bucket, key):
        self.reads.append(key)
        if key not in self.objects:
            raise KeyError(key)
        return Resp(self.objects[key])


KEY = "tenants/tn_a/workspaces/ws_a/telegram/chan/1"


class OcrTest(unittest.TestCase):
    def setUp(self):
        self.store = Store({KEY: PNG, "tenants/tn_a/workspaces/ws_a/telegram/chan/2": b"%PDF-1.7"})
        self.srv = ThreadingHTTPServer(("127.0.0.1", 0), server.make_handler(lambda: self.store, lambda data: "储能 订单", token="tok"))
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()
        self.url = f"http://127.0.0.1:{self.srv.server_address[1]}/v1/ocr"

    def tearDown(self):
        self.srv.shutdown()

    def post(self, body, auth="Bearer tok"):
        req = urllib.request.Request(self.url, data=json.dumps(body).encode(), headers={"Authorization": auth, "Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(req) as r:
                return r.status, json.loads(r.read())
        except urllib.error.HTTPError as e:
            return e.code, json.loads(e.read())

    def test_ok_scoped_image(self):
        st, out = self.post({"object_key": KEY, "tenant_id": "tn_a", "workspace_id": "ws_a"})
        self.assertEqual(st, 200)
        self.assertEqual((out["object_key"], out["text"], out["pages"]), (KEY, "储能 订单", 1))

    def test_scope_mismatch_never_reads_storage(self):
        for body in ({"object_key": KEY, "tenant_id": "tn_b", "workspace_id": "ws_a"},
                     {"object_key": "telegram/chan/1", "tenant_id": "tn_a", "workspace_id": "ws_a"},
                     {"object_key": "tenants/tn_a/workspaces/ws_a/../../tn_b/x", "tenant_id": "tn_a", "workspace_id": "ws_a"}):
            self.assertEqual(self.post(body), (403, {"error": "forbidden"}))
        self.assertEqual(self.store.reads, [])

    def test_auth_unsupported_missing_and_bad_request(self):
        self.assertEqual(self.post({"object_key": KEY, "tenant_id": "tn_a", "workspace_id": "ws_a"}, "Bearer nope")[0], 401)
        self.assertEqual(self.post({"object_key": "tenants/tn_a/workspaces/ws_a/telegram/chan/2", "tenant_id": "tn_a", "workspace_id": "ws_a"}), (415, {"error": "unsupported"}))
        self.assertEqual(self.post({"object_key": "tenants/tn_a/workspaces/ws_a/telegram/chan/3", "tenant_id": "tn_a", "workspace_id": "ws_a"}), (404, {"error": "not_found"}))
        self.assertEqual(self.post({"object_key": KEY}), (400, {"error": "invalid_request"}))


@unittest.skipUnless(shutil.which("tesseract"), "tesseract not installed")
class RealTesseractTest(unittest.TestCase):
    def test_recognises_rendered_text(self):
        from PIL import Image, ImageDraw, ImageFont
        img = Image.new("L", (900, 160), 255)
        try:
            font = ImageFont.load_default(size=72)
        except TypeError:  # Pillow < 10.1
            font = ImageFont.load_default()
        ImageDraw.Draw(img).text((20, 30), "LITHIUM 2026", fill=0, font=font)
        buf = io.BytesIO()
        img.save(buf, format="PNG")
        server.LANGS = "eng"
        out = server.ocr({"object_key": KEY, "tenant_id": "tn_a", "workspace_id": "ws_a"}, Store({KEY: buf.getvalue()}))
        self.assertIn("LITHIUM", out["text"].upper())


if __name__ == "__main__":
    unittest.main()
