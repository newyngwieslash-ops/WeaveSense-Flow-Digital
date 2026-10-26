# -*- coding: utf-8 -*-
"""Structured lesson edits (FR-T05, FR-T06, AC-17, AC-18).

A teacher edits single fields of an existing lesson revision, never whole HTML or
code. Only wording, board lines, hint text and pronunciations are editable; the
problem, parameters, answer, figure and self-test belong to the problem revision
and change only through a new problem revision (which invalidates everything).

Each edit carries an impact class that decides what becomes stale:
  reasoning     (board lines)    review, student projection, preview, media; the
                                 edited lines are NOT automatically verified and are
                                 listed in quality.edited_unverified for the reviewer
  wording       (title, say, hint text)  review, student projection, preview, media
  pronunciation (term readings)  media and captions only; preview is unchanged
The math solution and its verification are kept: the stage re-solves the problem
and refuses the edit when the base lesson no longer matches (REVISION_CONFLICT).
"""
import copy
import hashlib
import json

from .errors import MathError
from . import lesson as L

MAX_EDITS = 50
MAX_TEXT = 600
MAX_BOARD_LINES = 12
MAX_LINE = 200

IMPACT = {
    "reasoning": ["review", "student_projection", "preview", "media"],
    "wording": ["review", "student_projection", "preview", "media"],
    "pronunciation": ["media", "captions"],
}


def artifact_sha(lesson):
    raw = json.dumps(lesson, ensure_ascii=False, sort_keys=True).encode("utf-8")
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def _text(v, limit=MAX_TEXT):
    if not isinstance(v, str):
        raise MathError("INPUT_AMBIGUOUS", "edit value must be text")
    v = "".join(ch for ch in v if ch == "\n" or ord(ch) >= 32).strip()
    if not v or len(v) > limit:
        raise MathError("INPUT_AMBIGUOUS", "edit text must be 1..%d characters" % limit)
    return v


def classify(edit):
    t = edit.get("target")
    if t == "beat":
        f = edit.get("field")
        if f == "board":
            return "reasoning"
        if f in ("title", "say"):
            return "wording"
    if t == "hint" and edit.get("field") == "text":
        return "wording"
    if t == "pronunciation":
        return "pronunciation"
    raise MathError("POLICY_REVOKED", "this part is not editable here; change the problem revision instead",
                    {"target": str(t)[:20], "field": str(edit.get("field"))[:20]})


def impact(edits):
    """What an edit list invalidates and which stages regenerate (dry run, no lesson needed)."""
    if not isinstance(edits, list) or not edits or len(edits) > MAX_EDITS:
        raise MathError("INPUT_AMBIGUOUS", "edits must be a list of 1..%d items" % MAX_EDITS)
    classes = sorted({classify(e) for e in edits})
    inval = sorted({x for c in classes for x in IMPACT[c]})
    regen = ["apply_edits", "project_student", "render_preview"]  # the student projection and previews always follow the new hash
    return {"classes": classes, "invalidates": inval, "keeps": ["problem", "solution", "verification", "figure", "check_question"],
            "regenerates": regen, "manual_check": "reasoning" in classes}


def apply(base, edits, sol):
    impact(edits)
    if base["answer"]["exact"] != sol["answer"]["exact"]:
        raise MathError("REVISION_CONFLICT", "base lesson does not match the verified solution")
    out = copy.deepcopy(base)
    beats = {b["id"]: b for b in out["beats"]}
    hints = {h["level"]: h for h in out["hints"]}
    pron = dict(out.get("pronunciations") or {})
    log, unverified = [], set(out.get("quality", {}).get("edited_unverified", []))
    for e in edits:
        cls = classify(e)
        if e["target"] == "beat":
            b = beats.get(e.get("id"))
            if b is None:
                raise MathError("INPUT_AMBIGUOUS", "unknown beat")
            if e["field"] == "board":
                lines = e.get("value")
                if not isinstance(lines, list) or not 1 <= len(lines) <= MAX_BOARD_LINES:
                    raise MathError("INPUT_AMBIGUOUS", "board must be 1..%d lines" % MAX_BOARD_LINES)
                b["board"] = [_text(x, MAX_LINE) for x in lines]
                b["board_latex"] = []  # the edited plain lines are authoritative; no stale LaTeX
                unverified.add(b["id"])
            else:
                b[e["field"]] = _text(e.get("value"))
            log.append({"class": cls, "target": "beat", "id": b["id"], "field": e["field"]})
        elif e["target"] == "hint":
            h = hints.get(e.get("level"))
            if h is None:
                raise MathError("INPUT_AMBIGUOUS", "unknown hint level")
            h["text"] = _text(e.get("value"))
            log.append({"class": cls, "target": "hint", "level": h["level"], "field": "text"})
        else:
            term = _text(e.get("term"), 20)
            reading = e.get("reading", "")
            if reading == "":
                pron.pop(term, None)
            else:
                pron[term] = _text(reading, 40)
            log.append({"class": cls, "target": "pronunciation", "term": term})
    if pron:
        out["pronunciations"] = pron
    else:
        out.pop("pronunciations", None)
    meta = out["meta"]
    meta["edited_by_teacher"] = True
    meta["parent_lesson_hash"] = artifact_sha(base)
    meta["edit_log"] = log
    q = out.setdefault("quality", {})
    q["pedagogy"] = "pending_teacher_review"
    q["edited_unverified"] = sorted(unverified)
    if unverified:
        q["math_text"] = "teacher_edited_not_auto_verified"  # constraint 4: never reported as automatically verified
    problems = L.presentation_check(out, sol)
    if problems:
        raise MathError("VALIDATION_FAILED", "presentation consistency failed", {"problems": problems})
    return out
