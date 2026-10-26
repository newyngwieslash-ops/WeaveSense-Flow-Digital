# -*- coding: utf-8 -*-
"""Stage functions. Each runs inside an isolated subprocess (see runner.py):
input is JSON on stdin, output JSON on stdout. Pure functions of their input."""

from . import catalog, lesson as L, render
from .errors import MathError
from .families import REGISTRY as FAMILIES

ANALYZE_STAGES = ["validate_input", "solve", "verify"]
LESSON_STAGES = ["compose_lesson", "project_student", "render_preview"]


def _fam(problem):
    fid = problem.get("family_id")
    mod = FAMILIES.get(fid)
    if mod is None:
        known = catalog.family(fid)
        if known:
            raise MathError("UNSUPPORTED_FAMILY", "family %s is %s in %s, not implemented yet" % (fid, known["admission"], catalog.CATALOG_VERSION))
        raise MathError("UNSUPPORTED_FAMILY", "unknown problem family")
    return mod


def _public(d):
    return {k: v for k, v in d.items() if not k.startswith("_")}


def run_stage(stage, payload, prior):
    """prior: outputs of earlier stages of the same job (checkpoint)."""
    if stage == "grade":
        from .grade import grade
        return grade(payload)
    problem = payload["problem"]
    mod = _fam(problem)
    if stage == "validate_input":
        n = mod.normalize(problem)
        return {"normalized": _public(n)}
    n = mod.normalize(problem)  # deterministic; recomputed instead of trusting serialized exact values
    if stage == "solve":
        return {"solution": _public(mod.solve(n))}
    if stage == "verify":
        sol = mod.solve(n)
        if sol["answer"]["exact"] != prior["solve"]["solution"]["answer"]["exact"]:
            raise MathError("VALIDATION_FAILED", "re-solve differs from checkpointed solution")
        return {"report": mod.verify(n, sol)}
    sol = mod.solve(n)
    src = payload["analysis"]
    if src["solution"]["answer"]["exact"] != sol["answer"]["exact"]:
        raise MathError("REVISION_CONFLICT", "analysis solution does not match the problem revision")
    if stage == "compose_lesson":
        cq = mod.check_question(n, avoid=sol["answer"]["exact"])
        refs = {"problem_hash": payload.get("problem_hash", ""), "solution_hash": payload.get("solution_hash", ""),
                "report_hash": payload.get("report_hash", ""), "catalog_version": catalog.CATALOG_VERSION}
        lesson = L.compose(problem["family_id"], n, sol, src["report"], cq, refs)
        return {"lesson": lesson, "lesson_hash": L.canonical_hash(lesson)}
    if stage == "project_student":
        lesson = prior["compose_lesson"]["lesson"]
        return {"student": L.student_projection(lesson, "none")}
    if stage == "render_preview":
        lesson = prior["compose_lesson"]["lesson"]
        student = prior["project_student"]["student"]
        t_html = render.page(lesson, "teacher")
        s_html = render.page(student, "student")
        L.guard({"html": L.visible_text(s_html)}, L.answer_markers(lesson))
        return {"teacher_html": t_html, "student_html": s_html, "renderer": render.RENDERER_VERSION}
    raise MathError("INTERNAL", "unknown stage")
