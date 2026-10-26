# -*- coding: utf-8 -*-
"""The five additional P0 families: gold answers, layered evidence, exclusions,
self-test independence and the full stage pipeline with student leak guards."""

import unittest

import sympy as sp

from mathsvc import lesson as L
from mathsvc import pipeline
from mathsvc.errors import MathError
from mathsvc.families import REGISTRY


def prob(fid, params, **kw):
    d = {"family_id": fid, "params": params}
    d.update(kw)
    return d


GOLD = {
    "SOLID-VOL-01": [
        (prob("SOLID-VOL-01", {"l": "3", "w": "4", "h": "5"}, unit="cm"), "60"),
        (prob("SOLID-VOL-01", {"l": "3", "w": "20", "h": "5"}, units={"l": "cm", "w": "mm", "h": "cm"}, target_unit="cm"), "30"),
        (prob("SOLID-VOL-01", {"l": "1", "w": "1", "h": "1"}, unit="m", target_unit="cm"), "1000000"),
        (prob("SOLID-VOL-01", {"l": "1/2", "w": "sqrt(2)", "h": "sqrt(2)"}), "1"),
    ],
    "SOLID-ANGLE-01": [(prob("SOLID-ANGLE-01", {"e": e}), "sqrt(3)/3") for e in ("1", "2", "7/3", "sqrt(5)", "100")],
    "CONIC-CHORD-01": [
        (prob("CONIC-CHORD-01", {"a": "2", "b": "sqrt(3)"}), "[3, 4]"),
        (prob("CONIC-CHORD-01", {"a": "5", "b": "4"}), "[32/5, 10]"),
        (prob("CONIC-CHORD-01", {"a": "sqrt(2)", "b": "1"}), "[sqrt(2), 2*sqrt(2)]"),
    ],
    "CONIC-SLOPE-01": [
        (prob("CONIC-SLOPE-01", {"a": "2", "b": "sqrt(3)"}), "-3/4"),
        (prob("CONIC-SLOPE-01", {"a": "4", "b": "2"}), "-1/4"),
        (prob("CONIC-SLOPE-01", {"a": "3", "b": "1"}), "-1/9"),
    ],
    "CONIC-PARABOLA-01": [
        (prob("CONIC-PARABOLA-01", {"p": "2"}), "-3"),
        (prob("CONIC-PARABOLA-01", {"p": "1"}), "-3/4"),
        (prob("CONIC-PARABOLA-01", {"p": "sqrt(2)"}), "-3/2"),
    ],
}

REJECT = [
    (prob("SOLID-VOL-01", {"l": "3", "w": "4"}), "INPUT_AMBIGUOUS"),
    (prob("SOLID-VOL-01", {"l": "3", "w": "4", "h": "5"}, oblique=True), "UNSUPPORTED_FAMILY"),
    (prob("SOLID-VOL-01", {"l": "3", "w": "4", "h": "5"}, units={"l": "cm", "w": "mm", "h": "cm"}), "INPUT_AMBIGUOUS"),
    (prob("SOLID-VOL-01", {"l": "3", "w": "4", "h": "5"}, units={"l": "cm", "w": "", "h": "cm"}, target_unit="cm"), "CONDITION_CONFLICT"),
    (prob("SOLID-VOL-01", {"l": "3", "w": "4", "h": "5"}, unit="inch"), "PARAMETER_LIMIT"),
    (prob("SOLID-VOL-01", {"l": "0", "w": "4", "h": "5"}), "CONDITION_CONFLICT"),
    (prob("SOLID-ANGLE-01", {"e": "-1"}), "CONDITION_CONFLICT"),
    (prob("SOLID-ANGLE-01", {"e": "2"}, target="sin_angle_A1C_BB1C1C"), "UNSUPPORTED_FAMILY"),
    (prob("SOLID-ANGLE-01", {"e": "2"}, method_profile={"allowed": ["calculus"]}), "METHOD_MISMATCH"),
    (prob("CONIC-CHORD-01", {"a": "2", "b": "2"}), "CONDITION_CONFLICT"),
    (prob("CONIC-CHORD-01", {"a": "2", "b": "3"}), "CONDITION_CONFLICT"),
    (prob("CONIC-CHORD-01", {"a": "3", "b": "2"}, focus="left"), "UNSUPPORTED_FAMILY"),
    (prob("CONIC-SLOPE-01", {"a": "1", "b": "2"}), "CONDITION_CONFLICT"),
    (prob("CONIC-SLOPE-01", {"a": "3", "b": "2"}, input_state="pending_confirmation"), "INPUT_AMBIGUOUS"),
    (prob("CONIC-PARABOLA-01", {"p": "2"}, convention="y2=4px"), "UNSUPPORTED_FAMILY"),
    (prob("CONIC-PARABOLA-01", {"p": "__import__('os')"}), "PARAMETER_LIMIT"),
    (prob("CONIC-PARABOLA-01", {"p": "2", "q": "1"}), "CONDITION_CONFLICT"),
]


def run_all(problem):
    prior = {}
    for st in pipeline.ANALYZE_STAGES:
        prior[st] = pipeline.run_stage(st, {"problem": problem}, prior)
    analysis = {"solution": prior["solve"]["solution"], "report": prior["verify"]["report"]}
    lp = {}
    for st in pipeline.LESSON_STAGES:
        lp[st] = pipeline.run_stage(st, {"problem": problem, "analysis": analysis}, lp)
    return prior, lp


class FamilyGold(unittest.TestCase):
    def test_registry_has_six_families(self):
        self.assertEqual(sorted(REGISTRY), sorted(["SOLID-VOL-01", "SOLID-ANGLE-01", "SOLID-ANGLE-02", "CONIC-CHORD-01", "CONIC-SLOPE-01", "CONIC-PARABOLA-01"]))

    def test_gold_answers_verified_with_layered_evidence(self):
        for fid, cases in GOLD.items():
            mod = REGISTRY[fid]
            for p, want in cases:
                n = mod.normalize(p)
                s = mod.solve(n)
                self.assertEqual(s["answer"]["exact"], want, (fid, p))
                r = mod.verify(n, s)
                self.assertEqual(r["math_state"], "verified", (fid, p, r["failed_checks"]))
                for c in r["checks"]:
                    if c["kind"] == "sampling":
                        self.assertIn("not a proof", c["evidence"]["note"])
                        self.assertNotIn("symbolic", c["id"])

    def test_rejections(self):
        for p, code in REJECT:
            with self.assertRaises(MathError, msg=repr(p)) as cm:
                REGISTRY[p["family_id"]].normalize(p)
            self.assertEqual(cm.exception.code, code, p)

    def test_wrong_answer_is_caught_by_independent_verifier(self):
        mod = REGISTRY["CONIC-PARABOLA-01"]
        n = mod.normalize(prob("CONIC-PARABOLA-01", {"p": "2"}))
        s = mod.solve(n)
        s["_exact"]["dot"] = sp.Integer(-4)
        r = mod.verify(n, s)
        self.assertEqual(r["math_state"], "failed")
        mod = REGISTRY["CONIC-CHORD-01"]
        n = mod.normalize(prob("CONIC-CHORD-01", {"a": "2", "b": "sqrt(3)"}))
        s = mod.solve(n)
        s["_exact"]["lo"] = sp.Integer(2)
        self.assertEqual(mod.verify(n, s)["math_state"], "failed")

    def test_excluded_configurations_are_declared(self):
        s = REGISTRY["CONIC-SLOPE-01"].solve(REGISTRY["CONIC-SLOPE-01"].normalize(prob("CONIC-SLOPE-01", {"a": "2", "b": "1"})))
        self.assertEqual(len(s["excluded_points"]), 2)
        s = REGISTRY["CONIC-PARABOLA-01"].solve(REGISTRY["CONIC-PARABOLA-01"].normalize(prob("CONIC-PARABOLA-01", {"p": "2"})))
        self.assertEqual(s["excluded_lines"][0]["line"], "y=0")
        s = REGISTRY["CONIC-CHORD-01"].solve(REGISTRY["CONIC-CHORD-01"].normalize(prob("CONIC-CHORD-01", {"a": "2", "b": "1"})))
        self.assertEqual(s["configurations"]["min"]["chord"], "vertical")
        self.assertEqual(s["configurations"]["max"]["chord"], "horizontal")

    def test_self_test_differs_from_shown_problem(self):
        for fid, cases in GOLD.items():
            mod = REGISTRY[fid]
            for p, want in cases:
                n = mod.normalize(p)
                cq = mod.check_question(n, avoid=want)
                self.assertNotEqual(cq["question"]["params"], n["params"], fid)
                self.assertNotEqual(cq["answer_key"]["exact"], want, fid)


class FamilyPipeline(unittest.TestCase):
    def test_full_pipeline_and_student_projection(self):
        for fid, cases in GOLD.items():
            for p, want in cases[:2]:
                prior, lp = run_all(p)
                lesson = lp["compose_lesson"]["lesson"]
                self.assertEqual(lesson["answer"]["exact"], want)
                self.assertEqual(lesson["meta"]["family_id"], fid)
                stu = lp["project_student"]["student"]
                self.assertNotIn("answer", stu)
                self.assertNotIn("check_answer_key", stu)
                self.assertTrue(all(b.get("locked") for b in stu["beats"]))
                dumped = "\n".join(L._strings(stu))
                for m in L.answer_markers(lesson):
                    self.assertNotIn(m, dumped, (fid, m))
                vis = L.visible_text(lp["render_preview"]["student_html"])
                for m in L.answer_markers(lesson):
                    self.assertNotIn(m, vis, (fid, m))
                self.assertIn("答案", L.visible_text(lp["render_preview"]["teacher_html"]))
                self.assertNotIn("<script", lp["render_preview"]["student_html"])

    def test_explore_specs_stay_in_domain_and_leak_nothing(self):
        cases = [(fid, c[0][0]) for fid, c in GOLD.items()] + [("SOLID-ANGLE-02", prob("SOLID-ANGLE-02", {"a": "2", "h": "3"}))]
        for fid, p in cases:
            _, lp = run_all(p)
            lesson = lp["compose_lesson"]["lesson"]
            ex = lp["project_student"]["student"]["figure"].get("explore")
            self.assertIsNotNone(ex, fid)
            pred = ex["prediction"]
            self.assertTrue(pred["id"] and pred["prompt"] and len(pred["options"]) >= 2, fid)
            texts = "\n".join(L._strings(ex))
            for m in L.answer_markers(lesson):
                self.assertNotIn(m, texts, (fid, m))
            if ex["mode"] == "param":
                pr = ex["param"]
                self.assertTrue(ex["frames"], fid)
                self.assertTrue(any(f["v"] == pr["default"] and "points" in f for f in ex["frames"]), fid)
                for f in ex["frames"]:
                    self.assertTrue(pr["min"] <= f["v"] <= pr["max"], fid)
                    if "degenerate" in f:
                        self.assertNotIn("points", f)
                        self.assertTrue(f["degenerate"])
                    else:
                        self.assertTrue(all(abs(x) < 1e6 and abs(y) < 1e6 for x, y in f["points"].values()), fid)
                if fid == "CONIC-SLOPE-01":
                    self.assertEqual(sorted(f["v"] for f in ex["frames"] if "degenerate" in f), [0.0, 180.0])
            else:
                self.assertEqual(ex["mode"], "rotate")
                self.assertIn("yaw", ex["reset"])

    def test_plane_figure_renders_disabled_points(self):
        _, lp = run_all(prob("CONIC-SLOPE-01", {"a": "2", "b": "1"}))
        self.assertIn('aria-label="平面图形"', lp["render_preview"]["teacher_html"])
        self.assertIn("B 与 P 重合", lp["render_preview"]["teacher_html"])


class GuardHelpers(unittest.TestCase):
    def test_visible_text_ignores_geometry_but_keeps_labels(self):
        page = '<style>a{width:60px}</style><p>体积</p><svg><line x1="160.0"/><text x="1">A1</text></svg>'
        v = L.visible_text(page)
        self.assertNotIn("60", v)
        self.assertIn("A1", v)

    def test_marker_in_public_givens_is_not_used(self):
        les = {"answer": {"plain": "3", "latex": "3", "exact": "[3, 4]"}, "check_answer_key": {"plain": "60", "exact": "60", "latex": "60"},
               "problem": {"text": "x²/4 + y²/3 = 1", "params": {"a": "2"}}, "check_question": {"prompt": "长 60", "params": {}}}
        self.assertEqual(L.answer_markers(les), ["[3, 4]"])


if __name__ == "__main__":
    unittest.main()
