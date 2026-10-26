# -*- coding: utf-8 -*-
"""Registry of implemented problem families (see catalog.py for admission state)."""

from . import conic_chord_01, conic_parabola_01, conic_slope_01, solid_angle_01, solid_angle_02, solid_vol_01

REGISTRY = {m.FAMILY_ID: m for m in (solid_vol_01, solid_angle_01, solid_angle_02, conic_chord_01, conic_slope_01, conic_parabola_01)}
