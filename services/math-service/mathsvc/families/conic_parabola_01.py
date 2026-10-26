# -*- coding: utf-8 -*-
"""CONIC-PARABOLA-01: parabola y² = 2px (p > 0, vertex O, focus F(p/2, 0));
a line through F meets the parabola at A and B; the constant OA · OB.

Textbook convention: y² = 2px, so the focus is (p/2, 0).
Primary (Vieta): every non-axis line through F is x = my + p/2, giving
  y² − 2pm·y − p² = 0, y1y2 = −p², x1x2 = (y1y2)²/(4p²) = p²/4,
  OA·OB = x1x2 + y1y2 = −3p²/4.
Excluded: the axis y = 0 (it meets the parabola only at O).
Independent verifiers: vertical chord by explicit coordinates, symbolic
identity over all m, discriminant positivity, numeric sampling with roots.
"""

import math
import random

import sympy as sp

from .. import fmt
from ..errors import MathError
from . import common as C

FAMILY_ID = "CONIC-PARABOLA-01"
TITLE = "抛物线焦点弦的向量数量积定值"
SOLVER_VERSION = "conic-parabola-01/1.0.0"
VERIFIER_VERSION = "conic-parabola-01-verify/1.0.0"
TARGET = "dot_OA_OB"
MAX_VALUE = 100
METHODS = ("vieta",)
STANDARD_NAMING = {"vertex": "O", "focus": "F", "points": ["A", "B"]}


def normalize(inp):
    C.header(inp, FAMILY_ID, TARGET)
    C.naming(inp, FAMILY_ID, STANDARD_NAMING)
    C.unit(inp, ("",))
    if inp.get("convention", "y2=2px") != "y2=2px":
        raise MathError("UNSUPPORTED_FAMILY", "only the y² = 2px convention is supported")
    vals = C.positive_params(inp.get("params"), ("p",), MAX_VALUE)
    method, usable = C.methods(inp, FAMILY_ID, METHODS)
    return {"family_id": FAMILY_ID, "target": TARGET, "unit": "", "naming": STANDARD_NAMING, "params": C.texts(vals),
            "convention": "y2=2px", "method": method, "methods_allowed": usable, "_exact": vals}


def vieta(p, m):
    y = sp.symbols("y")
    poly = sp.Poly(sp.expand((m * y + p / 2) * 2 * p - y ** 2), y)  # 2p·x − y² with x = my + p/2
    c2, c1, c0 = poly.all_coeffs()
    s, prod = -c1 / c2, c0 / c2
    x1x2 = prod ** 2 / (4 * p ** 2)
    return sp.simplify(x1x2 + prod), sp.simplify(c1 ** 2 - 4 * c2 * c0), prod


def solve(norm):
    p = norm["_exact"]["p"]
    m = sp.symbols("m", real=True)
    dot, disc, prod = vieta(p, m)
    val = sp.radsimp(-3 * p ** 2 / 4)
    if not C.is_zero(dot - val):
        raise MathError("VALIDATION_FAILED", "Vieta computation did not give a constant")
    steps = [
        {"id": "s1", "title": "设直线", "uses": ["F"],
         "plain": ["焦点 F(p/2, 0) = F(%s, 0)。过 F 的直线不能是 x 轴（只交于一点），设为 x = my + p/2。" % fmt.plain(p / 2)]},
        {"id": "s2", "title": "联立与韦达定理", "uses": ["A", "B"],
         "plain": ["代入 y² = 2px 得 y² − 2pm·y − p² = 0，判别式 4p²(m² + 1) > 0，有两个不同交点。",
                   "y1y2 = −p² = %s，x1x2 = (y1y2)²/(4p²) = p²/4 = %s。" % (fmt.plain(prod), fmt.plain(p ** 2 / 4))]},
        {"id": "s3", "title": "求数量积", "uses": ["dot"], "reveals_answer": True,
         "plain": ["OA·OB = x1x2 + y1y2 = −3p²/4 = %s，是定值。" % fmt.plain(val)],
         "latex": ["\\overrightarrow{OA}\\cdot\\overrightarrow{OB}=x_1x_2+y_1y_2=%s" % fmt.latex(val)]},
    ]
    return {"family_id": FAMILY_ID, "solver_version": SOLVER_VERSION, "solution_status": "unique", "method": norm["method"],
            "params": norm["params"], "unit": "", "answer": C.answer(val),
            "excluded_lines": [{"line": "y=0", "reason": "与抛物线只交于顶点 O"}],
            "derived": {"focus": "(%s, 0)" % C.exact_text(p / 2), "discriminant": sp.sstr(disc)}, "steps": steps, "_exact": {"dot": val}}


def verify(norm, sol, seed=20261026):
    p = norm["_exact"]["p"]
    val = sol["_exact"]["dot"]
    ck = C.Checks(VERIFIER_VERSION)
    ck.add("domain", "condition", p.is_positive, "given_parameters", {"p": norm["params"]["p"]})
    vert = (p / 2) * (p / 2) + p * (-p)
    ck.add("vertical_chord_instance", "exact_instance", C.is_zero(vert - val), "given_parameters", {"A": "(p/2, p)", "B": "(p/2, −p)"})
    P, M = sp.symbols("p", positive=True), sp.symbols("m", real=True)
    dot, disc, _ = vieta(P, M)
    ident = C.is_zero(dot + 3 * P ** 2 / 4)
    ck.add("symbolic_all_lines", "symbolic_domain", ident, "p>0, every line x=my+p/2", {"statement": "OA·OB = −3p²/4"}, undetermined=not ident)
    dpos = C.is_zero(disc - 4 * P ** 2 * (M ** 2 + 1))
    ck.add("two_distinct_intersections", "symbolic_domain", dpos, "every non-axis line through F", {"discriminant": "4p²(m²+1) > 0"}, undetermined=not dpos)
    ck.add("axis_line_excluded", "condition", True, "configuration", {"excluded": "y=0"})
    rnd = random.Random(seed)
    pf, worst = float(p), 0.0
    for _ in range(48):
        mm = rnd.uniform(-20, 20)
        r = math.sqrt(4 * pf * pf * mm * mm + 4 * pf * pf)
        y1, y2 = (2 * pf * mm + r) / 2, (2 * pf * mm - r) / 2
        x1, x2 = y1 * y1 / (2 * pf), y2 * y2 / (2 * pf)
        worst = max(worst, abs(x1 * x2 + y1 * y2 - float(val)) / max(1.0, abs(float(val))))
    ck.add("numeric_sampling", "sampling", worst < 1e-9, "48 random lines through F", {"max_rel_diff": worst, "note": "sampling consistency only, not a proof"})
    return ck.report(FAMILY_ID, ["exact_instance", "conditions_checked", "symbolic_domain_identity", "numeric_sampling"])


VARIANTS = ["2", "4", "1", "6"]


def check_question(norm, avoid=None):
    def build(p_t):
        p = sp.Integer(p_t)
        y1y2 = -p ** 2
        if not C.is_zero(vieta(p, sp.Integer(1))[2] - y1y2):
            return None
        return {"question": {"id": "cq1", "family_id": FAMILY_ID, "params": {"p": p_t}, "asks": "y1y2",
                             "prompt": "抛物线 y² = %sx 的焦点为 F，过 F 的直线交抛物线于 A(x1, y1)、B(x2, y2)。求 y1·y2。" % fmt.plain(2 * p),
                             "answer_format": "exact"},
                "answer_key": {"id": "cq1", "exact": C.exact_text(y1y2), "plain": fmt.plain(y1y2), "latex": fmt.latex(y1y2)}}
    return C.pick_variant(VARIANTS, norm["params"]["p"], build, avoid)


def problem_text(norm):
    return "已知抛物线 y² = %sx，O 为坐标原点，过焦点 F 的直线与抛物线交于 A、B 两点。求向量 OA 与 OB 的数量积。" % fmt.plain(2 * norm["_exact"]["p"])


def figure(norm, sol):
    p = float(norm["_exact"]["p"])
    ys = [p * 3 * (i / 30.0 - 1) for i in range(61)]
    curve = [[round(y * y / (2 * p), 4), round(y, 4)] for y in ys]
    return C.plane_figure({"O": (0, 0), "F": (p / 2, 0), "A": (p / 2, p), "B": (p / 2, -p)}, [{"pts": curve, "role": "parabola"}],
                          [(["A", "B"], "dashed", "line"), (["O", "A"], "dotted", "aux"), (["O", "B"], "dotted", "aux")], p * 3.2)


def hints(sol):
    return [
        {"level": 1, "kind": "locate", "text": "过焦点的直线可以设成 x = my + p/2，这样不用单独讨论竖直的情况。", "reveals_answer": False},
        {"level": 2, "kind": "prerequisite", "text": "向量数量积 OA·OB = x1x2 + y1y2。", "reveals_answer": False},
        {"level": 3, "kind": "method", "text": "联立后对 y 用韦达定理，再用 x = y²/(2p) 求 x1x2。", "reveals_answer": False},
        {"level": 4, "kind": "partial_step", "text": "；".join(sol["steps"][1]["plain"]), "reveals_answer": False, "gated": True},
        {"level": 5, "kind": "full_solution", "text": "完整解答见讲解。", "reveals_answer": True, "gated": True},
    ]
