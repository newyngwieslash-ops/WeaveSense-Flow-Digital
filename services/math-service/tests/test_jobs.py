# -*- coding: utf-8 -*-
import os
import subprocess
import tempfile
import unittest
from unittest import mock

from mathsvc import jobs
from mathsvc.errors import MathError
from mathsvc.jobs import Store, Worker

PROB = {"problem": {"family_id": "SOLID-ANGLE-02", "params": {"a": "2", "h": "1"}, "unit": "cm"}}


def drain(w, jid, n=8):
    for _ in range(n):
        w.step(jid)


class JobTests(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.s = Store(self.dir)
        self.w = Worker(self.s)

    def test_idempotency_same_and_conflict(self):
        j1, c1 = self.s.start("t1", "analyze", "k", PROB)
        j2, c2 = self.s.start("t1", "analyze", "k", PROB)
        self.assertTrue(c1)
        self.assertFalse(c2)
        self.assertEqual(j1["job_id"], j2["job_id"])
        other = {"problem": dict(PROB["problem"], params={"a": "3", "h": "1"})}
        with self.assertRaises(MathError) as cm:
            self.s.start("t1", "analyze", "k", other)
        self.assertEqual(cm.exception.code, "REVISION_CONFLICT")
        j3, c3 = self.s.start("t2", "analyze", "k", PROB)  # other tenant: independent
        self.assertTrue(c3)
        with self.assertRaises(MathError):
            self.s.get("t2", j1["job_id"])  # tenant isolation

    def test_restart_resumes_from_checkpoint(self):
        j, _ = self.s.start("t1", "analyze", "k", PROB)
        self.w.step(j["job_id"])  # validate_input only
        calls = []
        real = jobs.isolated_stage

        def spy(stage, payload, prior, timeout):
            calls.append(stage)
            return real(stage, payload, prior, timeout)
        s2 = Store(self.dir)  # simulated process restart
        w2 = Worker(s2, stage_runner=spy)
        self.assertIn(j["job_id"], s2.runnable())
        drain(w2, j["job_id"])
        self.assertEqual(calls, ["solve", "verify"])
        v = s2.get("t1", j["job_id"])
        self.assertEqual(v["status"], "succeeded")
        self.assertEqual([r["kind"] for r in v["result_refs"]], ["solution", "validation_report"])

    def test_cancel_between_stages_keeps_partial_artifacts(self):
        j, _ = self.s.start("t1", "analyze", "k", PROB)
        self.w.step(j["job_id"])
        self.w.step(j["job_id"])
        self.s.cancel("t1", j["job_id"])
        drain(self.w, j["job_id"])
        v = self.s.get("t1", j["job_id"])
        self.assertEqual(v["status"], "cancelled")
        self.assertEqual([r["kind"] for r in v["result_refs"]], ["solution"])

    def test_timeout_is_undetermined_not_no_solution(self):
        out, _ = jobs.isolated_stage("solve", PROB, {}, 0.001)
        self.assertFalse(out["ok"])
        self.assertEqual(out["error"]["code"], "TIMEOUT_UNDETERMINED")

    def test_stage_subprocess_gets_no_platform_secrets(self):
        seen = {}
        real_run = subprocess.run

        def fake_run(*a, **kw):
            seen.update(kw.get("env") or {})
            return real_run(*a, **kw)
        with mock.patch.dict(os.environ, {"MATH_SERVICE_KEY": "x" * 40, "DATABASE_URL": "postgres://secret"}):
            with mock.patch("subprocess.run", side_effect=fake_run):
                out, _ = jobs.isolated_stage("validate_input", PROB, {}, 20)
        self.assertTrue(out["ok"], out)
        self.assertEqual(set(seen), {"PATH", "PYTHONHASHSEED", "LANG"})

    def test_lesson_build_requires_succeeded_analysis_of_same_tenant(self):
        j, _ = self.s.start("t1", "analyze", "k", PROB)
        drain(self.w, j["job_id"])
        lb, _ = self.s.start("t2", "lesson_build", "k2", dict(PROB, analysis_job_id=j["job_id"]))
        drain(self.w, lb["job_id"])
        self.assertEqual(self.s.get("t2", lb["job_id"])["status"], "failed")
        lb, _ = self.s.start("t1", "lesson_build", "k2", dict(PROB, analysis_job_id=j["job_id"]))
        drain(self.w, lb["job_id"])
        v = self.s.get("t1", lb["job_id"])
        self.assertEqual(v["status"], "succeeded", v["error"])
        kinds = {r["kind"]: r for r in v["result_refs"]}
        self.assertEqual(kinds["preview_html_student"]["audience"], "student")
        _, html = self.s.get_artifact("t1", kinds["preview_html_student"]["artifact_id"])
        self.assertNotIn("2√22/11".encode(), html)
        self.assertNotIn(b"<script", html)
        self.assertNotIn(b"http://cdn", html)
        self.assertNotIn(b"file://", html)
        _, th = self.s.get_artifact("t1", kinds["preview_html"]["artifact_id"])
        self.assertIn("2√22/11".encode(), th)
        with self.assertRaises(MathError):
            self.s.get_artifact("t2", kinds["preview_html"]["artifact_id"])


if __name__ == "__main__":
    unittest.main()
