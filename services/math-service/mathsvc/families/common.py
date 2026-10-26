# -*- coding: utf-8 -*-
"""Shared input checks and evidence helpers for the P0 problem families.

Every family module exposes the same interface (see pipeline.py):
FAMILY_ID, TITLE, normalize, solve, verify, check_question, problem_text,
figure, hints. Evidence is layered (constraint 4): exact instance checks,
symbolic domain identities and numeric sampling are reported separately and
sampling is never presented as proof.
"""

import sympy as sp

from ..errors import MathError
from ..params import ParamError, exact_text, parse_exact


def header(inp, family_id, target):
    if inp.get("family_id") != family_id:
        raise MathError("UNSUPPORTED_FAMILY", "family_id mismatch")
    if inp.get("input_state", "sufficient") != "sufficient":
        raise MathError("INPUT_AMBIGUOUS", "problem conditions are not confirmed as sufficient")
    t = inp.get("target", target)
    if t != target:
        raise MathError("UNSUPPORTED_FAMILY", "target %r is outside %s" % (t, family_id))


def naming(inp, family_id, standard):
    n = inp.get("naming") or standard
    if n != standard:
        raise MathError("UNSUPPORTED_FAMILY", "only the standard naming of %s is supported" % family_id)


def positive_params(params, names, max_value):
    params = params or {}
    extra = set(params) - set(names)
    if extra:
        raise MathError("CONDITION_CONFLICT", "unexpected parameters: %s" % ", ".join(sorted(extra)))
    vals = {}
    for k in names:
        if k not in params:
            raise MathError("INPUT_AMBIGUOUS", "missing parameter %s" % k)
        try:
            v = parse_exact(params[k])
        except ParamError as e:
            raise MathError(e.code, "%s: %s" % (k, e))
        if v.is_positive is not True:
            raise MathError("CONDITION_CONFLICT", "%s must be positive" % k)
        if (v - max_value).is_positive:
            raise MathError("PARAMETER_LIMIT", "%s must be <= %s" % (k, max_value))
        vals[k] = v
    return vals


def methods(inp, family_id, supported):
    mp = inp.get("method_profile") or {}
    allowed = mp.get("allowed") or list(supported)
    if not isinstance(allowed, list) or not all(isinstance(m, str) for m in allowed):
        raise MathError("METHOD_MISMATCH", "method_profile.allowed must be a list of method ids")
    usable = [m for m in allowed if m in supported]
    if not usable:
        raise MathError("METHOD_MISMATCH", "no allowed method is available for %s (supported: %s)" % (family_id, ", ".join(supported)))
    preferred = mp.get("preferred") or usable[0]
    if preferred not in usable:
        raise MathError("METHOD_MISMATCH", "preferred method %r is not allowed" % preferred)
    return preferred, usable


def unit(inp, units):
    u = inp.get("unit", "")
    if u not in units:
        raise MathError("PARAMETER_LIMIT", "unit must be one of %s" % ", ".join(x or '""' for x in units))
    return u


def texts(vals):
    return {k: exact_text(v) for k, v in vals.items()}


def is_zero(expr):
    return sp.simplify(sp.radsimp(expr)) == 0


def answer(value, unit_suffix=""):
    from .. import fmt
    plain = fmt.plain(value) + (" " + unit_suffix if unit_suffix else "")
    return {"exact": exact_text(value), "latex": fmt.latex(value), "plain": plain,
            "approx": round(float(value), 6), "unitless": not unit_suffix}


class Checks:
    def __init__(self, verifier):
        self.verifier = verifier
        self.items = []

    def add(self, cid, kind, ok, scope, evidence, undetermined=False):
        result = "undetermined" if undetermined else ("passed" if ok else "failed")
        self.items.append({"id": cid, "kind": kind, "result": result, "scope": scope,
                           "verifier": self.verifier, "evidence": evidence})

    def report(self, family_id, levels, extra=None):
        failed = [c["id"] for c in self.items if c["result"] == "failed"]
        if failed:
            state = "failed"
        elif all(c["result"] == "passed" for c in self.items):
            state = "verified"
        else:
            state = "partially_verified"
        out = {"family_id": family_id, "verifier_version": self.verifier, "math_state": state,
               "evidence_levels": levels, "failed_checks": failed, "checks": self.items,
               "manual_review": "required_for_pedagogy_and_presentation"}
        if extra:
            out.update(extra)
        return out


def pick_variant(variants, current, build, avoid=None):
    """First variant with different parameters whose answer is verified by
    build() and differs from the answer just shown (FR-M07)."""
    for v in variants:
        if v == current:
            continue
        got = build(v)
        if got is not None and got["answer_key"]["exact"] != avoid:
            return got
    raise MathError("VALIDATION_FAILED", "no verified check-question variant")


def plane_figure(points, curves, segments, extent):
    """2D figure: points/curves are floats on the xy plane (z = 0)."""
    return {"kind": "plane2d", "extent": extent,
            "points": {k: {"xyz": [float(x), float(y), 0.0], "label": k} for k, (x, y) in points.items()},
            "curves": curves, "edges": [], "aux": [{"seg": s, "style": st, "role": r} for s, st, r in segments], "faces": []}
