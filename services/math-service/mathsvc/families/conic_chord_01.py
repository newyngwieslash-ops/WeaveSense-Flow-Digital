# -*- coding: utf-8 -*-
"""CONIC-CHORD-01: ellipse x²/a² + y²/b² = 1 (a > b > 0), chords through the
right focus F(c, 0); range of the chord length and the endpoint configurations.

Primary (Vieta): non-vertical chords y = k(x - c) give
  L(k) = 2ab²(1 + k²) / (b² + a²k²), strictly decreasing in t = k² ≥ 0,
so L ∈ (2b²/a, 2a] for non-vertical chords; the vertical chord x = c is
checked separately and has length 2b²/a, so the full range is [2b²/a, 2a].
Independent verifiers: the focal polar form 2ab² / (a² − c²cos²θ) checked as a
symbolic identity, explicit endpoint chords, discriminant positivity (two
distinct real intersections for every line), numeric sampling.
"""

import random

import sympy as sp

from .. import fmt
from ..errors import MathError
from . import common as C

FAMILY_ID = "CONIC-CHORD-01"
TITLE = "椭圆焦点弦长的取值范围"
SOLVER_VERSION = "conic-chord-01/1.0.0"
VERIFIER_VERSION = "conic-chord-01-verify/1.0.0"
TARGET = "focal_chord_length_range"
MAX_VALUE = 100
METHODS = ("vieta",)
STANDARD_NAMING = {"focus": "F", "chord": ["A", "B"]}


def normalize(inp):
    C.header(inp, FAMILY_ID, TARGET)
    C.naming(inp, FAMILY_ID, STANDARD_NAMING)
    C.unit(inp, ("",))
    if inp.get("focus", "right") != "right":
        raise MathError("UNSUPPORTED_FAMILY", "only chords through the right focus are supported")
    vals = C.positive_params(inp.get("params"), ("a", "b"), MAX_VALUE)
    if not (vals["a"] - vals["b"]).is_positive:
        raise MathError("CONDITION_CONFLICT", "requires a > b > 0 (foci on the x axis, not a circle)")
    method, usable = C.methods(inp, FAMILY_ID, METHODS)
    return {"family_id": FAMILY_ID, "target": TARGET, "unit": "", "naming": STANDARD_NAMING, "params": C.texts(vals),
            "method": method, "methods_allowed": usable, "_exact": vals}


def _c(a, b):
    return sp.radsimp(sp.sqrt(a ** 2 - b ** 2))


def chord_formula(a, b, k):
    """Chord length from the quadratic in x (Vieta), as a function of slope k."""
    c = _c(a, b)
    A = b ** 2 + a ** 2 * k ** 2
    B = -2 * a ** 2 * k ** 2 * c
    Cc = a ** 2 * k ** 2 * c ** 2 - a ** 2 * b ** 2
    disc = sp.factor(sp.expand(B ** 2 - 4 * A * Cc))
    L = sp.sqrt(1 + k ** 2) * sp.sqrt(disc) / A
    return sp.simplify(L), disc


def _interval(lo, hi):
    return {"exact": "[%s, %s]" % (C.exact_text(lo), C.exact_text(hi)), "plain": "[%s, %s]" % (fmt.plain(lo), fmt.plain(hi)),
            "latex": "\\left[%s,\\ %s\\right]" % (fmt.latex(lo), fmt.latex(hi)),
            "approx": [round(float(lo), 6), round(float(hi), 6)], "unitless": True}


def solve(norm):
    a, b = norm["_exact"]["a"], norm["_exact"]["b"]
    c = _c(a, b)
    k = sp.symbols("k", real=True)
    L, disc = chord_formula(a, b, k)
    lo, hi = sp.radsimp(2 * b ** 2 / a), sp.radsimp(2 * a)
    steps = [
        {"id": "s1", "title": "求焦点", "uses": ["F"], "plain": ["c² = a² − b²，c = %s，右焦点 F(%s, 0)。" % (fmt.plain(c), fmt.plain(c))]},
        {"id": "s2", "title": "竖直弦单独讨论", "uses": ["A", "B"],
         "plain": ["当弦垂直于 x 轴时，x = c，代入得 y = ±b²/a，弦长为 2b²/a = %s。" % fmt.plain(lo)]},
        {"id": "s3", "title": "斜率存在时联立", "uses": ["k"],
         "plain": ["设 y = k(x − c)，代入椭圆得 (b² + a²k²)x² − 2a²k²c·x + a²k²c² − a²b² = 0，判别式恒正，必有两个不同交点。",
                   "由韦达定理，弦长 L = 2ab²(1 + k²)/(b² + a²k²)。"]},
        {"id": "s4", "title": "研究单调性与端点", "uses": ["range"], "reveals_answer": True,
         "plain": ["令 t = k² ≥ 0，L 随 t 增大而严格减小：t = 0（水平弦，即长轴）时 L = 2a = %s；t → ∞ 时 L → 2b²/a。" % fmt.plain(hi),
                   "结合竖直弦，弦长取值范围为 %s，最大值在水平弦取到，最小值在竖直弦取到。" % _interval(lo, hi)["plain"]],
         "latex": ["L\\in%s" % _interval(lo, hi)["latex"]]},
    ]
    return {"family_id": FAMILY_ID, "solver_version": SOLVER_VERSION, "solution_status": "unique", "method": norm["method"],
            "params": norm["params"], "unit": "", "answer": _interval(lo, hi),
            "configurations": {"max": {"chord": "horizontal", "length": C.exact_text(hi), "endpoints": "(-a,0),(a,0)"},
                               "min": {"chord": "vertical", "length": C.exact_text(lo), "endpoints": "(c,±b²/a)"}},
            "derived": {"c": C.exact_text(c), "L_of_k": sp.sstr(L), "discriminant": sp.sstr(disc)},
            "steps": steps, "_exact": {"lo": lo, "hi": hi, "c": c}}


def verify(norm, sol, seed=20261026):
    a, b = norm["_exact"]["a"], norm["_exact"]["b"]
    lo, hi, c = sol["_exact"]["lo"], sol["_exact"]["hi"], sol["_exact"]["c"]
    ck = C.Checks(VERIFIER_VERSION)
    ck.add("domain", "condition", bool((a - b).is_positive and b.is_positive), "given_parameters", {"a>b>0": True})
    A, Bs, k, th = sp.symbols("a b k theta", positive=True)
    k_r = sp.symbols("kr", real=True)
    Lsym, disc = chord_formula(A, Bs, k_r)
    closed = 2 * A * Bs ** 2 * (1 + k_r ** 2) / (Bs ** 2 + A ** 2 * k_r ** 2)
    sym_ok = C.is_zero(sp.simplify(Lsym ** 2 - closed ** 2))
    ck.add("vieta_closed_form", "symbolic_domain", sym_ok, "a>b>0, k real", {"statement": "L(k) = 2ab²(1+k²)/(b²+a²k²)"}, undetermined=not sym_ok)
    polar = 2 * A * Bs ** 2 / (A ** 2 - (A ** 2 - Bs ** 2) * sp.cos(th) ** 2)
    pol_ok = C.is_zero(sp.simplify(polar.subs(sp.cos(th) ** 2, 1 / (1 + k ** 2)) - closed.subs(k_r, k)))
    ck.add("independent_polar_form", "symbolic_domain", pol_ok, "a>b>0, θ∈(0,π)", {"statement": "focal polar form 2ab²/(a²−c²cos²θ) agrees"}, undetermined=not pol_ok)
    t = sp.symbols("t", nonnegative=True)
    Lt = 2 * a * b ** 2 * (1 + t) / (b ** 2 + a ** 2 * t)
    dL = sp.simplify(sp.diff(Lt, t) * (b ** 2 + a ** 2 * t) ** 2)
    mono = C.is_zero(dL - 2 * a * b ** 2 * (b ** 2 - a ** 2)) and (b ** 2 - a ** 2).is_negative
    ck.add("monotonic_in_k2", "symbolic_domain", mono, "t=k²≥0", {"derivative_numerator": sp.sstr(dL), "sign": "negative"}, undetermined=not mono)
    lim = sp.limit(Lt, t, sp.oo)
    ck.add("limit_matches_vertical", "exact_instance", C.is_zero(lim - lo), "given_parameters", {"limit_t_to_inf": C.exact_text(lim)})
    yv = sp.radsimp(b * sp.sqrt(1 - c ** 2 / a ** 2))
    ck.add("endpoint_vertical_attained", "exact_instance", C.is_zero(2 * yv - lo), "given_parameters", {"chord": "x=c", "length": C.exact_text(2 * yv)})
    ck.add("endpoint_horizontal_attained", "exact_instance", C.is_zero(Lt.subs(t, 0) - hi) and C.is_zero(2 * a - hi), "given_parameters",
           {"chord": "y=0", "intersections": "(-a,0),(a,0) distinct"})
    d_inst = sp.expand(disc.subs({A: a, Bs: b}))
    pos = sp.simplify(d_inst - 4 * a ** 2 * b ** 4 * (1 + k_r ** 2)) == 0
    ck.add("two_distinct_intersections", "symbolic_domain", pos, "every non-vertical line through F", {"discriminant": "4a²b⁴(1+k²) > 0"}, undetermined=not pos)
    rnd = random.Random(seed)
    af, bf, cf = float(a), float(b), float(c)
    bad = 0
    for _ in range(48):
        kk = rnd.uniform(-50, 50)
        qa, qb, qc = bf ** 2 + af ** 2 * kk ** 2, -2 * af ** 2 * kk ** 2 * cf, af ** 2 * kk ** 2 * cf ** 2 - af ** 2 * bf ** 2
        r = (qb * qb - 4 * qa * qc) ** 0.5
        x1, x2 = (-qb + r) / (2 * qa), (-qb - r) / (2 * qa)
        L = ((x1 - x2) ** 2 + (kk * (x1 - x2)) ** 2) ** 0.5
        if not (float(lo) - 1e-9 <= L <= float(hi) + 1e-9):
            bad += 1
    ck.add("numeric_sampling", "sampling", bad == 0, "48 random slopes in [-50,50]", {"out_of_range": bad, "note": "sampling consistency only, not a proof"})
    return ck.report(FAMILY_ID, ["exact_instance", "conditions_checked", "symbolic_domain_identity", "numeric_sampling"],
                     {"endpoint_reachability": {"max": "attained", "min": "attained"}})


PARAM_NAMES = ("a", "b")  # order of the entries in VARIANTS
VARIANTS = [("2", "sqrt(3)"), ("3", "2"), ("5", "4"), ("4", "2")]


def check_question(norm, avoid=None):
    def build(p):
        a, b = sp.Integer(p[0]), (sp.sqrt(3) if p[1] == "sqrt(3)" else sp.Integer(p[1]))
        lo = sp.radsimp(2 * b ** 2 / a)
        if not (a > b):
            return None
        return {"question": {"id": "cq1", "family_id": FAMILY_ID, "params": {"a": p[0], "b": p[1]}, "asks": "min_length",
                             "prompt": "椭圆 x²/%s + y²/%s = 1 过右焦点的弦中，最短弦长是多少？" % (fmt.plain(a ** 2), fmt.plain(b ** 2)), "answer_format": "exact"},
                "answer_key": {"id": "cq1", "exact": C.exact_text(lo), "plain": fmt.plain(lo), "latex": fmt.latex(lo)}}
    return C.pick_variant(VARIANTS, (norm["params"]["a"], norm["params"]["b"]), build, avoid)


def problem_text(norm):
    a, b = norm["_exact"]["a"], norm["_exact"]["b"]
    return "已知椭圆 x²/%s + y²/%s = 1，F 为其右焦点，过 F 的直线与椭圆交于 A、B 两点。求弦长 |AB| 的取值范围，并指出取到最大值和最小值时弦的位置。" % (
        fmt.plain(a ** 2), fmt.plain(b ** 2))


def figure(norm, sol):
    import math
    a, b, c = float(norm["_exact"]["a"]), float(norm["_exact"]["b"]), float(sol["_exact"]["c"])
    curve = [[round(a * math.cos(2 * math.pi * i / 72), 4), round(b * math.sin(2 * math.pi * i / 72), 4)] for i in range(73)]
    yv = b * b / a
    return C.plane_figure({"O": (0, 0), "F": (c, 0), "A": (c, yv), "B": (c, -yv), "M": (-a, 0), "N": (a, 0)},
                          [{"pts": curve, "role": "ellipse"}],
                          [(["A", "B"], "dashed", "line"), (["M", "N"], "dotted", "aux")], max(a, b) * 1.15)


def hints(sol):
    return [
        {"level": 1, "kind": "locate", "text": "过焦点的弦要分“斜率存在”和“垂直于 x 轴”两种情况。", "reveals_answer": False},
        {"level": 2, "kind": "prerequisite", "text": "联立直线与椭圆后用韦达定理和弦长公式 |AB| = √(1+k²)·|x1 − x2|。", "reveals_answer": False},
        {"level": 3, "kind": "method", "text": "把弦长写成 k² 的函数，研究它的单调性和极限。", "reveals_answer": False},
        {"level": 4, "kind": "partial_step", "text": "；".join(sol["steps"][2]["plain"]), "reveals_answer": False, "gated": True},
        {"level": 5, "kind": "full_solution", "text": "完整解答见讲解。", "reveals_answer": True, "gated": True},
    ]


def explore(norm, sol):
    import math
    a, b, c = float(norm["_exact"]["a"]), float(norm["_exact"]["b"]), float(sol["_exact"]["c"])

    def frame(deg):
        t = math.radians(deg)
        dx, dy = math.cos(t), math.sin(t)
        # Points F + s(dx, dy) on x²/a² + y²/b² = 1: quadratic in s.
        qa = dx * dx / (a * a) + dy * dy / (b * b)
        qb = 2 * c * dx / (a * a)
        qc = c * c / (a * a) - 1
        r = math.sqrt(qb * qb - 4 * qa * qc)
        s1, s2 = (-qb + r) / (2 * qa), (-qb - r) / (2 * qa)
        return {"A": (c + s1 * dx, s1 * dy), "B": (c + s2 * dx, s2 * dy)}, ""

    return C.param_explore("theta", "弦 AB 的倾斜角", "°", [float(d) for d in range(0, 180, 5)], frame, 90.0,
                           "椭圆中心 O，右焦点 F。拖动滑块让过 F 的直线绕 F 旋转，A、B 是直线与椭圆的两个交点。",
                           {"id": "chord_extreme", "prompt": "先预测：弦 AB 绕 F 旋转一周的过程中，弦长最短出现在什么位置？",
                            "options": ["弦与 x 轴垂直时", "弦与 x 轴重合时", "弦长始终不变", "说不准"]},
                           "倾斜角取 0° 到 175°（每 5° 一帧）；0° 时直线与长轴重合，弦即长轴。")
