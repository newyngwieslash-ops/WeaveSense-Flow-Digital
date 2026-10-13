"""save_media with fixtures: size limits, timeout, object storage upload (no network)."""
from __future__ import annotations

import asyncio
import importlib.util
import pathlib
import unittest


class Doc:
    def __init__(self, size, mime):
        self.size, self.mime_type = size, mime


class Msg:
    def __init__(self, mid, data=b"", size=0, mime="video/mp4", delay=0.0):
        self.id, self.raw_text, self.web_preview, self.photo = mid, "", None, None
        self.document = Doc(size, mime)
        self.media = object()
        self._data, self._delay = data, delay

    async def download_media(self, file=None):
        await asyncio.sleep(self._delay)
        return self._data


class FakeMinio:
    def __init__(self):
        self.objects = {}

    def put_object(self, bucket, key, stream, length, content_type):
        self.objects[key] = (stream.read(), content_type)


class SaveMediaTests(unittest.TestCase):
    def setUp(self):
        path = pathlib.Path(__file__).with_name("server.py")
        spec = importlib.util.spec_from_file_location("tg_media", path)
        mod = importlib.util.module_from_spec(spec)
        try:
            spec.loader.exec_module(mod)
        except Exception as exc:  # pragma: no cover
            self.skipTest(f"server.py not importable: {exc}")
        mod.MessageMediaDocument = object  # every fixture counts as a document
        mod.MessageMediaPhoto = type("NoPhoto", (), {})
        mod.minio_client = FakeMinio()
        mod.MINIO_ENDPOINT, mod.MINIO_BUCKET, mod.MEDIA_MAX_BYTES, mod.MEDIA_TIMEOUT = "minio:9000", "loom", 1000, 0.2
        self.mod = mod

    def run_(self, msg):
        return asyncio.run(self.mod.save_media(msg, "chan"))

    def test_saves_media_within_limit(self):
        urls, _, status = self.run_(Msg(7, data=b"x" * 500, size=500))
        self.assertEqual(status, "saved")
        self.assertEqual(urls, ["http://minio:9000/loom/telegram/chan/7.mp4"])
        self.assertEqual(self.mod.minio_client.objects["telegram/chan/7.mp4"][0], b"x" * 500)

    def test_declared_size_over_limit_is_not_downloaded(self):
        self.assertEqual(self.run_(Msg(8, data=b"x" * 5, size=5000))[2], "too_large")
        self.assertNotIn("telegram/chan/8.mp4", self.mod.minio_client.objects)

    def test_actual_bytes_over_limit_rejected(self):
        self.assertEqual(self.run_(Msg(9, data=b"x" * 2000, size=0))[2], "too_large")

    def test_slow_download_times_out(self):
        self.assertEqual(self.run_(Msg(10, data=b"x", size=1, delay=1.0))[2], "timeout")

    def test_no_object_storage_means_disabled(self):
        self.mod.minio_client = None
        self.assertEqual(self.run_(Msg(11, data=b"x", size=1))[2], "disabled")


if __name__ == "__main__":
    unittest.main()
