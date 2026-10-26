# -*- coding: utf-8 -*-
"""SOLID-ANGLE-02: regular square pyramid P-ABCD (base edge a, height h),
E is the midpoint of PC; find sin of the angle between line BE and plane PAC.

Primary solver: coordinate vectors via the adapted edulab kernel.
Independent verifiers (do not reuse the primary computation):
  * closed form 2a / sqrt(5a^2 + 2h^2), derived by hand and checked as a
    symbolic identity over the whole domain a>0, h>0;
  * synthetic route: BD is perpendicular to plane PAC so the projection of B is O,
    sin = |BO| / |BE| computed from edge lengths only;
  * numeric sampling (reported as sampling only, never as proof).
"""

import random

import sympy as sp

from .. import fmt
from ..errors import MathError
from ..kernel import geometry_kernel as K
from ..params import ParamError, exact_text, parse_exact

FAMILY_ID = "SOLID-ANGLE-02"
SOLVER_VERSION = "solid-angle-02/1.0.0"
VERIFIER_VERSION = "solid-angle-02-verify/1.0.0"
TARGET = "sin_angle_BE_PAC"
UNITS = ("", "mm", "cm", "dm", "m")
MAX_VALUE = 100
METHODS = ("vector_coordinate", "synthetic")
STANDARD_NAMING = {"apex": "P", "base": ["A", "B", "C", "D"], "midpoint": "E"}


def normalize(inp):
    """Validate a confirmed structured problem; returns the canonical input."""
    if inp.get("family_id") != FAMILY_ID:
        raise MathError("UNSUPPORTED_FAMILY", "family_id mismatch")
    if inp.get("input_state", "sufficient") != "sufficient":
        raise MathError("INPUT_AMBIGUOUS", "problem conditions are not confirmed as sufficient")
    target = inp.get("target", TARGET)
    if target != TARGET:
        raise MathError("UNSUPPORTED_FAMILY", "target %r is outside SOLID-ANGLE-02" % target)
    naming = inp.get("naming") or STANDARD_NAMING
    if naming != STANDARD_NAMING:
        raise MathError("UNSUPPORTED_FAMILY", "only standard naming P-ABCD with E on PC is supported")
    unit = inp.get("unit", "")
    if unit not in UNITS:
        raise MathError("PARAMETER_LIMIT", "unit must be one of %s" % ", ".join(u or '""' for u in UNITS))
    params = inp.get("params") or {}
    extra = set(params) - {"a", "h"}
    if extra:
        raise MathError("CONDITION_CONFLICT", "unexpected parameters: %s" % ", ".join(sorted(extra)))
    vals = {}
    for k in ("a", "h"):
        if k not in params:
            raise MathError("INPUT_AMBIGUOUS", "missing parameter %s" % k)
        try:
            v = parse_exact(params[k])
        except ParamError as e:
            raise MathError(e.code, "%s: %s" % (k, e))
        if not (v.is_positive is True):
            raise MathError("CONDITION_CONFLICT", "%s must be positive" % k)
        if (v - MAX_VALUE).is_positive:
            raise MathError("PARAMETER_LIMIT", "%s must be <= %d" % (k, MAX_VALUE))
        vals[k] = v
    mp = inp.get("method_profile") or {}
    allowed = mp.get("allowed") or list(METHODS)
    if not isinstance(allowed, list) or not all(isinstance(m, str) for m in allowed):
        raise MathError("METHOD_MISMATCH", "method_profile.allowed must be a list of method ids")
    usable = [m for m in allowed if m in METHODS]
    if not usable:
        raise MathError("METHOD_MISMATCH", "no allowed method is available for SOLID-ANGLE-02 (supported: %s)" % ", ".join(METHODS))
    preferred = mp.get("preferred") or usable[0]
    if preferred not in usable:
        raise MathError("METHOD_MISMATCH", "preferred method %r is not allowed" % preferred)
    return {"family_id": FAMILY_ID, "target": TARGET, "unit": unit, "naming": STANDARD_NAMING,
            "params": {k: exact_text(v) for k, v in vals.items()}, "method": preferred, "methods_allowed": usable,
            "_exact": vals}


def _points(a, h):
    pts = K.regular_quad_pyramid(a, h)
    pts["E"] = K.midpoint(pts["P"], pts["C"])
    return pts


def solve(norm):
    a, h = norm["_exact"]["a"], norm["_exact"]["h"]
    pts = _points(a, h)
    BE = pts["E"] - pts["B"]
    n = K.normal_from_points(pts["P"], pts["A"], pts["C"])
    n_s = K.simplify_vec(n)
    sin_t = K.line_plane_angle_sin(BE, n_s)
    dot = sp.radsimp(sp.simplify(BE.dot(n_s)))
    nBE = sp.radsimp(sp.simplify(K.norm(BE)))
    nn = sp.radsimp(sp.simplify(K.norm(n_s)))
    BO = sp.radsimp(sp.simplify(K.norm(pts["O"] - pts["B"])))
    coords = {k: {"exact": [exact_text(c) for c in v], "latex": fmt.vec_latex(v), "plain": fmt.vec_plain(v),
                  "float": [float(c) for c in v]} for k, v in pts.items()}
    if norm["method"] == "vector_coordinate":
        steps = [
            {"id": "s1", "title": "建立坐标系", "uses": ["O", "A", "B", "C", "P"],
             "plain": ["以底面中心 O 为原点，OA、OB、OP 分别为 x、y、z 轴。",
                       "A%s，B%s，C%s，P%s。" % (coords["A"]["plain"], coords["B"]["plain"], coords["C"]["plain"], coords["P"]["plain"])],
             "latex": ["A%s,\\ B%s,\\ C%s,\\ P%s" % (coords["A"]["latex"], coords["B"]["latex"], coords["C"]["latex"], coords["P"]["latex"])]},
            {"id": "s2", "title": "求中点 E 与向量 BE", "uses": ["E"],
             "plain": ["E 为 PC 中点，E%s。" % coords["E"]["plain"], "向量 BE = %s，|BE| = %s。" % (fmt.vec_plain(BE), fmt.plain(nBE))],
             "latex": ["E%s,\\ \\overrightarrow{BE}=%s,\\ |\\overrightarrow{BE}|=%s" % (coords["E"]["latex"], fmt.vec_latex(BE), fmt.latex(nBE))]},
            {"id": "s3", "title": "求平面 PAC 的法向量", "uses": ["n"],
             "plain": ["平面 PAC 过 P、A、C，取法向量 n = %s。" % fmt.vec_plain(n_s)],
             "latex": ["\\vec n=%s" % fmt.vec_latex(n_s)]},
            {"id": "s4", "title": "代入线面角公式", "uses": ["sin"], "reveals_answer": True,
             "plain": ["sin θ = |BE·n| / (|BE|·|n|) = %s / (%s·%s) = %s。" % (fmt.plain(abs(dot)), fmt.plain(nBE), fmt.plain(nn), fmt.plain(sin_t))],
             "latex": ["\\sin\\theta=\\dfrac{|\\overrightarrow{BE}\\cdot\\vec n|}{|\\overrightarrow{BE}||\\vec n|}=%s" % fmt.latex(sin_t)]},
        ]
    else:
        steps = [
            {"id": "s1", "title": "找垂线", "uses": ["O"],
             "plain": ["正四棱锥中 PO ⊥ 底面，故 PO ⊥ BD；又 AC ⊥ BD，所以 BD ⊥ 平面 PAC。",
                       "于是 B 在平面 PAC 上的射影为 O，∠BEO 的正弦即所求。"]},
            {"id": "s2", "title": "计算 BO 与 BE", "uses": ["E"],
             "plain": ["BO = a/√2 = %s。" % fmt.plain(BO), "BE = %s（由坐标或中线长计算）。" % fmt.plain(nBE)]},
            {"id": "s3", "title": "求正弦", "uses": ["sin"], "reveals_answer": True,
             "plain": ["sin θ = BO / BE = %s。" % fmt.plain(sin_t)],
             "latex": ["\\sin\\theta=\\dfrac{BO}{BE}=%s" % fmt.latex(sin_t)]},
        ]
    return {
        "family_id": FAMILY_ID, "solver_version": SOLVER_VERSION, "solution_status": "unique",
        "method": norm["method"], "params": norm["params"], "unit": norm["unit"],
        "answer": {"exact": exact_text(sin_t), "latex": fmt.latex(sin_t), "plain": fmt.plain(sin_t),
                   "approx": round(float(sin_t), 6), "unitless": True},
        "coordinates": coords,
        "derived": {"BE": [exact_text(c) for c in BE], "normal_PAC": [exact_text(c) for c in n_s],
                    "BE_len": exact_text(nBE), "BO_len": exact_text(BO)},
        "steps": steps,
        "_exact": {"sin": sin_t},
    }


def closed_form(a, h):
    return 2 * a / sp.sqrt(5 * a ** 2 + 2 * h ** 2)


def verify(norm, sol, seed=20261026):
    a, h = norm["_exact"]["a"], norm["_exact"]["h"]
    s = sol["_exact"]["sin"]
    checks = []

    def add(cid, kind, result, scope, evidence):
        checks.append({"id": cid, "kind": kind, "result": result, "scope": scope,
                       "verifier": VERIFIER_VERSION, "evidence": evidence})

    add("domain", "condition", "passed" if (a.is_positive and h.is_positive) else "failed",
        "given_parameters", {"a": norm["params"]["a"], "h": norm["params"]["h"]})
    pts = _points(a, h)
    BE = pts["E"] - pts["B"]
    nrm = K.normal_from_points(pts["P"], pts["A"], pts["C"])
    nondeg = all(sp.simplify(c) != 0 for c in [K.norm(BE), K.norm(nrm)])
    add("nondegenerate", "condition", "passed" if nondeg else "failed", "given_parameters",
        {"BE_nonzero": True, "plane_PAC_normal_nonzero": True} if nondeg else {})
    cf = closed_form(a, h)
    same = sp.simplify(sp.radsimp(cf - s)) == 0
    add("independent_closed_form", "exact_instance", "passed" if same else "failed", "given_parameters",
        {"closed_form": "2a/sqrt(5a^2+2h^2)", "value": exact_text(sp.radsimp(cf))})
    A, H = sp.symbols("a h", positive=True)
    pa = _points(A, H)
    be = pa["E"] - pa["B"]
    nn = K.normal_from_points(pa["P"], pa["A"], pa["C"])
    general = sp.Abs(be.dot(nn)) / (K.norm(be) * K.norm(nn))
    ident = sp.simplify(general ** 2 - closed_form(A, H) ** 2) == 0 and sp.simplify(general).is_positive
    add("symbolic_domain_identity", "symbolic_domain", "passed" if ident else "undetermined", "a>0,h>0",
        {"statement": "kernel formula equals closed form for all a>0, h>0"})
    BO = sp.sqrt(2) * a / 2
    BE_len = sp.sqrt((a / sp.sqrt(2)) ** 2 * sp.Rational(5, 4) + h ** 2 / 4)
    syn = sp.simplify(sp.radsimp(BO / BE_len - s)) == 0
    add("synthetic_route", "exact_instance", "passed" if syn else "failed", "given_parameters",
        {"route": "BD ⊥ plane PAC ⇒ sinθ = BO/BE"})
    in_range = bool((s > 0) and (s < 1))
    add("answer_range", "condition", "passed" if in_range else "failed", "given_parameters",
        {"interval": "(0,1)", "angle_deg_approx": round(float(sp.asin(s) * 180 / sp.pi), 4)})
    rnd = random.Random(seed)
    worst = 0.0
    for _ in range(24):
        ta, th = rnd.uniform(0.05, 100), rnd.uniform(0.05, 100)
        g = float(general.subs({A: ta, H: th}))
        c = float(closed_form(sp.Float(ta), sp.Float(th)))
        worst = max(worst, abs(g - c))
    add("numeric_sampling", "sampling", "passed" if worst < 1e-9 else "failed", "24 random samples in (0,100]^2",
        {"max_abs_diff": worst, "note": "sampling consistency only, not a proof"})
    failed = [c["id"] for c in checks if c["result"] == "failed"]
    if failed:
        state = "failed"
    elif all(c["result"] == "passed" for c in checks):
        state = "verified"
    else:
        state = "partially_verified"
    levels = ["exact_instance", "conditions_checked"]
    if ident:
        levels.append("symbolic_domain_identity")
    levels.append("numeric_sampling")
    return {"family_id": FAMILY_ID, "verifier_version": VERIFIER_VERSION, "math_state": state,
            "evidence_levels": levels, "failed_checks": failed, "checks": checks,
            "manual_review": "required_for_pedagogy_and_presentation"}


VARIANTS = [("2", "2"), ("4", "2"), ("2", "3"), ("6", "3"), ("1", "1")]


def check_question(norm):
    """Independent self-test variant with different parameters; answer key separate."""
    for a_t, h_t in VARIANTS:
        if (a_t, h_t) != (norm["params"]["a"], norm["params"]["h"]):
            a, h = parse_exact(a_t), parse_exact(h_t)
            pts = _points(a, h)
            be = pts["E"] - pts["B"]
            n = K.simplify_vec(K.normal_from_points(pts["P"], pts["A"], pts["C"]))
            v = K.line_plane_angle_sin(be, n)
            if sp.simplify(v - closed_form(a, h)) != 0:
                continue
            return {"question": {"id": "cq1", "family_id": FAMILY_ID, "params": {"a": a_t, "h": h_t},
                                 "prompt": "正四棱锥 P-ABCD 的底面边长为 %s，高为 %s，E 为 PC 的中点。求直线 BE 与平面 PAC 所成角的正弦值。" % (a_t, h_t),
                                 "answer_format": "exact"},
                    "answer_key": {"id": "cq1", "exact": exact_text(v), "plain": fmt.plain(v), "latex": fmt.latex(v)}}
    raise MathError("VALIDATION_FAILED", "no verified check-question variant")


def problem_text(norm):
    u = norm["unit"]
    return "正四棱锥 P-ABCD 的底面边长为 %s%s，高为 %s%s，E 为 PC 的中点。求直线 BE 与平面 PAC 所成角的正弦值。" % (
        norm["params"]["a"], u, norm["params"]["h"], u)


def figure(sol):
    c = sol["coordinates"]
    return {"kind": "solid3d", "points": {k: {"xyz": v["float"], "label": k} for k, v in c.items()},
            "edges": [["A", "B"], ["B", "C"], ["C", "D"], ["D", "A"], ["P", "A"], ["P", "B"], ["P", "C"], ["P", "D"]],
            "aux": [{"seg": ["B", "E"], "style": "dashed", "role": "line"}, {"seg": ["A", "C"], "style": "dashed", "role": "plane"},
                    {"seg": ["B", "D"], "style": "dotted", "role": "aux"}, {"seg": ["P", "O"], "style": "dotted", "role": "aux"}],
            "faces": [{"pts": ["P", "A", "C"], "role": "plane", "label": "平面 PAC"}]}
