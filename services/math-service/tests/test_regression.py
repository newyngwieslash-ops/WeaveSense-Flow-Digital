# -*- coding: utf-8 -*-
"""Engineering regression set: at least 25 positive cases per family and 30+
rejection cases, with expected answers from hand-derived closed forms that do
not call the solvers. This is NOT the frozen admission set of FR-Q01 (which
must be written and reviewed by independent teachers); it only guards the
implementation. Run `python -m tests.test_regression report` for a summary."""

import itertools
import json
import sys
import unittest

import sympy as sp

from mathsvc.errors import MathError
from mathsvc.families import REGISTRY
from mathsvc.params import parse_exact

VALUES = ["1", "2", "3", "5", "7", "1/2", "3/2", "sqrt(2)", "sqrt(3)", "2*sqrt(3)", "10", "100"]
TO_MM = {"mm": 1, "cm": 10, "dm": 100, "m": 1000}


def X(t):
    return parse_exact(t)


def positives():
    out = []
    units = ["mm", "cm", "dm", "m"]
    for i, (l, w, h) in enumerate(itertools.islice(itertools.product(VALUES[:6], VALUES[3:9], VALUES[:4]), 0, 144, 5)):
        u = [units[(i + j) % 4] for j in range(3)]
        tu = units[i % 4]
        want = X(l) * X(w) * X(h) * TO_MM[u[0]] * TO_MM[u[1]] * TO_MM[u[2]] / sp.Integer(TO_MM[tu]) ** 3
        out.append(({"family_id": "SOLID-VOL-01", "params": {"l": l, "w": w, "h": h}, "units": dict(zip("lwh", u)), "target_unit": tu}, want))
    for e in VALUES + ["4", "9/4", "sqrt(5)", "3*sqrt(2)", "6", "8", "12", "20", "50", "99", "1/3", "5/7", "sqrt(7)"]:
        out.append(({"family_id": "SOLID-ANGLE-01", "params": {"e": e}}, sp.sqrt(3) / 3))
    for a, h in itertools.islice(itertools.product(VALUES[:7], VALUES[:5]), 0, 35):
        out.append(({"family_id": "SOLID-ANGLE-02", "params": {"a": a, "h": h}}, 2 * X(a) / sp.sqrt(5 * X(a) ** 2 + 2 * X(h) ** 2)))
    ab = [(a, b) for a in VALUES for b in VALUES if (X(a) - X(b)).is_positive]
    for a, b in ab[:30]:
        out.append(({"family_id": "CONIC-CHORD-01", "params": {"a": a, "b": b}}, (2 * X(b) ** 2 / X(a), 2 * X(a))))
    for a, b in ab[5:35]:
        out.append(({"family_id": "CONIC-SLOPE-01", "params": {"a": a, "b": b}}, -X(b) ** 2 / X(a) ** 2))
    for p in VALUES + ["4", "6", "8", "9", "1/3", "5/2", "sqrt(5)", "3*sqrt(2)", "12", "20", "40", "64", "99"]:
        out.append(({"family_id": "CONIC-PARABOLA-01", "params": {"p": p}}, -3 * X(p) ** 2 / 4))
    return out


NEGATIVES = [
    ({"family_id": "SOLID-VOL-01", "params": {"l": "1", "w": "2"}}, "INPUT_AMBIGUOUS"),
    ({"family_id": "SOLID-VOL-01", "params": {"l": "1", "w": "2", "h": "1001"}}, "PARAMETER_LIMIT"),
    ({"family_id": "SOLID-VOL-01", "params": {"l": "1", "w": "2", "h": "3"}, "shape": "oblique_prism"}, "UNSUPPORTED_FAMILY"),
    ({"family_id": "SOLID-VOL-01", "params": {"l": "1", "w": "2", "h": "3"}, "units": {"l": "ft"}}, "PARAMETER_LIMIT"),
    ({"family_id": "SOLID-VOL-01", "params": {"l": "1", "w": "2", "h": "3.5"}}, "PARAMETER_LIMIT"),
    ({"family_id": "SOLID-VOL-01", "params": {"l": "1", "w": "-2", "h": "3"}}, "CONDITION_CONFLICT"),
    ({"family_id": "SOLID-ANGLE-01", "params": {"e": "0"}}, "CONDITION_CONFLICT"),
    ({"family_id": "SOLID-ANGLE-01", "params": {"e": "101"}}, "PARAMETER_LIMIT"),
    ({"family_id": "SOLID-ANGLE-01", "params": {"e": "2"}, "naming": {"base": ["E", "F", "G", "H"]}}, "UNSUPPORTED_FAMILY"),
    ({"family_id": "SOLID-ANGLE-01", "params": {"e": "2"}, "target": "angle_BD1_ABCD"}, "UNSUPPORTED_FAMILY"),
    ({"family_id": "SOLID-ANGLE-01", "params": {}}, "INPUT_AMBIGUOUS"),
    ({"family_id": "SOLID-ANGLE-02", "params": {"a": "2", "h": "1"}, "target": "sin_angle_BF_PAC"}, "UNSUPPORTED_FAMILY"),
    ({"family_id": "SOLID-ANGLE-02", "params": {"a": "2", "h": "0"}}, "CONDITION_CONFLICT"),
    ({"family_id": "SOLID-ANGLE-02", "params": {"a": "sqrt(sqrt(2))", "h": "1"}}, "PARAMETER_LIMIT"),
    ({"family_id": "SOLID-ANGLE-02", "params": {"a": "2", "h": "1"}, "input_state": "conflicting"}, "INPUT_AMBIGUOUS"),
    ({"family_id": "CONIC-CHORD-01", "params": {"a": "1", "b": "1"}}, "CONDITION_CONFLICT"),
    ({"family_id": "CONIC-CHORD-01", "params": {"a": "1", "b": "3"}}, "CONDITION_CONFLICT"),
    ({"family_id": "CONIC-CHORD-01", "params": {"a": "3", "b": "1"}, "focus": "left"}, "UNSUPPORTED_FAMILY"),
    ({"family_id": "CONIC-CHORD-01", "params": {"a": "3", "b": "1"}, "naming": {"focus": "F2"}}, "UNSUPPORTED_FAMILY"),
    ({"family_id": "CONIC-CHORD-01", "params": {"a": "3", "b": "1"}, "unit": "cm"}, "PARAMETER_LIMIT"),
    ({"family_id": "CONIC-CHORD-01", "params": {"a": "3"}}, "INPUT_AMBIGUOUS"),
    ({"family_id": "CONIC-SLOPE-01", "params": {"a": "2", "b": "2"}}, "CONDITION_CONFLICT"),
    ({"family_id": "CONIC-SLOPE-01", "params": {"a": "2", "b": "x"}}, "PARAMETER_LIMIT"),
    ({"family_id": "CONIC-SLOPE-01", "params": {"a": "2", "b": "1"}, "target": "slope_sum"}, "UNSUPPORTED_FAMILY"),
    ({"family_id": "CONIC-SLOPE-01", "params": {"a": "2", "b": "1"}, "method_profile": {"allowed": ["vieta"]}}, "METHOD_MISMATCH"),
    ({"family_id": "CONIC-PARABOLA-01", "params": {"p": "-1"}}, "CONDITION_CONFLICT"),
    ({"family_id": "CONIC-PARABOLA-01", "params": {"p": "2"}, "convention": "x2=2py"}, "UNSUPPORTED_FAMILY"),
    ({"family_id": "CONIC-PARABOLA-01", "params": {"p": "1e3"}}, "PARAMETER_LIMIT"),
    ({"family_id": "CONIC-PARABOLA-01", "params": {"p": "open('/etc/passwd')"}}, "PARAMETER_LIMIT"),
    ({"family_id": "CONIC-PARABOLA-01", "params": {"p": "2"}, "target": "dot_FA_FB"}, "UNSUPPORTED_FAMILY"),
    ({"family_id": "CONIC-PARABOLA-01", "params": {"p": "2"}, "method_profile": {"allowed": "vieta"}}, "METHOD_MISMATCH"),
]


def run_case(problem):
    mod = REGISTRY[problem["family_id"]]
    n = mod.normalize(problem)
    s = mod.solve(n)
    r = mod.verify(n, s)
    return s, r


def matches(sol, want):
    if isinstance(want, tuple):
        lo, hi = sol["_exact"]["lo"], sol["_exact"]["hi"]
        return sp.simplify(lo - want[0]) == 0 and sp.simplify(hi - want[1]) == 0
    got = list(sol["_exact"].values())[0]
    return sp.simplify(sp.radsimp(got - want)) == 0


def report():
    per = {}
    for p, want in positives():
        s, r = run_case(p)
        d = per.setdefault(p["family_id"], {"cases": 0, "correct": 0, "verified": 0})
        d["cases"] += 1
        d["correct"] += int(matches(s, want))
        d["verified"] += int(r["math_state"] == "verified")
    neg = sum(1 for p, code in NEGATIVES if _rejects(p) == code)
    return {"kind": "engineering_regression_not_admission", "positives": per, "negatives": {"cases": len(NEGATIVES), "expected_rejection": neg}}


def _rejects(p):
    try:
        REGISTRY[p["family_id"]].normalize(p)
    except MathError as e:
        return e.code
    return None


class Regression(unittest.TestCase):
    def test_at_least_25_per_family_and_all_correct(self):
        cases = positives()
        counts = {}
        for p, want in cases:
            counts[p["family_id"]] = counts.get(p["family_id"], 0) + 1
            s, r = run_case(p)
            self.assertTrue(matches(s, want), p)
            self.assertEqual(r["math_state"], "verified", (p, r["failed_checks"]))
        self.assertEqual(len(counts), 6)
        for fid, c in counts.items():
            self.assertGreaterEqual(c, 25, fid)

    def test_rejections(self):
        self.assertGreaterEqual(len(NEGATIVES), 30)
        for p, code in NEGATIVES:
            self.assertEqual(_rejects(p), code, p)


if __name__ == "__main__":
    if sys.argv[1:] == ["report"]:
        print(json.dumps(report(), ensure_ascii=False, indent=1))
    else:
        unittest.main()
