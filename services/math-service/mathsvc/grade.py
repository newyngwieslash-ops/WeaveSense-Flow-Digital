# -*- coding: utf-8 -*-
"""Objective answer judging (FR-S07, AC-27).

Runs inside the isolated stage runner. The response is parsed by the same
whitelist parser as problem parameters (never eval/sympify). Results:
  correct          exactly equivalent (and the unit matches when one is expected)
  correct_approx   decimal within the declared tolerance, only if approximations are allowed
  incorrect        parsed and not equivalent, or the unit is missing/wrong
  needs_exact      a decimal was given but the task requires an exact value
  needs_teacher    free text, steps or a form the judge does not support
  invalid          empty response
The output never contains the expected answer.
"""

import re

import sympy as sp

from .errors import MathError
from .params import LENIENT, ParamError, parse_exact

GRADER_VERSION = "grader/1.0.0"
UNIT_WORDS = {"立方毫米": "mm³", "立方厘米": "cm³", "立方分米": "dm³", "立方米": "m³"}
_UNIT_RE = re.compile(r"(mm|cm|dm|m)(?:\^3|³|3)$")
_DEC_RE = re.compile(r"^-?\d+\.\d+$")
_TRANS = str.maketrans({"（": "(", "）": ")", "－": "-", "−": "-", "／": "/", "×": "*", "·": "*", "，": ",", "【": "[", "】": "]", "［": "[", "］": "]"})


def _expected_unit(ans):
    m = re.search(r"\s((?:mm|cm|dm|m)³)$", ans.get("plain", ""))
    return m.group(1) if m else ""


def _split_unit(s):
    for w, u in UNIT_WORDS.items():
        if s.endswith(w):
            return s[: -len(w)].strip(), u
    m = _UNIT_RE.search(s)
    if m and (len(s) == len(m.group(0)) or not s[len(s) - len(m.group(0)) - 1].isalpha()):
        body = s[: -len(m.group(0))].strip()
        if body:
            return body, m.group(1) + "³"
    return s, ""


def _implicit_mul(t):
    # Students write 2√22, 2sqrt(3), 2(…); make the multiplication explicit for the whitelist parser.
    return re.sub(r"([0-9)])(?=(√|sqrt|\())", r"\1*", t)


def _scalar(text):
    t = _implicit_mul(text.replace(" ", ""))
    if _DEC_RE.match(t):
        return "decimal", sp.Rational(t)
    return "exact", parse_exact(t, LENIENT)


def _equal(a, b):
    return sp.simplify(sp.radsimp(a - b)) == 0


def grade(payload):
    exp = payload.get("expected") or {}
    exact = exp.get("exact")
    if not isinstance(exact, str) or not exact:
        raise MathError("INPUT_AMBIGUOUS", "expected answer missing")
    raw = payload.get("response")
    if not isinstance(raw, str):
        raise MathError("INPUT_AMBIGUOUS", "response must be text")
    approx = payload.get("approx") or {}
    allow_approx, tol = bool(approx.get("allowed")), float(approx.get("rel_tol") or 1e-3)
    s = raw.strip().translate(_TRANS)
    out = {"grader": GRADER_VERSION}
    if not s:
        return dict(out, result="invalid", reason="empty")
    if len(s) > 80 or re.search(r"[\u4e00-\u9fff]", re.sub("|".join(UNIT_WORDS), "", s)):
        return dict(out, result="needs_teacher", reason="free_text_or_steps")
    want_unit = _expected_unit(exp)
    s, got_unit = _split_unit(s)
    if want_unit and payload.get("unit_policy", "required") == "required":
        if not got_unit:
            return dict(out, result="incorrect", reason="unit_missing")
        if got_unit != want_unit:
            return dict(out, result="incorrect", reason="unit_mismatch")
    elif got_unit and not want_unit:
        return dict(out, result="incorrect", reason="unexpected_unit")
    try:
        if exact.startswith("["):
            lo_e, hi_e = [parse_exact(x, LENIENT) for x in exact[1:-1].split(",")]
            m = re.fullmatch(r"\[(.+),(.+)\]", s.replace(" ", ""))
            if not m:
                return dict(out, result="needs_teacher", reason="interval_form_not_supported")
            (k1, lo), (k2, hi) = _scalar(m.group(1)), _scalar(m.group(2))
            if "decimal" in (k1, k2):
                return dict(out, result="needs_exact", reason="decimal_in_interval")
            ok = _equal(lo, lo_e) and _equal(hi, hi_e)
            return dict(out, result="correct" if ok else "incorrect", reason="interval")
        want = parse_exact(exact, LENIENT)
        kind, got = _scalar(s)
    except ParamError:
        return dict(out, result="needs_teacher", reason="unparsed_expression")
    if kind == "decimal":
        if not allow_approx:
            return dict(out, result="needs_exact", reason="decimal_given")
        w = float(want)
        ok = abs(float(got) - w) <= tol * max(1.0, abs(w))
        return dict(out, result="correct_approx" if ok else "incorrect", reason="approx_rel_tol=%g" % tol)
    return dict(out, result="correct" if _equal(got, want) else "incorrect", reason="exact_equivalence")
