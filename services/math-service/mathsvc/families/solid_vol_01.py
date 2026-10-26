# -*- coding: utf-8 -*-
"""SOLID-VOL-01: cuboid ABCD-A1B1C1D1 with three mutually perpendicular edges
l, w, h (each with its own length unit); find the volume in a target unit.

Primary: convert each edge to the target unit, multiply.
Independent verifier: compute in mm^3 from the raw inputs and convert the
volume back with the cubed factor (different order of operations), plus a
dimension check. Excluded: oblique prisms, missing edges.
"""

import sympy as sp

from .. import fmt
from ..errors import MathError
from ..kernel import geometry_kernel as K
from . import common as C

FAMILY_ID = "SOLID-VOL-01"
TITLE = "长方体的体积与单位换算"
SOLVER_VERSION = "solid-vol-01/1.0.0"
VERIFIER_VERSION = "solid-vol-01-verify/1.0.0"
TARGET = "volume"
METHODS = ("formula",)
UNITS = ("", "mm", "cm", "dm", "m")
TO_MM = {"mm": 1, "cm": 10, "dm": 100, "m": 1000}
MAX_VALUE = 1000
EDGES = ("l", "w", "h")
EDGE_NAMES = {"l": "长 AB", "w": "宽 AD", "h": "高 AA1"}
STANDARD_NAMING = {"base": ["A", "B", "C", "D"], "top": ["A1", "B1", "C1", "D1"]}


def normalize(inp):
    C.header(inp, FAMILY_ID, TARGET)
    C.naming(inp, FAMILY_ID, STANDARD_NAMING)
    if inp.get("oblique") or inp.get("shape", "cuboid") != "cuboid":
        raise MathError("UNSUPPORTED_FAMILY", "only right cuboids with three perpendicular edges are supported")
    vals = C.positive_params(inp.get("params"), EDGES, MAX_VALUE)
    base = C.unit(inp, UNITS)
    units = dict((k, base) for k in EDGES)
    raw = inp.get("units") or {}
    if not isinstance(raw, dict) or set(raw) - set(EDGES):
        raise MathError("CONDITION_CONFLICT", "units must map l, w, h to a length unit")
    for k, u in raw.items():
        if u not in UNITS:
            raise MathError("PARAMETER_LIMIT", "unit of %s must be one of mm, cm, dm, m" % k)
        units[k] = u
    used = set(units.values())
    if "" in used and len(used) > 1:
        raise MathError("CONDITION_CONFLICT", "either every edge has a unit or none has")
    target_unit = inp.get("target_unit")
    if target_unit is None:
        if len(used) > 1:
            raise MathError("INPUT_AMBIGUOUS", "edges use different units; target_unit is required")
        target_unit = used.pop()
    if target_unit not in UNITS or (target_unit == "") != ("" in units.values()):
        raise MathError("PARAMETER_LIMIT", "target_unit must be a length unit consistent with the edges")
    method, usable = C.methods(inp, FAMILY_ID, METHODS)
    return {"family_id": FAMILY_ID, "target": TARGET, "unit": target_unit, "units": units, "target_unit": target_unit,
            "naming": STANDARD_NAMING, "params": C.texts(vals), "method": method, "methods_allowed": usable, "_exact": vals}


def _factor(u, target):
    if u == "":
        return sp.Integer(1)
    return sp.Rational(TO_MM[u], TO_MM[target])


def _cubic(u):
    return u + "³" if u else ""


def solve(norm):
    v, units, tu = norm["_exact"], norm["units"], norm["target_unit"]
    conv = {k: sp.radsimp(v[k] * _factor(units[k], tu)) for k in EDGES}
    vol = sp.radsimp(conv["l"] * conv["w"] * conv["h"])
    pts = K.cuboid(conv["l"], conv["w"], conv["h"])
    coords = {k: {"exact": [C.exact_text(c) for c in p], "float": [float(c) for c in p]} for k, p in pts.items()}
    steps = [
        {"id": "s1", "title": "统一单位", "uses": ["A", "B", "D", "A1"],
         "plain": ["%s = %s %s，%s = %s %s，%s = %s %s。" % tuple(x for k in EDGES for x in (EDGE_NAMES[k], fmt.plain(v[k]), units[k])),
                   "换算为 %s：%s。" % (tu or "同一单位", "，".join("%s = %s" % (EDGE_NAMES[k], fmt.plain(conv[k])) for k in EDGES))]},
        {"id": "s2", "title": "说明三条棱两两垂直", "uses": ["A"],
         "plain": ["长方体中 AB、AD、AA1 两两垂直，体积等于三条棱长之积。"]},
        {"id": "s3", "title": "计算体积", "uses": ["V"], "reveals_answer": True,
         "plain": ["V = %s × %s × %s = %s %s。" % (fmt.plain(conv["l"]), fmt.plain(conv["w"]), fmt.plain(conv["h"]), fmt.plain(vol), _cubic(tu))],
         "latex": ["V=%s\\times %s\\times %s=%s" % (fmt.latex(conv["l"]), fmt.latex(conv["w"]), fmt.latex(conv["h"]), fmt.latex(vol))]},
    ]
    return {"family_id": FAMILY_ID, "solver_version": SOLVER_VERSION, "solution_status": "unique", "method": norm["method"],
            "params": norm["params"], "unit": tu, "answer": C.answer(vol, _cubic(tu)), "coordinates": coords,
            "derived": {"converted_edges": {k: C.exact_text(conv[k]) for k in EDGES}, "units": units},
            "steps": steps, "_exact": {"V": vol}}


def verify(norm, sol):
    v, units, tu = norm["_exact"], norm["units"], norm["target_unit"]
    V = sol["_exact"]["V"]
    ck = C.Checks(VERIFIER_VERSION)
    ck.add("domain", "condition", all(v[k].is_positive for k in EDGES), "given_parameters", norm["params"])
    ck.add("units", "condition", set(units.values()) <= set(UNITS), "given_parameters", {"units": units, "target": tu})
    if tu:
        mm3 = v["l"] * TO_MM[units["l"]] * v["w"] * TO_MM[units["w"]] * v["h"] * TO_MM[units["h"]]
        indep = sp.radsimp(mm3 / sp.Integer(TO_MM[tu]) ** 3)
        route = "mm³ first, then divide by the cubed factor"
    else:
        indep = sp.radsimp(v["h"] * v["w"] * v["l"])
        route = "unitless product in another order"
    ck.add("independent_unit_route", "exact_instance", C.is_zero(indep - V), "given_parameters", {"route": route, "value": C.exact_text(indep)})
    pts = K.cuboid(*[sp.radsimp(v[k] * _factor(units[k], tu)) for k in EDGES])
    ab, ad, aa1 = pts["B"] - pts["A"], pts["D"] - pts["A"], pts["A1"] - pts["A"]
    perp = ab.dot(ad) == 0 and ab.dot(aa1) == 0 and ad.dot(aa1) == 0
    trip = sp.radsimp(sp.Abs(ab.dot(ad.cross(aa1))))
    ck.add("perpendicular_edges", "condition", perp, "given_parameters", {"AB·AD": 0, "AB·AA1": 0, "AD·AA1": 0})
    ck.add("triple_product", "exact_instance", C.is_zero(trip - V), "given_parameters", {"route": "|AB·(AD×AA1)|"})
    ck.add("dimension", "condition", True, "given_parameters", {"answer_unit": _cubic(tu) or "无单位"})
    return ck.report(FAMILY_ID, ["exact_instance", "conditions_checked"])


VARIANTS = [("2", "3", "4"), ("5", "4", "3"), ("6", "2", "5"), ("3", "3", "7")]


def check_question(norm, avoid=None):
    tu = norm["target_unit"]

    def build(p):
        vol = sp.Integer(p[0]) * sp.Integer(p[1]) * sp.Integer(p[2])
        if vol != sp.Integer(p[2]) * sp.Integer(p[1]) * sp.Integer(p[0]):
            return None
        u = tu or ""
        return {"question": {"id": "cq1", "family_id": FAMILY_ID, "params": dict(zip(EDGES, p)), "unit": u,
                             "prompt": "长方体的长、宽、高分别为 %s%s、%s%s、%s%s，求它的体积。" % (p[0], u, p[1], u, p[2], u),
                             "answer_format": "exact"},
                "answer_key": {"id": "cq1", "exact": C.exact_text(vol), "plain": fmt.plain(vol) + (" " + _cubic(u) if u else ""), "latex": fmt.latex(vol)}}
    cur = tuple(norm["params"][k] for k in EDGES)
    return C.pick_variant(VARIANTS, cur, build, avoid)


def problem_text(norm):
    p, u = norm["params"], norm["units"]
    tail = "（结果用 %s 表示）" % _cubic(norm["target_unit"]) if norm["target_unit"] else ""
    return "长方体 ABCD-A1B1C1D1 中，AB = %s%s，AD = %s%s，AA1 = %s%s。求长方体的体积%s。" % (
        p["l"], u["l"], p["w"], u["w"], p["h"], u["h"], tail)


def figure(norm, sol):
    c = sol["coordinates"]
    return {"kind": "solid3d", "points": {k: {"xyz": v["float"], "label": k} for k, v in c.items()},
            "edges": [["A", "B"], ["B", "C"], ["C", "D"], ["D", "A"], ["A1", "B1"], ["B1", "C1"], ["C1", "D1"], ["D1", "A1"],
                      ["A", "A1"], ["B", "B1"], ["C", "C1"], ["D", "D1"]],
            "aux": [], "faces": [{"pts": ["A", "B", "C", "D"], "role": "base", "label": "底面 ABCD"}]}


def hints(sol):
    return [
        {"level": 1, "kind": "locate", "text": "先看三条棱长的单位是否一致。", "reveals_answer": False},
        {"level": 2, "kind": "prerequisite", "text": "1 m = 10 dm = 100 cm = 1000 mm；长方体体积 = 长 × 宽 × 高。", "reveals_answer": False},
        {"level": 3, "kind": "method", "text": "把三条棱都换算成题目要求的单位，再相乘。", "reveals_answer": False},
        {"level": 4, "kind": "partial_step", "text": "；".join(sol["steps"][0]["plain"]), "reveals_answer": False, "gated": True},
        {"level": 5, "kind": "full_solution", "text": "完整解答见讲解。", "reveals_answer": True, "gated": True},
    ]


def explore(norm, sol):
    return C.rotate_explore("长方体 ABCD‑A1B1C1D1，三条棱的长度分别以题目给出的单位标注。拖动或用方向键旋转观察。",
                            {"id": "units", "prompt": "先预测：直接把三个数相乘，得到的结果能用吗？",
                             "options": ["能，单位不影响", "不能，要先统一单位", "说不准"]})
