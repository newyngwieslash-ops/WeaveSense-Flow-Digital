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


class VariantSetJobTests(unittest.TestCase):
    """FR-M07 / AC-22: each variant independently checked; answer keys stay teacher-audience."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.s = Store(self.dir)
        self.w = Worker(self.s)

    def run_set(self, problem, count=3):
        j, _ = self.s.start("t1", "variant_set", "vk", {"problem": problem, "count": count, "problem_hash": "ph"})
        drain(self.w, j["job_id"])
        return self.s.get("t1", j["job_id"], internal=True)

    def test_variant_set_items_verified_distinct_and_student_artifact_has_no_keys(self):
        import json as _json
        v = self.run_set(PROB["problem"])
        self.assertEqual(v["status"], "succeeded")
        vs = v["_checkpoint"]["verify_variants"]["variant_set"]
        # sin depends only on h/a: (4,2) repeats the parent ratio and (1,1) repeats (2,2); fewer items, never padded.
        self.assertEqual((vs["requested"], len(vs["items"])), (3, 2))
        self.assertTrue(any(d["reason"] == "answer_reused" for d in vs["dropped"]))
        answers = [it["answer_key"]["exact"] for it in vs["items"]]
        self.assertEqual(len(set(answers)), 2)
        self.assertNotIn(vs["parent"]["answer_exact"], answers)
        for it in vs["items"]:
            self.assertEqual(it["math_state"], "verified")
            self.assertEqual((it["change_type"], it["transfer"]), ("numbers_only", "near"))
            self.assertNotEqual(it["params"], {"a": "2", "h": "1"})
        kinds = {r["kind"]: r for r in v["result_refs"]}
        self.assertEqual(kinds["variant_set"]["audience"], "teacher")
        self.assertEqual(kinds["variant_set_student"]["audience"], "student")
        _, data = self.s.get_artifact("t1", kinds["variant_set_student"]["artifact_id"])
        stu = _json.loads(data)
        self.assertNotIn("answer_key", data.decode("utf-8"))
        self.assertEqual([x["id"] for x in stu["items"]], ["v1", "v2"])

    def test_variant_reusing_parent_answer_is_dropped_with_reason(self):
        v = self.run_set({"family_id": "SOLID-VOL-01", "params": {"l": "3", "w": "4", "h": "5"}, "unit": "cm"}, count=4)
        vs = v["_checkpoint"]["verify_variants"]["variant_set"]
        self.assertTrue(any(d["reason"] == "answer_reused" for d in vs["dropped"]))
        self.assertNotIn("60", [it["answer_key"]["exact"] for it in vs["items"]])

    def test_family_with_constant_answer_fails_instead_of_faking_variants(self):
        v = self.run_set({"family_id": "SOLID-ANGLE-01", "params": {"e": "1"}})
        self.assertEqual(v["status"], "failed")
        self.assertEqual(v["error"]["code"], "VALIDATION_FAILED")


class LessonEditJobTests(unittest.TestCase):
    """FR-T05/FR-T06, AC-17/18: field level edits on an exact base hash; math kept, stale parts regenerated."""

    def setUp(self):
        self.dir = tempfile.mkdtemp()
        self.s = Store(self.dir)
        self.w = Worker(self.s)
        a, _ = self.s.start("t1", "analyze", "a", PROB)
        drain(self.w, a["job_id"])
        lb, _ = self.s.start("t1", "lesson_build", "l", dict(PROB, analysis_job_id=a["job_id"]))
        drain(self.w, lb["job_id"])
        self.aj = a["job_id"]
        v = self.s.get("t1", lb["job_id"])
        self.lesson_ref = {r["kind"]: r for r in v["result_refs"]}["lesson"]
        import json as _json
        _, data = self.s.get_artifact("t1", self.lesson_ref["artifact_id"])
        self.base = _json.loads(data)

    def edit(self, edits, key="e", sha=None, tenant="t1"):
        p = dict(PROB, analysis_job_id=self.aj, base_lesson_artifact_id=self.lesson_ref["artifact_id"],
                 base_lesson_hash=sha or self.lesson_ref["sha256"], edits=edits)
        j, _ = self.s.start(tenant, "lesson_edit", key, p)
        drain(self.w, j["job_id"])
        return self.s.get(tenant, j["job_id"], internal=True)

    def test_wording_and_board_edit_creates_new_lesson_with_log(self):
        import json as _json
        b0 = self.base["beats"][0]["id"]
        v = self.edit([{"target": "beat", "id": b0, "field": "say", "value": "先看底面。"},
                       {"target": "beat", "id": b0, "field": "board", "value": ["连接 AC"]},
                       {"target": "pronunciation", "term": "棱", "reading": "léng"}])
        self.assertEqual(v["status"], "succeeded", v.get("error"))
        lesson = v["_checkpoint"]["apply_edits"]["lesson"]
        self.assertEqual(lesson["beats"][0]["say"], "先看底面。")
        self.assertEqual(lesson["quality"]["edited_unverified"], [b0])
        self.assertEqual(lesson["quality"]["math_text"], "teacher_edited_not_auto_verified")
        self.assertEqual(lesson["meta"]["parent_lesson_hash"], self.lesson_ref["sha256"])
        self.assertEqual(lesson["answer"], self.base["answer"])
        self.assertEqual(lesson["pronunciations"], {"棱": "léng"})
        imp = v["_checkpoint"]["apply_edits"]["impact"]
        self.assertEqual(imp["classes"], ["pronunciation", "reasoning", "wording"])
        self.assertTrue(imp["manual_check"])
        kinds = {r["kind"]: r for r in v["result_refs"]}
        self.assertEqual(set(kinds), {"lesson", "lesson_student", "preview_html", "preview_html_student"})
        _, html = self.s.get_artifact("t1", kinds["preview_html"]["artifact_id"])
        self.assertIn("连接 AC".encode(), html)  # the preview shows board lines; narration feeds captions and media
        self.assertNotEqual(kinds["lesson"]["sha256"], self.lesson_ref["sha256"])
        _, sd = self.s.get_artifact("t1", kinds["lesson_student"]["artifact_id"])
        self.assertNotIn("连接 AC", sd.decode("utf-8"))  # beats stay locked for students
        self.assertIsInstance(_json.loads(sd), dict)

    def test_hint_leaking_the_answer_is_refused(self):
        lv = next(h["level"] for h in self.base["hints"] if not h.get("gated"))
        v = self.edit([{"target": "hint", "level": lv, "field": "text", "value": "答案是 " + self.base["answer"]["plain"]}], key="leak")
        self.assertEqual(v["status"], "failed")
        self.assertEqual(v["error"]["code"], "VALIDATION_FAILED")

    def test_non_editable_targets_stale_hash_and_other_tenant_are_refused(self):
        v = self.edit([{"target": "answer", "field": "plain", "value": "1"}], key="ans")
        self.assertEqual((v["status"], v["error"]["code"]), ("failed", "POLICY_REVOKED"))
        v = self.edit([{"target": "beat", "id": self.base["beats"][0]["id"], "field": "say", "value": "x"}], key="stale", sha="sha256:old")
        self.assertEqual((v["status"], v["error"]["code"]), ("failed", "REVISION_CONFLICT"))
        v = self.edit([{"target": "beat", "id": self.base["beats"][0]["id"], "field": "say", "value": "x"}], key="t2", tenant="t2")
        self.assertEqual(v["status"], "failed")

    def test_impact_classes(self):
        from mathsvc import edits
        self.assertEqual(edits.impact([{"target": "pronunciation", "term": "棱", "reading": "léng"}])["invalidates"], ["captions", "media"])
        self.assertIn("review", edits.impact([{"target": "beat", "id": "s1", "field": "title", "value": "x"}])["invalidates"])
        with self.assertRaises(MathError):
            edits.impact([])
