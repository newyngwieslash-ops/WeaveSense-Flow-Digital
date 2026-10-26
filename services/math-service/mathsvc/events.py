# -*- coding: utf-8 -*-
"""Job event delivery to the platform (DEV-056, DEV-057).

Every job status write records an outbox row in the same SQLite transaction
(jobs.Store._update). This notifier delivers pending rows to MATH_EVENT_URL at
least once, oldest first, signed with the service key:

    X-Math-Event-Signature: hex(HMAC-SHA256(key, "event\\n" + body))

Events are hints only. The platform deduplicates by event_id, ignores stale
sequence numbers and always re-reads the job (GET /v1/jobs/{id}) before acting,
so a lost, duplicated, delayed or reordered event never changes the outcome.
Failures back off exponentially (at most 60 s). Redirects are not followed.
"""

import hashlib
import hmac
import json
import threading
import time
import urllib.error
import urllib.request

EVENT_SCHEMA = "edu.math.event/1"


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


def sign(key, body):
    return hmac.new(key, b"event\n" + body, hashlib.sha256).hexdigest()


def body_of(ev, sent_at=None):
    doc = {"schema_version": EVENT_SCHEMA, "event_id": ev["event_id"], "tenant_id": ev["tenant_id"], "job_id": ev["job_id"],
           "request_id": ev["request_id"] or "", "seq": ev["seq"], "status": ev["status"], "sent_at": int(sent_at or time.time())}
    return json.dumps(doc, sort_keys=True, separators=(",", ":")).encode("utf-8")


class Notifier:
    def __init__(self, store, url, key, timeout=5):
        if not (url.startswith("http://") or url.startswith("https://")):
            raise SystemExit("MATH_EVENT_URL must be an http(s) URL")
        self.store, self.url, self.key, self.timeout = store, url, key, timeout
        self.stop = threading.Event()
        self.opener = urllib.request.build_opener(_NoRedirect)

    def post(self, body):
        req = urllib.request.Request(self.url, data=body, method="POST", headers={
            "Content-Type": "application/json", "X-Math-Event-Signature": sign(self.key, body)})
        try:
            with self.opener.open(req, timeout=self.timeout) as r:
                return 200 <= r.status < 300
        except (urllib.error.URLError, OSError, ValueError):
            return False

    def deliver_once(self):
        n = 0
        for ev in self.store.pending_events():
            ok = self.post(body_of(ev))
            self.store.mark_event(ev["event_id"], ok, ev["attempts"] + 1)
            n += 1 if ok else 0
        return n

    def loop(self, every=0.2):
        while not self.stop.is_set():
            try:
                self.deliver_once()
            except Exception:  # noqa: BLE001 - keep delivering; rows stay pending
                pass
            self.stop.wait(every)
