# -*- coding: utf-8 -*-
"""Versioned capability catalog (PRD §6.2, FR-M02).

admission:
  planned      not implemented, the UI must not offer automatic verification;
  dev          implemented, development regression only, NOT admitted;
  admitted     passed the frozen admission evaluation (PRD §17.2) for this catalog version.
Only "admitted" families may be shown as "supported with automatic verification".
"""

CATALOG_VERSION = "edu-math-catalog/2026.10.0"

LIMITS = {
    "param_text_max": 48,
    "param_ast_nodes_max": 16,
    "param_ast_depth_max": 6,
    "integer_abs_max": 1000000,
    "radicand_max": 10000,
    "solve_timeout_s": 10,
    "verify_timeout_s": 10,
    "render_timeout_s": 30,
    "worker_memory_mb": 512,
    "upload_max_bytes": 10 * 1024 * 1024,
    "upload_max_pixels": 8192,
}

FAMILIES = [
    {"id": "SOLID-VOL-01", "name": "长方体体积与单位", "admission": "planned",
     "params": {"l": "(0,1000]", "w": "(0,1000]", "h": "(0,1000]"}, "methods": ["formula"]},
    {"id": "SOLID-ANGLE-01", "name": "正方体 A1C 与底面 ABCD 所成角", "admission": "planned",
     "params": {"edge": "(0,100]"}, "methods": ["vector_coordinate", "synthetic"]},
    {"id": "SOLID-ANGLE-02", "name": "正四棱锥 BE 与平面 PAC 所成角正弦", "admission": "dev",
     "params": {"a": "(0,100] 整数/有理数/受限根式", "h": "(0,100] 整数/有理数/受限根式"},
     "units": ["", "mm", "cm", "dm", "m"],
     "methods": ["vector_coordinate", "synthetic"],
     "target": "sin_angle_BE_PAC",
     "notes": "E 为 PC 中点；点名必须为标准命名 P-ABCD；超出范围返回 PARAMETER_LIMIT。"},
    {"id": "CONIC-CHORD-01", "name": "椭圆焦点弦长取值范围", "admission": "planned",
     "params": {"a": "(0,100]", "b": "(0,a)"}, "methods": ["vieta"]},
    {"id": "CONIC-SLOPE-01", "name": "椭圆斜率积定值 k_PA·k_PB", "admission": "planned",
     "params": {"a": "(0,100]", "b": "(0,100]"}, "methods": ["point_substitution"]},
    {"id": "CONIC-PARABOLA-01", "name": "抛物线焦点弦 OA·OB 定值", "admission": "planned",
     "params": {"p": "(0,100]"}, "methods": ["vieta"]},
]


def catalog():
    return {"catalog_version": CATALOG_VERSION, "limits": LIMITS, "families": FAMILIES}


def family(fid):
    for f in FAMILIES:
        if f["id"] == fid:
            return f
    return None
