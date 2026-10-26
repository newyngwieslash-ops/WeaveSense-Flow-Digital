# -*- coding: utf-8 -*-
"""Lesson schema edu.lesson/1 and the student projection.

Structure inspired by edulab skills/edu-math-video/lesson (meta/scenes/beats/board),
rewritten as a versioned data contract; no executable expressions are allowed
anywhere in a lesson (figures are declared points/segments/faces only).
"""

import hashlib
import json

from .errors import MathError

SCHEMA = "edu.lesson/1"
COMPOSER_VERSION = "lesson-composer/1.0.0"


def canonical_hash(obj):
    raw = json.dumps(obj, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def compose(fam, norm, sol, report, cq, refs):
    if report["math_state"] != "verified":
        raise MathError("VALIDATION_FAILED", "lesson requires a verified solution", {"failed": report["failed_checks"]})
    beats = []
    for st in sol["steps"]:
        beats.append({"id": st["id"], "title": st["title"], "board": st.get("plain", []), "board_latex": st.get("latex", []),
                      "say": "".join(st.get("plain", [])), "figure_focus": st.get("uses", []),
                      "reveals_answer": bool(st.get("reveals_answer")), "solution_step_ref": st["id"]})
    hints = [
        {"level": 1, "kind": "locate", "text": "先找出直线 BE 与平面 PAC 的位置关系，想想 B 到平面 PAC 的垂足在哪里。", "reveals_answer": False},
        {"level": 2, "kind": "prerequisite", "text": "线面角的正弦等于直线方向向量与平面法向量夹角余弦的绝对值。", "reveals_answer": False},
        {"level": 3, "kind": "method", "text": "以底面中心为原点建立坐标系，写出 B、E 的坐标，并找平面 PAC 的一个法向量。", "reveals_answer": False},
        {"level": 4, "kind": "partial_step", "text": "；".join(sol["steps"][1].get("plain", [])) if len(sol["steps"]) > 1 else "", "reveals_answer": False, "gated": True},
        {"level": 5, "kind": "full_solution", "text": "完整解答见讲解。", "reveals_answer": True, "gated": True},
    ]
    lesson = {
        "schema_version": SCHEMA, "composer_version": COMPOSER_VERSION,
        "meta": {"family_id": fam, "title": "正四棱锥中线面角的正弦", "language": "zh-CN", "style": "standard",
                 "ai_generated": False, "generator": "deterministic-template", "refs": refs},
        "problem": {"text": fam_text(norm), "params": norm["params"], "unit": norm["unit"]},
        "figure": figure_of(sol),
        "beats": beats,
        "answer": {"plain": sol["answer"]["plain"], "latex": sol["answer"]["latex"], "exact": sol["answer"]["exact"],
                   "release": "after_teacher_release"},
        "hints": hints,
        "check_question": cq["question"],
        "check_answer_key": cq["answer_key"],
        "quality": {"math": report["math_state"], "pedagogy": "pending_teacher_review", "presentation": "auto_checked"},
    }
    problems = presentation_check(lesson, sol)
    if problems:
        raise MathError("VALIDATION_FAILED", "presentation consistency failed", {"problems": problems})
    return lesson


def fam_text(norm):
    from .families import solid_angle_02
    return solid_angle_02.problem_text(norm)


def figure_of(sol):
    from .families import solid_angle_02
    return solid_angle_02.figure(sol)


def presentation_check(lesson, sol):
    """Automated presentation checks: labels exist, every beat has board + narration, answer consistent."""
    problems = []
    pts = lesson["figure"]["points"]
    for e in lesson["figure"]["edges"] + [a["seg"] for a in lesson["figure"]["aux"]]:
        for p in e:
            if p not in pts:
                problems.append("figure references unknown point %s" % p)
    for b in lesson["beats"]:
        if not b["board"] or not b["say"]:
            problems.append("beat %s missing board or narration" % b["id"])
    if lesson["answer"]["exact"] != sol["answer"]["exact"]:
        problems.append("answer differs from solution")
    if not any(b["reveals_answer"] for b in lesson["beats"]):
        problems.append("no beat is marked as revealing the answer")
    return problems


def answer_markers(lesson):
    a = lesson["answer"]
    k = lesson.get("check_answer_key") or {}
    out = {a["plain"], a["latex"], a["exact"], k.get("plain", ""), k.get("latex", ""), k.get("exact", "")}
    return sorted(m for m in out if m and len(m) >= 2)


def student_projection(lesson, reveal="none"):
    """Server-side projection for students (PRD constraint 2). reveal=none keeps
    only the problem, the unlabeled figure, and ungated hints; nothing that
    reveals the answer or the check-question key is included."""
    if reveal not in ("none", "released"):
        raise MathError("POLICY_REVOKED", "unknown reveal policy")
    out = {
        "schema_version": SCHEMA, "projection": "student", "reveal": reveal,
        "meta": {k: lesson["meta"][k] for k in ("family_id", "title", "language", "style")},
        "problem": lesson["problem"], "figure": lesson["figure"],
        "hints": [{"level": h["level"], "kind": h["kind"], "text": h["text"]} for h in lesson["hints"] if not h.get("gated")],
        "gated_hint_levels": [h["level"] for h in lesson["hints"] if h.get("gated")],
        "check_question": lesson["check_question"],
    }
    if reveal == "released":
        out["beats"] = lesson["beats"]
        out["answer"] = lesson["answer"]
    else:
        out["beats"] = [{"id": b["id"], "title": b["title"], "locked": True} for b in lesson["beats"]]
    guard(out, answer_markers(lesson) if reveal == "none" else [lesson["check_answer_key"]["plain"], lesson["check_answer_key"]["exact"]])
    return out


def _strings(obj):
    if isinstance(obj, str):
        yield obj
    elif isinstance(obj, dict):
        for k, v in obj.items():
            yield str(k)
            yield from _strings(v)
    elif isinstance(obj, (list, tuple)):
        for v in obj:
            yield from _strings(v)


def guard(obj, markers):
    texts = list(_strings(obj))
    leaked = [m for m in markers if m and any(m in t for t in texts)]
    if leaked:
        raise MathError("VALIDATION_FAILED", "student projection would leak answer content", {"markers": len(leaked)})
