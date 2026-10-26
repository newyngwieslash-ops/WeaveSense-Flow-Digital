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
    {"id": "SOLID-VOL-01", "name": "长方体体积与单位换算", "admission": "dev",
     "params": {"l": "(0,1000]", "w": "(0,1000]", "h": "(0,1000]"}, "units": ["", "mm", "cm", "dm", "m"],
     "methods": ["formula"], "target": "volume", "solver": "solid-vol-01/1.0.0",
     "notes": "三条棱两两垂直；可逐棱给单位（units），单位不同时必须给 target_unit；不含斜棱柱、缺边长。"},
    {"id": "SOLID-ANGLE-01", "name": "正方体 A1C 与底面 ABCD 所成角", "admission": "dev",
     "params": {"e": "(0,100]"}, "units": ["", "mm", "cm", "dm", "m"], "methods": ["vector_coordinate", "synthetic"],
     "target": "sin_angle_A1C_ABCD", "solver": "solid-angle-01/1.0.0",
     "notes": "只支持 A1C 与底面 ABCD；余弦、正切作为派生值；不泛化为任意线面角。"},
    {"id": "SOLID-ANGLE-02", "name": "正四棱锥 BE 与平面 PAC 所成角正弦", "admission": "dev",
     "params": {"a": "(0,100] 整数/有理数/受限根式", "h": "(0,100] 整数/有理数/受限根式"},
     "units": ["", "mm", "cm", "dm", "m"],
     "methods": ["vector_coordinate", "synthetic"],
     "target": "sin_angle_BE_PAC", "solver": "solid-angle-02/1.0.0",
     "notes": "E 为 PC 中点；点名必须为标准命名 P-ABCD；超出范围返回 PARAMETER_LIMIT。"},
    {"id": "CONIC-CHORD-01", "name": "椭圆右焦点弦长取值范围与端点构型", "admission": "dev",
     "params": {"a": "(0,100]", "b": "(0,a)"}, "methods": ["vieta"], "target": "focal_chord_length_range",
     "solver": "conic-chord-01/1.0.0",
     "notes": "x²/a²+y²/b²=1，a>b>0，只支持右焦点；竖直弦单独检查；水平弦（最大）与竖直弦（最小）均可取到。"},
    {"id": "CONIC-SLOPE-01", "name": "椭圆斜率积定值 k_PA·k_PB", "admission": "dev",
     "params": {"a": "(0,100]", "b": "(0,a)"}, "methods": ["point_substitution"], "target": "slope_product_PA_PB",
     "solver": "conic-slope-01/1.0.0",
     "notes": "P 为右顶点 (a,0)，A、B 关于原点对称；排除 A/B 为左右顶点，禁用点在图形中标出。"},
    {"id": "CONIC-PARABOLA-01", "name": "抛物线焦点弦 OA·OB 定值", "admission": "dev",
     "params": {"p": "(0,100]"}, "methods": ["vieta"], "target": "dot_OA_OB", "solver": "conic-parabola-01/1.0.0",
     "notes": "教材约定 y²=2px，焦点 (p/2,0)；排除轴向直线 y=0；两有限实交点由判别式保证。"},
]


def catalog():
    return {"catalog_version": CATALOG_VERSION, "limits": LIMITS, "families": FAMILIES}


def family(fid):
    for f in FAMILIES:
        if f["id"] == fid:
            return f
    return None
