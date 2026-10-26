# -*- coding: utf-8 -*-
"""Plain-text (Unicode) and LaTeX renderings of exact values."""

import re

import sympy as sp


def latex(v):
    return sp.latex(sp.radsimp(v))


def plain(v):
    s = sp.sstr(sp.radsimp(v))
    s = re.sub(r"sqrt\((\d+)\)", r"√\1", s)
    s = re.sub(r"(\d)\*√", r"\1√", s)
    s = s.replace("*", "·")
    return s


def vec_plain(v):
    return "(" + ", ".join(plain(c) for c in v) + ")"


def vec_latex(v):
    return "\\left(" + ", ".join(latex(c) for c in v) + "\\right)"
