# -*- coding: utf-8 -*-
"""CONIC-SLOPE-01: ellipse x²/a² + y²/b² = 1 (a > b > 0), P = (a, 0); A and B on
the ellipse, symmetric about the origin; the constant k_PA · k_PB.

Primary (point substitution): A(x0, y0), B(−x0, −y0),
  k_PA·k_PB = y0² / (x0² − a²) = −b²/a² using y0² = b²(1 − x0²/a²).
Excluded (and listed as disabled points for interaction): A or B at the
left/right vertices (±a, 0), where a slope is undefined or B coincides with P.
Independent verifiers: parametrisation A = (a cosφ, b sinφ) as a symbolic
identity for sinφ ≠ 0, explicit instance points, numeric sampling.
"""

import math
import random

import sympy as sp

from .. import fmt
from ..errors import MathError
from . import common as C

FAMILY_ID = "CONIC-SLOPE-01"
TITLE = "椭圆中斜率之积为定值"
SOLVER_VERSION = "conic-slope-01/1.0.0"
VERIFIER_VERSION = "conic-slope-01-verify/1.0.0"
TARGET = "slope_product_PA_PB"
MAX_VALUE = 100
METHODS = ("point_substitution",)
STANDARD_NAMING = {"vertex": "P", "points": ["A", "B"]}


def normalize(inp):
    C.header(inp, FAMILY_ID, TARGET)
    C.naming(inp, FAMILY_ID, STANDARD_NAMING)
    C.unit(inp, ("",))
    vals = C.positive_params(inp.get("params"), ("a", "b"), MAX_VALUE)
    if not (vals["a"] - vals["b"]).is_positive:
        raise MathError("CONDITION_CONFLICT", "requires a > b > 0")
    method, usable = C.methods(inp, FAMILY_ID, METHODS)
    return {"family_id": FAMILY_ID, "target": TARGET, "unit": "", "naming": STANDARD_NAMING, "params": C.texts(vals),
            "method": method, "methods_allowed": usable, "_exact": vals}


def product_at(a, b, x0, y0):
    k1 = y0 / (x0 - a)
    k2 = (-y0) / (-x0 - a)
    return sp.simplify(k1 * k2)


def solve(norm):
    a, b = norm["_exact"]["a"], norm["_exact"]["b"]
    x0, y0 = sp.symbols("x0 y0", real=True)
    raw = sp.simplify((y0 / (x0 - a)) * ((-y0) / (-x0 - a)))
    sub = sp.simplify(raw.subs(y0 ** 2, b ** 2 * (1 - x0 ** 2 / a ** 2)))
    val = sp.radsimp(-b ** 2 / a ** 2)
    if not C.is_zero(sub - val):
        raise MathError("VALIDATION_FAILED", "substitution did not reduce to a constant")
    steps = [
        {"id": "s1", "title": "设点", "uses": ["A", "B"], "plain": ["设 A(x0, y0)，则 B(−x0, −y0)，且 x0 ≠ ±a（否则斜率不存在或 B 与 P 重合）。"]},
        {"id": "s2", "title": "写出斜率之积", "uses": ["P"],
         "plain": ["k_PA·k_PB = y0/(x0 − a) · (−y0)/(−x0 − a) = y0²/(x0² − a²)。"]},
        {"id": "s3", "title": "代入椭圆方程", "uses": ["k"], "reveals_answer": True,
         "plain": ["由 y0² = b²(1 − x0²/a²) 得 k_PA·k_PB = −b²/a² = %s，与 A 的位置无关。" % fmt.plain(val)],
         "latex": ["k_{PA}\\cdot k_{PB}=-\\dfrac{b^2}{a^2}=%s" % fmt.latex(val)]},
    ]
    return {"family_id": FAMILY_ID, "solver_version": SOLVER_VERSION, "solution_status": "unique", "method": norm["method"],
            "params": norm["params"], "unit": "", "answer": C.answer(val),
            "excluded_points": [{"A": "(a,0)", "reason": "B = P，k_PB 无定义"}, {"A": "(-a,0)", "reason": "k_PA 无定义"}],
            "derived": {"general": "y0²/(x0²−a²)"}, "steps": steps, "_exact": {"k": val}}


def verify(norm, sol, seed=20261026):
    a, b = norm["_exact"]["a"], norm["_exact"]["b"]
    val = sol["_exact"]["k"]
    ck = C.Checks(VERIFIER_VERSION)
    ck.add("domain", "condition", bool((a - b).is_positive and b.is_positive), "given_parameters", {"a>b>0": True})
    A, B = sp.symbols("a b", positive=True)
    phi = sp.symbols("phi", real=True)
    gen = sp.simplify((B * sp.sin(phi) / (A * sp.cos(phi) - A)) * (B * sp.sin(phi) / (A * sp.cos(phi) + A)))
    ident = C.is_zero(sp.trigsimp(gen + B ** 2 / A ** 2))
    ck.add("parametric_identity", "symbolic_domain", ident, "a>b>0, sinφ≠0", {"statement": "A=(a cosφ, b sinφ) gives −b²/a²"}, undetermined=not ident)
    inst = product_at(a, b, sp.Integer(0), b)
    ck.add("instance_minor_vertex", "exact_instance", C.is_zero(inst - val), "given_parameters", {"A": "(0,b)", "value": C.exact_text(inst)})
    x1 = a / 2
    y1 = sp.radsimp(b * sp.sqrt(3) / 2)
    inst2 = product_at(a, b, x1, y1)
    ck.add("instance_other_point", "exact_instance", C.is_zero(inst2 - val), "given_parameters", {"A": "(a/2, √3b/2)"})
    ck.add("excluded_points_declared", "condition", len(sol["excluded_points"]) == 2, "configuration", {"excluded": ["(a,0)", "(-a,0)"]})
    rnd = random.Random(seed)
    af, bf, worst = float(a), float(b), 0.0
    for _ in range(48):
        t = rnd.uniform(0.01, math.pi - 0.01) * rnd.choice([1, -1])
        x0, y0 = af * math.cos(t), bf * math.sin(t)
        worst = max(worst, abs(y0 / (x0 - af) * (-y0) / (-x0 - af) - float(val)))
    ck.add("numeric_sampling", "sampling", worst < 1e-9, "48 random points on the ellipse", {"max_abs_diff": worst, "note": "sampling consistency only, not a proof"})
    return ck.report(FAMILY_ID, ["exact_instance", "conditions_checked", "symbolic_domain_identity", "numeric_sampling"])


PARAM_NAMES = ("a", "b")  # order of the entries in VARIANTS
VARIANTS = [("2", "1"), ("3", "2"), ("4", "3"), ("5", "3")]


def check_question(norm, avoid=None):
    def build(p):
        a, b = sp.Integer(p[0]), sp.Integer(p[1])
        v = sp.radsimp(-b ** 2 / a ** 2)
        if not C.is_zero(product_at(a, b, sp.Integer(0), b) - v):
            return None
        return {"question": {"id": "cq1", "family_id": FAMILY_ID, "params": {"a": p[0], "b": p[1]},
                             "prompt": "椭圆 x²/%s + y²/%s = 1 的右顶点为 P，A、B 是椭圆上关于原点对称的两点（不在 x 轴上）。求直线 PA 与 PB 的斜率之积。" % (
                                 fmt.plain(a ** 2), fmt.plain(b ** 2)), "answer_format": "exact"},
                "answer_key": {"id": "cq1", "exact": C.exact_text(v), "plain": fmt.plain(v), "latex": fmt.latex(v)}}
    return C.pick_variant(VARIANTS, (norm["params"]["a"], norm["params"]["b"]), build, avoid)


def problem_text(norm):
    a, b = norm["_exact"]["a"], norm["_exact"]["b"]
    return "已知椭圆 x²/%s + y²/%s = 1，P 为右顶点，A、B 是椭圆上关于原点对称的两点，且 A、B 都不是左右顶点。求直线 PA 与 PB 的斜率之积。" % (
        fmt.plain(a ** 2), fmt.plain(b ** 2))


def figure(norm, sol):
    a, b = float(norm["_exact"]["a"]), float(norm["_exact"]["b"])
    curve = [[round(a * math.cos(2 * math.pi * i / 72), 4), round(b * math.sin(2 * math.pi * i / 72), 4)] for i in range(73)]
    t = 2 * math.pi / 3
    fig = C.plane_figure({"O": (0, 0), "P": (a, 0), "A": (a * math.cos(t), b * math.sin(t)), "B": (-a * math.cos(t), -b * math.sin(t))},
                         [{"pts": curve, "role": "ellipse"}],
                         [(["P", "A"], "dashed", "line"), (["P", "B"], "dashed", "line"), (["A", "B"], "dotted", "aux")], max(a, b) * 1.15)
    fig["disabled_points"] = [{"xy": [a, 0.0], "reason": "B 与 P 重合"}, {"xy": [-a, 0.0], "reason": "斜率不存在"}]
    return fig


def hints(sol):
    return [
        {"level": 1, "kind": "locate", "text": "A、B 关于原点对称，可以只设一个点的坐标。", "reveals_answer": False},
        {"level": 2, "kind": "prerequisite", "text": "两点连线的斜率 k = (y2 − y1)/(x2 − x1)，要求 x1 ≠ x2。", "reveals_answer": False},
        {"level": 3, "kind": "method", "text": "写出两条斜率之积，再用点在椭圆上的条件消去 y0²。", "reveals_answer": False},
        {"level": 4, "kind": "partial_step", "text": "；".join(sol["steps"][1]["plain"]), "reveals_answer": False, "gated": True},
        {"level": 5, "kind": "full_solution", "text": "完整解答见讲解。", "reveals_answer": True, "gated": True},
    ]


def explore(norm, sol):
    a, b = float(norm["_exact"]["a"]), float(norm["_exact"]["b"])

    def frame(deg):
        if deg % 180 == 0:
            return None, "此时 A 或 B 与 P 重合，直线 PA 或 PB 不存在，不在题目考虑范围内。"
        t = math.radians(deg)
        x, y = a * math.cos(t), b * math.sin(t)
        return {"P": (a, 0.0), "A": (x, y), "B": (-x, -y)}, ""

    return C.param_explore("t", "点 A 的位置参数", "°", [float(d) for d in range(0, 360, 5)], frame, 120.0,
                           "A 在椭圆上移动，B 始终是 A 关于原点的对称点，P 是右顶点。观察直线 PA 与 PB 的倾斜程度如何一起变化。",
                           {"id": "slope_product", "prompt": "先预测：A 在椭圆上移动时，直线 PA 与 PB 斜率的乘积会怎样？",
                            "options": ["随 A 变化", "保持不变", "有时不存在，其余时候随 A 变化", "说不准"]},
                           "参数取 0° 到 355°（每 5° 一帧）；0° 与 180° 为退化位置。")
