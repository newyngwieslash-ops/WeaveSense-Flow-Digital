"""Fail-closed release/operation gate over an operator-reviewed registry.

No network, credential handling, license inference, or self-approval. Callers must
load this registry from trusted storage, not from browser/LLM request parameters.
"""
import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import sys

from jsonschema import Draft202012Validator, FormatChecker

SCHEMA = Path(__file__).with_suffix(".schema.json")
USES = {"ingest", "analysis", "report_publish", "export", "external_share", "model_training"}


class RightsDenied(ValueError):
    """Machine-readable denial only; no source contents echoed."""


def timestamp(value):
    try:
        result = datetime.fromisoformat(value.replace("Z", "+00:00").replace("z", "+00:00"))
    except (TypeError, ValueError) as exc:
        raise RightsDenied("INVALID_RIGHTS_TIMESTAMP") from exc
    if result.tzinfo is None:
        raise RightsDenied("TIMESTAMP_REQUIRES_TIMEZONE")
    return result


def validate_registry(manifest):
    schema = json.loads(SCHEMA.read_text())
    # jsonschema's RFC3339 checker is an optional extra. Always enforce dates
    # locally as well, so installing fewer extras cannot relax this gate.
    checker = FormatChecker()

    @checker.checks("date-time", raises=RightsDenied)
    def valid_datetime(value):
        return not isinstance(value, str) or timestamp(value) is not None

    validator = Draft202012Validator(schema, format_checker=checker)
    if next(validator.iter_errors(manifest), None) is not None:
        raise RightsDenied("INVALID_RIGHTS_SCHEMA")
    sources = {}
    for source in manifest["sources"]:
        if source["id"] in sources:
            raise RightsDenied("DUPLICATE_SOURCE_ID")
        sources[source["id"]] = source
    visiting, visited = set(), set()

    def visit(source_id):
        if source_id not in sources:
            raise RightsDenied("UNKNOWN_LINEAGE_PARENT")
        if source_id in visiting:
            raise RightsDenied("CYCLIC_LINEAGE")
        if source_id in visited:
            return
        visiting.add(source_id)
        source = sources[source_id]
        if source["kind"] == "derived" and not source["parent_ids"]:
            raise RightsDenied("DERIVED_SOURCE_REQUIRES_LINEAGE")
        if source["kind"] == "fixture" and source["data_class"] != "demo":
            raise RightsDenied("FIXTURE_REQUIRES_DEMO")
        for parent in source["parent_ids"]:
            visit(parent)
        visiting.remove(source_id)
        visited.add(source_id)

    for source_id in sources:
        visit(source_id)
    return sources


def authorize(manifest, *, profile, purpose, source_ids, now=None):
    """Validate ALL selected inputs and ancestors; return attribution obligations.

    'production' is the commercial rights policy, not a deployment permission.
    Missing/empty selection is refused, rather than interpreted as no restriction.
    """
    if profile not in {"demo", "production"} or purpose not in USES:
        raise RightsDenied("UNKNOWN_PROFILE_OR_PURPOSE")
    if not isinstance(source_ids, (list, tuple)) or not source_ids:
        raise RightsDenied("EXPLICIT_SOURCES_REQUIRED")
    if any(not isinstance(item, str) for item in source_ids):
        raise RightsDenied("INVALID_SOURCE_SELECTION")
    sources = validate_registry(manifest)
    now = now if now is not None else datetime.now(timezone.utc)
    if now.tzinfo is None:
        raise RightsDenied("TIMESTAMP_REQUIRES_TIMEZONE")
    checked, obligations = set(), []

    def check(source_id):
        if source_id in checked:
            return
        source = sources.get(source_id)
        if source is None:
            raise RightsDenied("UNKNOWN_SOURCE")
        rights = source["rights"]
        if not source["enabled"]:
            raise RightsDenied("SOURCE_DISABLED")
        if source["data_class"] != ("operational" if profile == "production" else "demo"):
            raise RightsDenied("DATA_CLASS_MISMATCH")
        if rights["status"] != "approved":
            raise RightsDenied("RIGHTS_NOT_APPROVED")
        if not all([rights["owner"], rights["license_ref"], rights["review_ref"], rights["evidence"], rights["valid_from"]]):
            raise RightsDenied("RIGHTS_EVIDENCE_INCOMPLETE")
        start = timestamp(rights["valid_from"])
        expiry = timestamp(rights["expiry"]) if rights["expiry"] else None
        if now < start or (expiry is not None and (expiry <= start or now >= expiry)):
            raise RightsDenied("RIGHTS_OUTSIDE_VALIDITY")
        if purpose not in rights["permitted_uses"]:
            raise RightsDenied("PURPOSE_NOT_PERMITTED")
        if profile == "production" and rights["commercial_use"] != "allowed":
            raise RightsDenied("COMMERCIAL_USE_NOT_ALLOWED")
        if purpose in {"export", "external_share"} and rights["redistribution"] != "allowed":
            raise RightsDenied("REDISTRIBUTION_NOT_ALLOWED")
        attribution = rights["attribution"]
        if attribution["required"]:
            if not attribution["text"]:
                raise RightsDenied("ATTRIBUTION_MISSING")
            obligations.append({"source_id": source_id, "text": attribution["text"]})
        for parent in source["parent_ids"]:
            check(parent)
        checked.add(source_id)

    for source_id in source_ids:
        check(source_id)
    return {"allowed": True, "profile": profile, "purpose": purpose,
            "checked_source_ids": sorted(checked), "attributions": obligations}


def reject_duplicate_keys(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise RightsDenied("DUPLICATE_JSON_KEY")
        result[key] = value
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("manifest", type=Path)
    parser.add_argument("--profile", required=True, choices=["demo", "production"])
    parser.add_argument("--purpose", required=True, choices=sorted(USES))
    parser.add_argument("--source", action="append", required=True)
    args = parser.parse_args()
    try:
        manifest = json.loads(args.manifest.read_text(), object_pairs_hook=reject_duplicate_keys)
        result = authorize(manifest, profile=args.profile, purpose=args.purpose, source_ids=args.source)
    except RightsDenied as exc:
        result = {"allowed": False, "reason": str(exc)}
    except (OSError, ValueError):
        result = {"allowed": False, "reason": "UNREADABLE_RIGHTS_REGISTRY"}
    print(json.dumps(result, ensure_ascii=False))
    return 0 if result["allowed"] else 1


if __name__ == "__main__":
    sys.exit(main())
