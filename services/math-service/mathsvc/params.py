# -*- coding: utf-8 -*-
"""Restricted exact-number parser.

Accepts integers, rationals (a/b) and bounded square roots such as
"2", "3/2", "-1", "sqrt(3)", "2*sqrt(3)/3", "√2". The text is parsed with
ast.parse (no evaluation) and only a fixed node whitelist is walked; values
are built with sympy constructors directly. sympify / eval are never used.
"""

import ast
import re

import sympy as sp

MAX_TEXT = 48
MAX_NODES = 16
MAX_DEPTH = 6
MAX_INT = 10 ** 6
MAX_RADICAND = 10 ** 4


class ParamError(ValueError):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


_SQRT_SIGN = re.compile(r"√\s*(\d+)")


def parse_exact(text):
    if isinstance(text, bool):
        raise ParamError("PARAMETER_LIMIT", "boolean is not a number")
    if isinstance(text, int):
        text = str(text)
    if not isinstance(text, str):
        raise ParamError("PARAMETER_LIMIT", "parameter must be a string or integer")
    s = text.strip()
    if not s or len(s) > MAX_TEXT:
        raise ParamError("PARAMETER_LIMIT", "parameter text empty or longer than %d" % MAX_TEXT)
    s = _SQRT_SIGN.sub(r"sqrt(\1)", s)
    if not re.fullmatch(r"[0-9sqrt()+\-*/ ]+", s):
        raise ParamError("PARAMETER_LIMIT", "only integers, fractions and sqrt() are allowed")
    try:
        tree = ast.parse(s, mode="eval")
    except SyntaxError:
        raise ParamError("PARAMETER_LIMIT", "cannot parse parameter")
    count = [0]
    return sp.radsimp(_walk(tree.body, 0, count, False))


def _walk(node, depth, count, in_sqrt):
    count[0] += 1
    if count[0] > MAX_NODES or depth > MAX_DEPTH:
        raise ParamError("PARAMETER_LIMIT", "expression too complex")
    if isinstance(node, ast.Constant) and type(node.value) is int:
        if abs(node.value) > MAX_INT:
            raise ParamError("PARAMETER_LIMIT", "integer out of range")
        return sp.Integer(node.value)
    if isinstance(node, ast.UnaryOp) and isinstance(node.op, (ast.USub, ast.UAdd)):
        v = _walk(node.operand, depth + 1, count, in_sqrt)
        return -v if isinstance(node.op, ast.USub) else v
    if isinstance(node, ast.BinOp) and isinstance(node.op, (ast.Add, ast.Sub, ast.Mult, ast.Div)):
        left = _walk(node.left, depth + 1, count, in_sqrt)
        right = _walk(node.right, depth + 1, count, in_sqrt)
        if isinstance(node.op, ast.Add):
            return left + right
        if isinstance(node.op, ast.Sub):
            return left - right
        if isinstance(node.op, ast.Mult):
            return left * right
        if right == 0:
            raise ParamError("PARAMETER_LIMIT", "division by zero")
        return left / right
    if (isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id == "sqrt"
            and len(node.args) == 1 and not node.keywords):
        if in_sqrt:
            raise ParamError("PARAMETER_LIMIT", "nested radicals are not supported")
        rad = _walk(node.args[0], depth + 1, count, True)
        if not rad.is_Rational or rad < 0 or abs(rad) > MAX_RADICAND:
            raise ParamError("PARAMETER_LIMIT", "radicand must be a non-negative rational <= %d" % MAX_RADICAND)
        return sp.sqrt(rad)
    raise ParamError("PARAMETER_LIMIT", "unsupported syntax in parameter")


def exact_text(v):
    """Canonical text form used for hashing and display (round-trips through parse_exact)."""
    return sp.sstr(sp.radsimp(v))
