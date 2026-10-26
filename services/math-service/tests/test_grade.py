# -*- coding: utf-8 -*-
import unittest

from mathsvc.grade import grade
from mathsvc.jobs import isolated_stage


def g(exact, resp, plain=None, **kw):
    return grade(dict({"expected": {"exact": exact, "plain": plain or exact}, "response": resp}, **kw))


class GradeTest(unittest.TestCase):
    def test_exact_equivalence(self):
        for r in ["2*sqrt(22)/11", "2√22/11", "sqrt(88)/11", "(2/11)*sqrt(22)", "4/sqrt(22)", "4／√22"]:
            self.assertEqual(g("2*sqrt(22)/11", r)["result"], "correct", r)
        self.assertEqual(g("2*sqrt(22)/11", "sqrt(22)/11")["result"], "incorrect")
        self.assertEqual(g("-3/4", "－3/4")["result"], "correct")
        self.assertEqual(g("-3/4", "3/4")["result"], "incorrect")

    def test_blank_and_free_text(self):
        self.assertEqual(g("1", "  ")["result"], "invalid")
        self.assertEqual(g("1", "我觉得是三分之一")["result"], "needs_teacher")
        self.assertEqual(g("1", "x^2+")["result"], "needs_teacher")
        self.assertEqual(g("1", "__import__('os')")["result"], "needs_teacher")

    def test_decimal_policy(self):
        self.assertEqual(g("sqrt(3)/3", "0.577")["result"], "needs_exact")
        self.assertEqual(g("sqrt(3)/3", "0.577", approx={"allowed": True, "rel_tol": 1e-3})["result"], "correct_approx")
        self.assertEqual(g("sqrt(3)/3", "0.6", approx={"allowed": True, "rel_tol": 1e-3})["result"], "incorrect")

    def test_units(self):
        e = {"exact": "30", "plain": "30 cm³"}
        for r, want in [("30 cm³", "correct"), ("30cm^3", "correct"), ("30 立方厘米", "correct"), ("30", "incorrect"),
                        ("30 mm³", "incorrect"), ("30000 mm³", "incorrect")]:
            self.assertEqual(grade({"expected": e, "response": r})["result"], want, r)
        self.assertEqual(grade({"expected": e, "response": "30", "unit_policy": "optional"})["result"], "correct")
        self.assertEqual(g("sqrt(3)/3", "sqrt(3)/3 cm³")["result"], "incorrect")

    def test_interval(self):
        self.assertEqual(g("[3, 4]", "[3,4]")["result"], "correct")
        self.assertEqual(g("[sqrt(2), 2*sqrt(2)]", "[√2, √8]")["result"], "correct")
        self.assertEqual(g("[3, 4]", "[3, 5]")["result"], "incorrect")
        self.assertEqual(g("[3, 4]", "3<=L<=4")["result"], "needs_teacher")

    def test_no_expected_leak(self):
        out = g("2*sqrt(22)/11", "1")
        self.assertNotIn("22", repr(out))

    def test_isolated_runner(self):
        out, _ = isolated_stage("grade", {"expected": {"exact": "-3/4"}, "response": "-0.75"}, {}, 20)
        self.assertTrue(out["ok"], out)
        self.assertEqual(out["result"]["result"], "needs_exact")


if __name__ == "__main__":
    unittest.main()
