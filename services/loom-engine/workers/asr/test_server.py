import json
import threading
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer

import server

MP3 = b"ID3\x04\x00" + b"0" * 64
KEY = "tenants/tn_a/workspaces/ws_a/telegram/chan/7"


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
        self.objects = objects

    def get_object(self, bucket, key):
        if key not in self.objects:
            raise KeyError(key)
        return Resp(self.objects[key])


def fake_runner(data, language):
    return "储能 电站 并网", 42, language or "zh", [{"start": 0.0, "end": 42.0, "text": "储能 电站 并网"}]


class AsrTest(unittest.TestCase):
    def test_transcribes_scoped_audio(self):
        out = server.asr({"object_key": KEY, "tenant_id": "tn_a", "workspace_id": "ws_a", "language": "zh"}, Store({KEY: MP3}), fake_runner)
        self.assertEqual(out["text"], "储能 电站 并网")
        self.assertEqual(out["seconds"], 42)
        self.assertEqual(out["language"], "zh")
        self.assertEqual(len(out["sha256"]), 64)

    def test_rejects_foreign_scope_and_traversal(self):
        for body in ({"object_key": KEY, "tenant_id": "tn_b", "workspace_id": "ws_a"},
                     {"object_key": "tenants/tn_a/workspaces/ws_a/../../x", "tenant_id": "tn_a", "workspace_id": "ws_a"}):
            with self.assertRaises(server.AsrError) as cm:
                server.asr(body, Store({KEY: MP3}), fake_runner)
            self.assertEqual(cm.exception.status, 403)

    def test_rejects_non_media_and_bad_requests(self):
        with self.assertRaises(server.AsrError) as cm:
            server.asr({"object_key": KEY, "tenant_id": "tn_a", "workspace_id": "ws_a"}, Store({KEY: b"\x89PNG...."}), fake_runner)
        self.assertEqual(cm.exception.code, "unsupported")
        for body in (None, {"object_key": KEY}, {"object_key": KEY, "tenant_id": "tn_a", "workspace_id": "ws_a", "extra": "x"},
                     {"object_key": KEY, "tenant_id": "tn_a", "workspace_id": "ws_a", "language": "zh;rm"}):
            with self.assertRaises(server.AsrError):
                server.asr(body, Store({KEY: MP3}), fake_runner)
        with self.assertRaises(server.AsrError) as cm:
            server.asr({"object_key": KEY, "tenant_id": "tn_a", "workspace_id": "ws_a"}, Store({}), fake_runner)
        self.assertEqual(cm.exception.status, 404)

    def test_media_magic(self):
        self.assertTrue(server.is_media(b"OggS...."))
        self.assertTrue(server.is_media(b"RIFF\x00\x00\x00\x00WAVEfmt "))
        self.assertTrue(server.is_media(b"\x00\x00\x00\x20ftypisom"))
        self.assertTrue(server.is_media(b"\xff\xfb\x90\x00"))
        self.assertFalse(server.is_media(b"%PDF-1.7"))

    def test_http_auth(self):
        srv = ThreadingHTTPServer(("127.0.0.1", 0), server.make_handler(lambda: Store({KEY: MP3}), fake_runner, token="t0k"))
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        url = f"http://127.0.0.1:{srv.server_address[1]}/v1/asr"
        body = json.dumps({"object_key": KEY, "tenant_id": "tn_a", "workspace_id": "ws_a"}).encode()
        try:
            with self.assertRaises(urllib.error.HTTPError) as cm:
                urllib.request.urlopen(urllib.request.Request(url, body, {"Authorization": "Bearer nope", "Content-Type": "application/json"}))
            self.assertEqual(cm.exception.code, 401)
            r = urllib.request.urlopen(urllib.request.Request(url, body, {"Authorization": "Bearer t0k", "Content-Type": "application/json"}))
            self.assertEqual(json.loads(r.read())["seconds"], 42)
        finally:
            srv.shutdown()

    def test_real_whisper_when_installed(self):
        try:
            import faster_whisper  # noqa: F401
        except ImportError:
            self.skipTest("faster-whisper not installed (image-level test)")
        import math
        import struct
        rate = 16000
        frames = b"".join(struct.pack("<h", int(8000 * math.sin(2 * math.pi * 440 * i / rate))) for i in range(rate))
        wav = b"RIFF" + struct.pack("<I", 36 + len(frames)) + b"WAVEfmt " + struct.pack("<IHHIIHH", 16, 1, 1, rate, rate * 2, 2, 16) + b"data" + struct.pack("<I", len(frames)) + frames
        out = server.asr({"object_key": KEY, "tenant_id": "tn_a", "workspace_id": "ws_a"}, Store({KEY: wav}))
        self.assertEqual(out["seconds"], 1)


if __name__ == "__main__":
    unittest.main()
