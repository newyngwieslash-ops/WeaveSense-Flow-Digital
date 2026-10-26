# -*- coding: utf-8 -*-
import json
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer

from mathsvc import events
from mathsvc.jobs import Store, Worker

PROB = {"problem": {"family_id": "SOLID-ANGLE-02", "params": {"a": "2", "h": "1"}, "unit": "cm"}}
KEY = b"k" * 32


class Sink(BaseHTTPRequestHandler):
    replies = []
    got = []

    def do_POST(self):
        body = self.rfile.read(int(self.headers["Content-Length"]))
        Sink.got.append((body, self.headers.get("X-Math-Event-Signature")))
        code = Sink.replies.pop(0) if Sink.replies else 202
        self.send_response(code)
        if code == 302:
            self.send_header("Location", "http://127.0.0.1:1/elsewhere")
        self.end_headers()

    def log_message(self, *a):
        pass


class EventTests(unittest.TestCase):
    def setUp(self):
        self.s = Store(tempfile.mkdtemp())
        self.w = Worker(self.s)

    def rows(self):
        return self.s.db.execute("SELECT job_id, seq, status, delivered_at, attempts FROM events ORDER BY seq").fetchall()

    def test_status_writes_record_outbox_rows_in_the_same_transaction(self):
        j, _ = self.s.start("t1", "analyze", "k", PROB, request_id="emj_1")
        for _ in range(8):
            self.w.step(j["job_id"])
        v = self.s.get("t1", j["job_id"])
        self.assertEqual(v["status"], "succeeded")
        rows = self.rows()
        self.assertEqual(rows[0][2], "queued")
        self.assertEqual(rows[-1][2], "succeeded")
        self.assertEqual(rows[-1][1], v["seq"])
        seqs = [r[1] for r in rows]
        self.assertEqual(seqs, sorted(set(seqs)))  # strictly increasing, one row per status write
        # A failing write records nothing.
        n = len(rows)
        with self.assertRaises(Exception):
            with self.s.lock:
                self.s._update(j["job_id"], status="failed", no_such_column=1)
        self.assertEqual(len(self.rows()), n)
        self.assertEqual(self.s.get("t1", j["job_id"])["status"], "succeeded")
        # Same key again: no new job, no new event.
        self.s.start("t1", "analyze", "k", PROB)
        self.assertEqual(len(self.rows()), n)

    def test_notifier_delivers_signed_at_least_once_with_backoff(self):
        srv = HTTPServer(("127.0.0.1", 0), Sink)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        self.addCleanup(srv.shutdown)
        Sink.got, Sink.replies = [], [500, 302]
        j, _ = self.s.start("t1", "analyze", "k2", PROB, request_id="emj_2")
        n = events.Notifier(self.s, "http://127.0.0.1:%d/api/edu/math-events" % srv.server_port, KEY)
        self.assertEqual(n.deliver_once(), 0)  # 500: kept, backed off
        r = self.rows()[0]
        self.assertIsNone(r[3])
        self.assertEqual(r[4], 1)
        self.assertEqual(n.deliver_once(), 0)  # still backing off: not even attempted
        self.assertEqual(len(Sink.got), 1)
        self.s.db.execute("UPDATE events SET next_at=0")
        self.assertEqual(n.deliver_once(), 0)  # 302 is not followed and not delivered
        self.s.db.execute("UPDATE events SET next_at=0")
        self.assertEqual(n.deliver_once(), 1)
        self.assertIsNotNone(self.rows()[0][3])
        self.assertEqual(n.deliver_once(), 0)  # delivered rows are not sent again
        body, sig = Sink.got[-1]
        self.assertEqual(sig, events.sign(KEY, body))
        doc = json.loads(body)
        self.assertEqual(doc["schema_version"], "edu.math.event/1")
        self.assertEqual((doc["job_id"], doc["request_id"], doc["status"], doc["seq"]), (j["job_id"], "emj_2", "queued", 1))
        self.assertTrue(doc["event_id"].startswith("me_"))
        self.assertEqual(len(Sink.got), 3)

    def test_rejects_non_http_url(self):
        with self.assertRaises(SystemExit):
            events.Notifier(self.s, "file:///etc/passwd", KEY)


if __name__ == "__main__":
    unittest.main()
