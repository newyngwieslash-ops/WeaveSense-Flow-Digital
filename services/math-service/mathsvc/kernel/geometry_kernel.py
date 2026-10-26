# -*- coding: utf-8 -*-
"""Exact solid-geometry kernel (sympy).

Adapted from edulab skills/edu-solid-geometry/lib/geometry_kernel.py
(Apache-2.0, Copyright 2026 WY (@akokoi1), baseline 805fdc5).
Modifications by WeaveSense-Flow (2026-10):
  * kept only vector helpers and the box / cube / regular square pyramid builders;
  * removed the random problem generator, is_clean() and to_three();
  * V() and the builders only accept sympy numbers or Python ints and never
    sympify text, so untrusted strings cannot reach sympy's parser (eval).
"""

import sympy as sp

sqrt = sp.sqrt


def _num(c):
    if isinstance(c, bool) or isinstance(c, str):
        raise TypeError("kernel accepts exact numbers only")
    if isinstance(c, int):
        return sp.Integer(c)
    if isinstance(c, sp.Expr):
        return c
    raise TypeError("kernel accepts exact numbers only")


def V(*comps):
    """Column vector from exact components."""
    if len(comps) == 1 and isinstance(comps[0], (list, tuple)):
        comps = comps[0]
    return sp.Matrix([_num(c) for c in comps])


def midpoint(a, b):
    return (a + b) / 2


def normal_from_points(p, q, r):
    """Normal of the plane through p, q, r (cross product, not simplified)."""
    return (q - p).cross(r - p)


def simplify_vec(v):
    """Reduce a vector to a primitive direction when its entries are rational multiples."""
    v = sp.Matrix([sp.simplify(c) for c in v])
    nonzero = [c for c in v if c != 0]
    if not nonzero:
        return v
    g = nonzero[0]
    for c in nonzero[1:]:
        g = sp.gcd(g, c)
    if g != 0:
        cand = sp.simplify(v / g)
        if all(c.is_rational for c in cand):
            return cand
    return v


def norm(v):
    return sp.sqrt(sum(c ** 2 for c in v))


def line_plane_angle_sin(line_dir, normal):
    """sin(theta) = |v.n| / (|v||n|), exact."""
    s = sp.Abs(line_dir.dot(normal)) / (norm(line_dir) * norm(normal))
    return sp.radsimp(sp.simplify(s))


def tex(expr):
    return sp.latex(sp.radsimp(sp.simplify(expr)))


def tex_vec(v):
    return "(" + ", ".join(sp.latex(sp.radsimp(sp.simplify(c))) for c in v) + ")"


def regular_quad_pyramid(base_edge, height):
    """Regular square pyramid P-ABCD, base centre O at origin, AC on x, BD on y, P on z."""
    a = _num(base_edge)
    h = _num(height)
    d = sp.radsimp(a / sqrt(2))
    return {
        "O": V(0, 0, 0),
        "A": V(d, 0, 0),
        "C": V(-d, 0, 0),
        "B": V(0, d, 0),
        "D": V(0, -d, 0),
        "P": V(0, 0, h),
    }


def cuboid(lx, ly, lz):
    """Cuboid ABCD-A1B1C1D1, A at origin, AB on x, AD on y, AA1 on z."""
    lx, ly, lz = _num(lx), _num(ly), _num(lz)
    return {
        "A": V(0, 0, 0), "B": V(lx, 0, 0), "C": V(lx, ly, 0), "D": V(0, ly, 0),
        "A1": V(0, 0, lz), "B1": V(lx, 0, lz), "C1": V(lx, ly, lz), "D1": V(0, ly, lz),
    }


def cube(edge):
    return cuboid(edge, edge, edge)
