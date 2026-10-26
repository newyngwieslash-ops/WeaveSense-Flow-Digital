# -*- coding: utf-8 -*-
"""Durable job store and worker (SQLite).

* logical idempotency key = (tenant, kind, idempotency_key); same input returns the
  original job, a different input hash returns REVISION_CONFLICT;
* every stage result is checkpointed; after a restart a running job resumes at
  the first stage without a checkpoint (no recomputation of finished stages);
* cancel is cooperative between stages; partial artifacts stay listed;
* each stage runs in a fresh subprocess with CPU/memory limits, an empty
  environment (no platform secrets) and a wall-clock timeout. A timeout is
  reported as TIMEOUT_UNDETERMINED, never as "no solution".
"""

import hashlib
import json
import os
import resource
import sqlite3
import subprocess
import sys
import threading
import time
import uuid

from . import catalog
from .errors import MathError
from .pipeline import ANALYZE_STAGES, EDIT_STAGES, LESSON_STAGES, VARIANT_STAGES

SCHEMA_VERSION = "edu.math.job/1"
TERMINAL = ("succeeded", "failed", "cancelled")
STAGES = {"analyze": ANALYZE_STAGES, "lesson_build": LESSON_STAGES, "variant_set": VARIANT_STAGES, "lesson_edit": EDIT_STAGES}
TIMEOUTS = {"validate_input": 5, "solve": catalog.LIMITS["solve_timeout_s"], "verify": catalog.LIMITS["verify_timeout_s"],
            "compose_lesson": 10, "project_student": 5, "render_preview": catalog.LIMITS["render_timeout_s"],
            "generate_variants": 5, "verify_variants": 30, "apply_edits": 10}


def now():
    return time.time()


def sha256_text(s):
    return "sha256:" + hashlib.sha256(s.encode("utf-8")).hexdigest()


def canon(obj):
    return json.dumps(obj, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


class Store:
    def __init__(self, data_dir):
        self.dir = data_dir
        os.makedirs(os.path.join(data_dir, "artifacts"), exist_ok=True)
        self.lock = threading.RLock()
        self.db = sqlite3.connect(os.path.join(data_dir, "jobs.db"), check_same_thread=False, isolation_level=None)
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.executescript("""
        CREATE TABLE IF NOT EXISTS jobs(
          job_id TEXT PRIMARY KEY, operation_id TEXT, tenant_id TEXT NOT NULL, kind TEXT NOT NULL,
          idem_key TEXT NOT NULL, input_hash TEXT NOT NULL, input_json TEXT NOT NULL,
          request_id TEXT, trace_id TEXT, status TEXT NOT NULL, stage TEXT, seq INTEGER NOT NULL DEFAULT 0,
          stages_json TEXT NOT NULL, checkpoint_json TEXT NOT NULL, result_refs_json TEXT NOT NULL DEFAULT '[]',
          usage_json TEXT NOT NULL DEFAULT '[]', warnings_json TEXT NOT NULL DEFAULT '[]', error_json TEXT,
          cancel_requested INTEGER NOT NULL DEFAULT 0, created_at REAL, updated_at REAL,
          UNIQUE(tenant_id, kind, idem_key));
        CREATE TABLE IF NOT EXISTS artifacts(
          artifact_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, job_id TEXT NOT NULL, kind TEXT NOT NULL,
          audience TEXT NOT NULL, media_type TEXT NOT NULL, sha256 TEXT NOT NULL, size INTEGER NOT NULL,
          path TEXT NOT NULL, created_at REAL);
        """)

    # ---- jobs ----
    def start(self, tenant, kind, idem_key, payload, request_id="", trace_id=""):
        if kind not in STAGES:
            raise MathError("UNSUPPORTED_FAMILY", "unknown job kind")
        if not idem_key or len(idem_key) > 128:
            raise MathError("INPUT_AMBIGUOUS", "idempotency_key is required (<=128 chars)")
        raw = canon(payload)
        h = sha256_text(raw)
        with self.lock:
            row = self.db.execute("SELECT job_id, input_hash FROM jobs WHERE tenant_id=? AND kind=? AND idem_key=?",
                                  (tenant, kind, idem_key)).fetchone()
            if row:
                if row[1] != h:
                    raise MathError("REVISION_CONFLICT", "idempotency key reused with a different input")
                return self.get(tenant, row[0]), False
            jid = "mj_" + uuid.uuid4().hex
            stages = [{"name": s, "status": "pending"} for s in STAGES[kind]]
            t = now()
            self.db.execute("INSERT INTO jobs(job_id, operation_id, tenant_id, kind, idem_key, input_hash, input_json, request_id, trace_id,"
                            " status, stage, seq, stages_json, checkpoint_json, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
                            (jid, "mop_" + uuid.uuid4().hex, tenant, kind, idem_key, h, raw, request_id, trace_id,
                             "queued", STAGES[kind][0], 1, json.dumps(stages), "{}", t, t))
            return self.get(tenant, jid), True

    def find_by_key(self, tenant, kind, idem_key):
        with self.lock:
            row = self.db.execute("SELECT job_id FROM jobs WHERE tenant_id=? AND kind=? AND idem_key=?", (tenant, kind, idem_key)).fetchone()
        return self.get(tenant, row[0]) if row else None

    def _row(self, jid):
        cur = self.db.execute("SELECT * FROM jobs WHERE job_id=?", (jid,))
        r = cur.fetchone()
        if not r:
            return None
        return dict(zip([d[0] for d in cur.description], r))

    def get(self, tenant, jid, internal=False):
        with self.lock:
            r = self._row(jid)
        if not r or r["tenant_id"] != tenant:
            raise MathError("POLICY_REVOKED", "job not found for this tenant")
        stages = json.loads(r["stages_json"])
        done = sum(1 for s in stages if s["status"] in ("succeeded", "skipped"))
        view = {"schema_version": SCHEMA_VERSION, "request_id": r["request_id"], "trace_id": r["trace_id"], "job_id": r["job_id"],
                "operation_id": r["operation_id"], "kind": r["kind"], "status": r["status"], "stage": r["stage"],
                "seq": r["seq"], "progress": round(done / len(stages), 3), "stages": stages, "input_hash": r["input_hash"],
                "result_refs": json.loads(r["result_refs_json"]), "usage": json.loads(r["usage_json"]),
                "usage_state": "metered" if r["status"] in TERMINAL else "accruing",
                "warnings": json.loads(r["warnings_json"]), "error": json.loads(r["error_json"]) if r["error_json"] else None,
                "catalog_version": catalog.CATALOG_VERSION}
        if internal:
            view["_input"] = json.loads(r["input_json"])
            view["_checkpoint"] = json.loads(r["checkpoint_json"])
        return view

    def cancel(self, tenant, jid):
        with self.lock:
            v = self.get(tenant, jid)
            if v["status"] in TERMINAL:
                return v
            if v["status"] == "queued":
                self._update(jid, status="cancelled", cancel_requested=1)
            else:
                self._update(jid, cancel_requested=1)
        return self.get(tenant, jid)

    def _update(self, jid, **cols):
        cols["updated_at"] = now()
        sets = ", ".join("%s=?" % k for k in cols) + ", seq=seq+1"
        self.db.execute("UPDATE jobs SET %s WHERE job_id=?" % sets, list(cols.values()) + [jid])

    def runnable(self):
        with self.lock:
            return [r[0] for r in self.db.execute("SELECT job_id FROM jobs WHERE status IN ('queued','running') ORDER BY created_at").fetchall()]

    # ---- artifacts ----
    def put_artifact(self, tenant, jid, kind, audience, media_type, data):
        aid = "ma_" + uuid.uuid4().hex
        d = os.path.join(self.dir, "artifacts", tenant)
        os.makedirs(d, exist_ok=True)
        path = os.path.join(d, aid)
        with open(path, "wb") as f:
            f.write(data)
        digest = "sha256:" + hashlib.sha256(data).hexdigest()
        with self.lock:
            self.db.execute("INSERT INTO artifacts VALUES(?,?,?,?,?,?,?,?,?,?)", (aid, tenant, jid, kind, audience, media_type, digest, len(data), path, now()))
        return {"artifact_id": aid, "kind": kind, "audience": audience, "media_type": media_type, "sha256": digest, "size": len(data)}

    def get_artifact(self, tenant, aid):
        with self.lock:
            r = self.db.execute("SELECT tenant_id, kind, audience, media_type, sha256, size, path, job_id FROM artifacts WHERE artifact_id=?", (aid,)).fetchone()
        if not r or r[0] != tenant:
            raise MathError("POLICY_REVOKED", "artifact not found for this tenant")
        with open(r[6], "rb") as f:
            data = f.read()
        if "sha256:" + hashlib.sha256(data).hexdigest() != r[4]:
            raise MathError("INTERNAL", "artifact integrity check failed")
        return {"artifact_id": aid, "kind": r[1], "audience": r[2], "media_type": r[3], "sha256": r[4], "size": r[5], "job_id": r[7]}, data


def _limits():
    mem = catalog.LIMITS["worker_memory_mb"] * 1024 * 1024
    resource.setrlimit(resource.RLIMIT_AS, (mem, mem))
    resource.setrlimit(resource.RLIMIT_CPU, (60, 60))
    resource.setrlimit(resource.RLIMIT_NOFILE, (64, 64))
    os.setsid()


def isolated_stage(stage, payload, prior, timeout):
    """Run one stage in a fresh subprocess with an empty environment."""
    root = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
    env = {"PATH": "/usr/bin:/bin", "PYTHONHASHSEED": "0", "LANG": "C.UTF-8"}
    req = json.dumps({"payload": payload, "prior": prior}, ensure_ascii=False).encode("utf-8")
    t0 = time.monotonic()
    try:
        p = subprocess.run([sys.executable, "-E", "-s", "-m", "mathsvc.runner", stage], input=req, capture_output=True,
                           timeout=timeout, env=env, preexec_fn=_limits, cwd=root)
    except subprocess.TimeoutExpired:
        return {"ok": False, "error": MathError("TIMEOUT_UNDETERMINED", "stage %s exceeded %ss; result undetermined (not 'no solution')" % (stage, timeout)).to_dict()}, int((time.monotonic() - t0) * 1000)
    ms = int((time.monotonic() - t0) * 1000)
    try:
        return json.loads(p.stdout.decode("utf-8")), ms
    except ValueError:
        return {"ok": False, "error": MathError("INTERNAL", "stage %s produced no result (exit %s)" % (stage, p.returncode),
                                               {"stderr_tail": p.stderr.decode("utf-8", "replace")[-300:]}).to_dict()}, ms


class Worker:
    def __init__(self, store, stage_runner=isolated_stage):
        self.store = store
        self.run_stage = stage_runner
        self.stop = threading.Event()
        self.wake = threading.Event()

    def loop(self):
        while not self.stop.is_set():
            ids = self.store.runnable()
            for jid in ids:
                if self.stop.is_set():
                    break
                self.step(jid)
            if not ids:
                self.wake.wait(0.5)
                self.wake.clear()

    def step(self, jid):
        """Advance one job by one stage (idempotent at stage granularity)."""
        s = self.store
        with s.lock:
            r = s._row(jid)
        if not r or r["status"] in TERMINAL:
            return
        if r["cancel_requested"]:
            with s.lock:
                s._update(jid, status="cancelled")
            return
        stages = json.loads(r["stages_json"])
        ckpt = json.loads(r["checkpoint_json"])
        nxt = next((x for x in stages if x["status"] not in ("succeeded", "skipped")), None)
        if nxt is None:
            with s.lock:
                s._update(jid, status="succeeded")
            return
        nxt["status"] = "running"
        with s.lock:
            s._update(jid, status="running", stage=nxt["name"], stages_json=json.dumps(stages))
        payload = json.loads(r["input_json"])
        if r["kind"] in ("lesson_build", "lesson_edit"):
            try:
                payload["analysis"] = self.analysis_of(r["tenant_id"], payload.get("analysis_job_id", ""))
                if r["kind"] == "lesson_edit":
                    payload["base_lesson"] = self.base_lesson_of(r["tenant_id"], payload.get("base_lesson_artifact_id", ""), payload.get("base_lesson_hash", ""))
            except MathError as e:
                with s.lock:
                    s._update(jid, status="failed", error_json=json.dumps(e.to_dict()))
                return
        out, ms = self.run_stage(nxt["name"], payload, ckpt, TIMEOUTS.get(nxt["name"], 10))
        usage = json.loads(r["usage_json"])
        usage.append({"stage": nxt["name"], "unit": "compute_ms", "quantity": ms})
        if not out.get("ok"):
            nxt["status"] = "failed"
            nxt["error"] = out["error"]["code"]
            with s.lock:
                s._update(jid, status="failed", stages_json=json.dumps(stages), usage_json=json.dumps(usage), error_json=json.dumps(out["error"]))
            return
        res = out["result"]
        refs = json.loads(r["result_refs_json"])
        for a in self._artifacts(r, nxt["name"], res):
            refs.append(a)
        ckpt[nxt["name"]] = res
        nxt["status"] = "succeeded"
        nxt["compute_ms"] = ms
        status = "running"
        if all(x["status"] == "succeeded" for x in stages):
            status = "succeeded"
        with s.lock:
            cur = s._row(jid)
            if cur["cancel_requested"] and status != "succeeded":
                status = "cancelled"
            s._update(jid, status=status, stages_json=json.dumps(stages), checkpoint_json=json.dumps(ckpt, ensure_ascii=False),
                      result_refs_json=json.dumps(refs), usage_json=json.dumps(usage))

    def analysis_of(self, tenant, ajid):
        a = self.store.get(tenant, ajid, internal=True)
        if a["kind"] != "analyze" or a["status"] != "succeeded":
            raise MathError("REVISION_CONFLICT", "analysis job is not a succeeded analysis")
        ck = a["_checkpoint"]
        return {"solution": ck["solve"]["solution"], "report": ck["verify"]["report"], "input_hash": a["input_hash"]}

    def base_lesson_of(self, tenant, aid, sha):
        """The exact teacher lesson an edit starts from (same tenant, kind lesson, hash bound)."""
        meta, data = self.store.get_artifact(tenant, aid)
        if meta["kind"] != "lesson" or not sha or meta["sha256"] != sha:
            raise MathError("REVISION_CONFLICT", "base lesson does not match the requested revision hash")
        return json.loads(data)

    def _artifacts(self, r, stage, res):
        t, j = r["tenant_id"], r["job_id"]
        js = lambda o: json.dumps(o, ensure_ascii=False, sort_keys=True).encode("utf-8")  # noqa: E731
        put = self.store.put_artifact
        if stage == "solve":
            return [put(t, j, "solution", "teacher", "application/json", js(res["solution"]))]
        if stage == "verify":
            return [put(t, j, "validation_report", "teacher", "application/json", js(res["report"]))]
        if stage in ("compose_lesson", "apply_edits"):
            return [put(t, j, "lesson", "teacher", "application/json", js(res["lesson"]))]
        if stage == "project_student":
            return [put(t, j, "lesson_student", "student", "application/json", js(res["student"]))]
        if stage == "render_preview":
            return [put(t, j, "preview_html", "teacher", "text/html; charset=utf-8", res["teacher_html"].encode("utf-8")),
                    put(t, j, "preview_html_student", "student", "text/html; charset=utf-8", res["student_html"].encode("utf-8"))]
        if stage == "verify_variants":
            from .variants import student_items
            vs = res["variant_set"]
            return [put(t, j, "variant_set", "teacher", "application/json", js(vs)),
                    put(t, j, "variant_set_student", "student", "application/json", js({"items": student_items(vs)}))]
        return []
