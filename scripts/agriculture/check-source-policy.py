"""Build gate for the reviewed deployment source configuration, not user input."""
import argparse
import importlib.util
import json
from pathlib import Path
import sys

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location("source_rights", ROOT / "contracts/agriculture/source-rights.py")
rights = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rights)


def check(config, *, profile, now=None):
    if profile != "agriculture":
        raise rights.RightsDenied("EXPLICIT_AGRICULTURE_PROFILE_REQUIRED")
    if (not isinstance(config, dict) or set(config) != {"product_profile", "requested_uses", "registry"}
            or config["product_profile"] != profile):
        raise rights.RightsDenied("INVALID_DEPLOYMENT_SOURCE_CONFIG")
    sources = rights.validate_registry(config["registry"])
    requested = config["requested_uses"]
    if not isinstance(requested, dict):
        raise rights.RightsDenied("INVALID_REQUESTED_USES")
    enabled = {key for key, source in sources.items() if source["enabled"]}
    if set(requested) != enabled:
        raise rights.RightsDenied("ENABLED_SOURCE_SELECTION_MISMATCH")
    checks = []
    for source_id in sorted(enabled):
        uses = requested[source_id]
        if (not isinstance(uses, list) or not uses or any(not isinstance(use, str) for use in uses)
                or len(uses) != len(set(uses)) or not set(uses).issubset(rights.USES)):
            raise rights.RightsDenied("EXPLICIT_KNOWN_PURPOSES_REQUIRED")
        for purpose in sorted(uses):
            checks.append(rights.authorize(config["registry"], profile="production", purpose=purpose,
                                           source_ids=[source_id], now=now))
    return {"allowed": True, "product_profile": profile, "enabled_source_count": len(enabled),
            "external_sources_disabled": not enabled, "checks": checks,
            "runtime_enforcement_verified": False, "production_ready": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", required=True, choices=["agriculture"])
    parser.add_argument("--config", type=Path, default=ROOT / "deploy/agriculture/sources.json",
                        help="Operator-reviewed deployment file; never an HTTP or LLM payload")
    args = parser.parse_args()
    try:
        config = json.loads(args.config.read_text(), object_pairs_hook=rights.reject_duplicate_keys)
        result = check(config, profile=args.profile)
    except rights.RightsDenied as exc:
        result = {"allowed": False, "reason": str(exc)}
    except (OSError, ValueError):
        result = {"allowed": False, "reason": "UNREADABLE_DEPLOYMENT_SOURCE_CONFIG"}
    print(json.dumps(result, ensure_ascii=False))
    return 0 if result["allowed"] else 1


if __name__ == "__main__":
    sys.exit(main())
