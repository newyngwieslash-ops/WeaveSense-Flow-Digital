# -*- coding: utf-8 -*-
"""SOLID-ANGLE-01: cube ABCD-A1B1C1D1 with edge e; the angle between line A1C
and the base plane ABCD, reported as its sine (cos and tan as derived values).

Primary: coordinate vectors (kernel). Independent verifiers: synthetic route
(AA1 ⊥ base so the projection of A1 is A, sin = AA1 / A1C from edge lengths),
symbolic identity over the whole domain e > 0, numeric sampling.
Not generalised to arbitrary line-plane angles.
"""

import random

import sympy as sp

from .. import fmt
from ..kernel import geometry_kernel as K
from . import common as C

FAMILY_ID = "SOLID-ANGLE-01"
TITLE = "正方体中 A1C 与底面所成的角"
SOLVER_VERSION = "solid-angle-01/1.0.0"
VERIFIER_VERSION = "solid-angle-01-verify/1.0.0"
TARGET = "sin_angle_A1C_ABCD"
UNITS = ("", "mm", "cm", "dm", "m")
MAX_VALUE = 100
METHODS = ("vector_coordinate", "synthetic")
STANDARD_NAMING = {"base": ["A", "B", "C", "D"], "top": ["A1", "B1", "C1", "D1"]}


def normalize(inp):
    C.header(inp, FAMILY_ID, TARGET)
    C.naming(inp, FAMILY_ID, STANDARD_NAMING)
    u = C.unit(inp, UNITS)
    vals = C.positive_params(inp.get("params"), ("e",), MAX_VALUE)
    method, usable = C.methods(inp, FAMILY_ID, METHODS)
    return {"family_id": FAMILY_ID, "target": TARGET, "unit": u, "naming": STANDARD_NAMING, "params": C.texts(vals),
            "method": method, "methods_allowed": usable, "_exact": vals}


def _sin(e):
    pts = K.cube(e)
    d = pts["C"] - pts["A1"]
    n = K.normal_from_points(pts["A"], pts["B"], pts["D"])
    return pts, d, K.simplify_vec(n), K.line_plane_angle_sin(d, K.simplify_vec(n))


def solve(norm):
    e = norm["_exact"]["e"]
    pts, d, n, s = _sin(e)
    cos_t = sp.radsimp(sp.sqrt(1 - s ** 2))
    tan_t = sp.radsimp(s / cos_t)
    dl = sp.radsimp(K.norm(d))
    coords = {k: {"exact": [C.exact_text(c) for c in v], "plain": fmt.vec_plain(v), "latex": fmt.vec_latex(v),
                  "float": [float(c) for c in v]} for k, v in pts.items()}
    if norm["method"] == "vector_coordinate":
        steps = [
            {"id": "s1", "title": "建立坐标系", "uses": ["A", "C", "A1"],
             "plain": ["以 A 为原点，AB、AD、AA1 分别为 x、y、z 轴。", "C%s，A1%s。" % (coords["C"]["plain"], coords["A1"]["plain"])]},
            {"id": "s2", "title": "方向向量与法向量", "uses": ["A1", "C"],
             "plain": ["向量 A1C = %s，|A1C| = %s；底面 ABCD 的法向量取 n = %s。" % (fmt.vec_plain(d), fmt.plain(dl), fmt.vec_plain(n))]},
            {"id": "s3", "title": "代入线面角公式", "uses": ["sin"], "reveals_answer": True,
             "plain": ["sin θ = |A1C·n| / (|A1C|·|n|) = %s。" % fmt.plain(s)],
             "latex": ["\\sin\\theta=\\dfrac{|\\overrightarrow{A_1C}\\cdot\\vec n|}{|\\overrightarrow{A_1C}||\\vec n|}=%s" % fmt.latex(s)]},
        ]
    else:
        steps = [
            {"id": "s1", "title": "找射影", "uses": ["A"],
             "plain": ["AA1 ⊥ 底面 ABCD，所以 A1 在底面上的射影为 A，∠A1CA 就是所求的角。"]},
            {"id": "s2", "title": "求相关线段", "uses": ["A1", "C"],
             "plain": ["AA1 = %s，AC = %s，A1C = %s。" % (fmt.plain(e), fmt.plain(sp.radsimp(sp.sqrt(2) * e)), fmt.plain(dl))]},
            {"id": "s3", "title": "求正弦", "uses": ["sin"], "reveals_answer": True,
             "plain": ["sin θ = AA1 / A1C = %s。" % fmt.plain(s)], "latex": ["\\sin\\theta=\\dfrac{AA_1}{A_1C}=%s" % fmt.latex(s)]},
        ]
    return {"family_id": FAMILY_ID, "solver_version": SOLVER_VERSION, "solution_status": "unique", "method": norm["method"],
            "params": norm["params"], "unit": norm["unit"], "answer": C.answer(s), "coordinates": coords,
            "derived": {"A1C": [C.exact_text(c) for c in d], "normal_ABCD": [C.exact_text(c) for c in n],
                        "cos": C.exact_text(cos_t), "tan": C.exact_text(tan_t), "A1C_len": C.exact_text(dl)},
            "steps": steps, "_exact": {"sin": s}}


def verify(norm, sol, seed=20261026):
    e = norm["_exact"]["e"]
    s = sol["_exact"]["sin"]
    ck = C.Checks(VERIFIER_VERSION)
    ck.add("domain", "condition", e.is_positive, "given_parameters", {"e": norm["params"]["e"]})
    syn = e / sp.sqrt(e ** 2 + e ** 2 + e ** 2)
    ck.add("synthetic_route", "exact_instance", C.is_zero(syn - s), "given_parameters", {"route": "AA1 ⊥ ABCD ⇒ sinθ = AA1/A1C"})
    E = sp.symbols("e", positive=True)
    _, d, n, general = _sin(E)
    ident = C.is_zero(general - sp.sqrt(3) / 3)
    ck.add("symbolic_domain_identity", "symbolic_domain", ident, "e>0", {"statement": "sinθ = √3/3 for every e>0"}, undetermined=not ident)
    ck.add("answer_range", "condition", bool(0 < s < 1), "given_parameters", {"interval": "(0,1)"})
    rnd = random.Random(seed)
    worst = 0.0
    for _ in range(24):
        t = rnd.uniform(0.05, 100)
        worst = max(worst, abs(float(general.subs(E, t)) - 3 ** 0.5 / 3))
    ck.add("numeric_sampling", "sampling", worst < 1e-12, "24 random samples in (0,100]", {"max_abs_diff": worst, "note": "sampling consistency only, not a proof"})
    levels = ["exact_instance", "conditions_checked"] + (["symbolic_domain_identity"] if ident else []) + ["numeric_sampling"]
    return ck.report(FAMILY_ID, levels)


PARAM_NAMES = ("e",)  # order of the entries in VARIANTS
VARIANTS = ["2", "3", "4", "5"]


def check_question(norm, avoid=None):
    """Same configuration, different edge, asks for the tangent (not the sine just shown)."""
    def build(e_t):
        e = sp.Integer(e_t)
        _, d, n, s = _sin(e)
        t = sp.radsimp(s / sp.sqrt(1 - s ** 2))
        if not C.is_zero(t - sp.sqrt(2) / 2):
            return None
        return {"question": {"id": "cq1", "family_id": FAMILY_ID, "params": {"e": e_t}, "asks": "tan",
                             "prompt": "正方体 ABCD-A1B1C1D1 的棱长为 %s，求直线 A1C 与底面 ABCD 所成角的正切值。" % e_t, "answer_format": "exact"},
                "answer_key": {"id": "cq1", "exact": C.exact_text(t), "plain": fmt.plain(t), "latex": fmt.latex(t)}}
    return C.pick_variant(VARIANTS, norm["params"]["e"], build, avoid)


def problem_text(norm):
    return "正方体 ABCD-A1B1C1D1 的棱长为 %s%s。求直线 A1C 与底面 ABCD 所成角的正弦值。" % (norm["params"]["e"], norm["unit"])


def figure(norm, sol):
    c = sol["coordinates"]
    return {"kind": "solid3d", "points": {k: {"xyz": v["float"], "label": k} for k, v in c.items()},
            "edges": [["A", "B"], ["B", "C"], ["C", "D"], ["D", "A"], ["A1", "B1"], ["B1", "C1"], ["C1", "D1"], ["D1", "A1"],
                      ["A", "A1"], ["B", "B1"], ["C", "C1"], ["D", "D1"]],
            "aux": [{"seg": ["A1", "C"], "style": "dashed", "role": "line"}, {"seg": ["A", "C"], "style": "dotted", "role": "aux"}],
            "faces": [{"pts": ["A", "B", "C", "D"], "role": "plane", "label": "底面 ABCD"}]}


def hints(sol):
    return [
        {"level": 1, "kind": "locate", "text": "线面角是直线与它在平面内射影所成的角，先找 A1 在底面上的射影。", "reveals_answer": False},
        {"level": 2, "kind": "prerequisite", "text": "线面角的正弦等于直线方向向量与平面法向量夹角余弦的绝对值。", "reveals_answer": False},
        {"level": 3, "kind": "method", "text": "可以在直角三角形 A1AC 中计算，也可以建立坐标系用向量计算。", "reveals_answer": False},
        {"level": 4, "kind": "partial_step", "text": "；".join(sol["steps"][1]["plain"]), "reveals_answer": False, "gated": True},
        {"level": 5, "kind": "full_solution", "text": "完整解答见讲解。", "reveals_answer": True, "gated": True},
    ]


def explore(norm, sol):
    return C.rotate_explore("正方体 ABCD‑A1B1C1D1，虚线是体对角线 A1C，点线 AC 是它在底面上的射影。拖动或用方向键旋转观察。",
                            {"id": "projection", "prompt": "先预测：A1C 在底面 ABCD 上的射影是哪条线段？",
                             "options": ["AC", "BD", "AB", "说不准"]})
