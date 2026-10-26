# -*- coding: utf-8 -*-
"""Silent preview renderer: a self-contained HTML page with static SVG views.

No scripts, no external URLs (CDN), no file:// paths and no fonts are embedded;
the page relies on system CJK fonts. Served by the platform with
Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; img-src data:.
"""

import html
import math

RENDERER_VERSION = "preview-svg/1.0.0"
VIEWS = [(35, 20), (75, 20), (130, 25)]


def _project(xyz, az, el, scale, cx, cy):
    x, y, z = xyz
    a, e = math.radians(az), math.radians(el)
    xr = x * math.cos(a) - y * math.sin(a)
    yr = x * math.sin(a) + y * math.cos(a)
    sx = xr
    sy = z * math.cos(e) - yr * math.sin(e)
    return cx + sx * scale, cy - sy * scale


def svg(fig, az, el, w=360, h=300, labels=True):
    pts = fig["points"]
    span = max(max(abs(c) for c in p["xyz"]) for p in pts.values()) or 1
    scale = (min(w, h) * 0.36) / span
    P = {k: _project(v["xyz"], az, el, scale, w / 2, h / 2) for k, v in pts.items()}
    out = ['<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" role="img" aria-label="立体图形">' % (w, h)]
    for f in fig.get("faces", []):
        poly = " ".join("%.1f,%.1f" % P[p] for p in f["pts"])
        out.append('<polygon points="%s" fill="#3b82f6" fill-opacity="0.12" stroke="none"/>' % poly)
    for a, b in fig["edges"]:
        out.append('<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="#1f2937" stroke-width="1.6"/>' % (P[a] + P[b]))
    for ax in fig.get("aux", []):
        a, b = ax["seg"]
        dash = "6 4" if ax["style"] == "dashed" else "2 4"
        color = "#dc2626" if ax["role"] == "line" else "#6b7280"
        out.append('<line x1="%.1f" y1="%.1f" x2="%.1f" y2="%.1f" stroke="%s" stroke-width="1.4" stroke-dasharray="%s"/>' % (P[a] + P[b] + (color, dash)))
    if labels:
        for k, (x, y) in P.items():
            out.append('<circle cx="%.1f" cy="%.1f" r="2.4" fill="#111827"/><text x="%.1f" y="%.1f" font-size="14">%s</text>' % (x, y, x + 5, y - 5, html.escape(k)))
    out.append("</svg>")
    return "".join(out)


CSS = """body{font-family:"PingFang SC","Noto Sans CJK SC","Microsoft YaHei",sans-serif;margin:0;background:#f8fafc;color:#111827}
main{max-width:980px;margin:0 auto;padding:24px}header{display:flex;justify-content:space-between;align-items:baseline}
.badge{font-size:12px;padding:2px 8px;border-radius:10px;background:#fef3c7}.grid{display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr);gap:20px}
@media(max-width:720px){.grid{grid-template-columns:1fr}}section{background:#fff;border:1px solid #e5e7eb;border-radius:10px;padding:16px;margin-bottom:16px}
.views input{display:none}.views svg{display:none;width:100%;height:auto}.views label{display:inline-block;margin:0 6px 8px 0;padding:4px 10px;border:1px solid #cbd5e1;border-radius:6px;cursor:pointer}
#v0:checked~.f0,#v1:checked~.f1,#v2:checked~.f2{display:block}.beat h3{margin:0 0 6px;font-size:16px}.beat p{margin:4px 0;line-height:1.7}
.locked{color:#6b7280}.answer{font-weight:600;background:#ecfdf5;padding:8px;border-radius:6px}"""


def page(lesson, audience):
    esc = html.escape
    fig = lesson["figure"]
    views = ['<div class="views">']
    for i, _ in enumerate(VIEWS):
        views.append('<input type="radio" name="v" id="v%d"%s>' % (i, " checked" if i == 0 else ""))
    for i, _ in enumerate(VIEWS):
        views.append('<label for="v%d">视角 %d</label>' % (i, i + 1))
    for i, (az, el) in enumerate(VIEWS):
        views.append(svg(fig, az, el).replace("<svg ", '<svg class="f%d" ' % i, 1))
    views.append("</div>")
    beats = []
    for b in lesson["beats"]:
        if b.get("locked"):
            beats.append('<div class="beat locked"><h3>%s</h3><p>（教师开放后可见）</p></div>' % esc(b["title"]))
        else:
            beats.append('<div class="beat"><h3>%s</h3>%s</div>' % (esc(b["title"]), "".join("<p>%s</p>" % esc(t) for t in b["board"])))
    hints = "".join("<li>提示 %d：%s</li>" % (h["level"], esc(h["text"])) for h in lesson.get("hints", []) if not h.get("gated"))
    ans = ""
    if lesson.get("answer"):
        ans = '<p class="answer">答案：%s</p>' % esc(lesson["answer"]["plain"])
    badge = "教师预览（静音）" if audience == "teacher" else "学生视图（静音）"
    return ("<!doctype html><html lang=\"zh-CN\"><head><meta charset=\"utf-8\">"
            "<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; style-src 'unsafe-inline'; img-src data:\">"
            "<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\"><title>%s</title><style>%s</style></head><body><main>"
            "<header><h1>%s</h1><span class=\"badge\">%s</span></header>"
            "<section><p>%s</p></section><div class=\"grid\"><section>%s</section><section>%s%s</section></div>"
            "<section><h2>分级提示</h2><ol>%s</ol></section><footer><small>renderer %s · 无配音 · 无外部资源</small></footer></main></body></html>") % (
        esc(lesson["meta"]["title"]), CSS, esc(lesson["meta"]["title"]), badge, esc(lesson["problem"]["text"]),
        "".join(views), "".join(beats), ans, hints, RENDERER_VERSION)
