"""Synthetic policy tests, not evidence of any actual commercial license."""
import copy
from datetime import datetime, timezone
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "contracts/agriculture/source-rights.py"
spec = importlib.util.spec_from_file_location("source_rights", SCRIPT)
rights = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rights)
NOW = datetime(2026, 10, 29, tzinfo=timezone.utc)
gate_spec = importlib.util.spec_from_file_location("source_policy", ROOT / "scripts/agriculture/check-source-policy.py")
gate = importlib.util.module_from_spec(gate_spec)
gate_spec.loader.exec_module(gate)


def registry():
    return {"schema_version": "1.0.0", "sources": [{
        "id": "synthetic-input", "kind": "imagery", "enabled": True,
        "data_class": "operational", "source_ref": "synthetic-only", "parent_ids": [],
        "rights": {
            "status": "approved", "owner": "UNIT TEST ONLY", "license_ref": "synthetic-license",
            "commercial_use": "allowed", "redistribution": "allowed",
            "permitted_uses": sorted(rights.USES),
            "attribution": {"required": True, "text": "SYNTHETIC TEST ONLY"},
            "valid_from": "2026-01-01T00:00:00Z", "expiry": "2027-01-01T00:00:00Z",
            "review_ref": "synthetic-review", "evidence": [{"ref": "synthetic-evidence", "sha256": "a" * 64}]
        }}]}


class SourceRightsTest(unittest.TestCase):
    def gate(self, doc, **kw):
        args = dict(profile="production", purpose="analysis", source_ids=["synthetic-input"], now=NOW)
        args.update(kw)
        return rights.authorize(doc, **args)

    def deny(self, doc, code, **kw):
        with self.assertRaisesRegex(rights.RightsDenied, "^" + code + "$"):
            self.gate(doc, **kw)

    def test_approved_exact_use_returns_attribution(self):
        out = self.gate(registry())
        self.assertTrue(out["allowed"])
        self.assertEqual(out["attributions"][0]["text"], "SYNTHETIC TEST ONLY")

    def test_unknown_prohibited_revoked_and_disabled(self):
        for status in ["unknown", "prohibited", "revoked"]:
            with self.subTest(status=status):
                doc = registry()
                doc["sources"][0]["rights"]["status"] = status
                self.deny(doc, "RIGHTS_NOT_APPROVED")
        doc = registry()
        doc["sources"][0]["enabled"] = False
        self.deny(doc, "SOURCE_DISABLED")

    def test_missing_license_evidence_and_review(self):
        for field, value in [("license_ref", None), ("owner", None), ("review_ref", None), ("evidence", []), ("valid_from", None)]:
            with self.subTest(field=field):
                doc = registry()
                doc["sources"][0]["rights"][field] = value
                self.deny(doc, "RIGHTS_EVIDENCE_INCOMPLETE")

    def test_expiry_boundary_future_start_and_revocation_rechecked(self):
        doc = registry()
        doc["sources"][0]["rights"]["expiry"] = NOW.isoformat()
        self.deny(doc, "RIGHTS_OUTSIDE_VALIDITY")
        doc = registry()
        doc["sources"][0]["rights"]["valid_from"] = "2026-10-30T00:00:00Z"
        self.deny(doc, "RIGHTS_OUTSIDE_VALIDITY")
        doc = registry()
        self.gate(doc)
        doc["sources"][0]["rights"]["status"] = "revoked"
        self.deny(doc, "RIGHTS_NOT_APPROVED")

    def test_each_use_is_independent(self):
        for purpose in rights.USES - {"ingest"}:
            with self.subTest(purpose=purpose):
                doc = registry()
                doc["sources"][0]["rights"]["permitted_uses"] = ["ingest"]
                self.deny(doc, "PURPOSE_NOT_PERMITTED", purpose=purpose)

    def test_commercial_and_redistribution_unknown_or_prohibited(self):
        for value in ["unknown", "prohibited"]:
            doc = registry()
            doc["sources"][0]["rights"]["commercial_use"] = value
            self.deny(doc, "COMMERCIAL_USE_NOT_ALLOWED")
            doc = registry()
            doc["sources"][0]["rights"]["redistribution"] = value
            for purpose in ["export", "external_share"]:
                self.deny(doc, "REDISTRIBUTION_NOT_ALLOWED", purpose=purpose)

    def test_attribution_required(self):
        doc = registry()
        doc["sources"][0]["rights"]["attribution"]["text"] = None
        self.deny(doc, "ATTRIBUTION_MISSING")

    def test_demo_never_promoted_to_operational(self):
        doc = registry()
        source = doc["sources"][0]
        source["kind"], source["data_class"] = "fixture", "demo"
        self.deny(doc, "DATA_CLASS_MISMATCH")
        self.assertTrue(self.gate(doc, profile="demo")["allowed"])
        source["data_class"] = "operational"
        self.deny(doc, "FIXTURE_REQUIRES_DEMO")
        self.deny(registry(), "DATA_CLASS_MISMATCH", profile="demo")

    def test_unknown_empty_and_malformed_selection(self):
        self.deny(registry(), "UNKNOWN_SOURCE", source_ids=["missing"])
        self.deny(registry(), "EXPLICIT_SOURCES_REQUIRED", source_ids=[])
        self.deny(registry(), "INVALID_SOURCE_SELECTION", source_ids=[True])
        self.deny(registry(), "UNKNOWN_PROFILE_OR_PURPOSE", purpose="anything")

    def test_strict_schema_rejects_unknown_fields_wrong_types_and_unsigned_urls(self):
        for field, value in [("enabled", "false"), ("unexpected", True), ("source_ref", "https://host/?token=secret")]:
            doc = registry()
            doc["sources"][0][field] = value
            self.deny(doc, "INVALID_RIGHTS_SCHEMA")
        doc = registry()
        del doc["sources"][0]["rights"]["license_ref"]
        self.deny(doc, "INVALID_RIGHTS_SCHEMA")

    def test_derived_rights_cannot_launder_parent(self):
        doc = registry()
        child = copy.deepcopy(doc["sources"][0])
        child.update(id="derived", kind="derived", parent_ids=["synthetic-input"])
        doc["sources"].append(child)
        self.assertEqual(len(self.gate(doc, source_ids=["derived"])["checked_source_ids"]), 2)
        doc["sources"][0]["rights"]["status"] = "revoked"
        self.deny(doc, "RIGHTS_NOT_APPROVED", source_ids=["derived"])

    def test_lineage_unknown_cycle_and_duplicate(self):
        doc = registry()
        doc["sources"][0]["parent_ids"] = ["missing"]
        self.deny(doc, "UNKNOWN_LINEAGE_PARENT")
        doc["sources"][0]["parent_ids"] = ["synthetic-input"]
        self.deny(doc, "CYCLIC_LINEAGE")
        doc = registry()
        doc["sources"].append(copy.deepcopy(doc["sources"][0]))
        self.deny(doc, "DUPLICATE_SOURCE_ID")

    def test_template_disabled_and_no_implicit_license(self):
        template = json.loads((ROOT / "contracts/agriculture/source-rights.template.json").read_text())
        rights.validate_registry(template)
        for source in template["sources"]:
            self.deny(template, "SOURCE_DISABLED", source_ids=[source["id"]])

    def test_cli_missing_license_denies_without_echoing_private_fields(self):
        doc = registry()
        doc["sources"][0]["rights"].update(license_ref=None, owner="DO-NOT-LOG-THIS")
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "registry.json"
            path.write_text(json.dumps(doc))
            run = subprocess.run([sys.executable, str(SCRIPT), str(path), "--profile", "production", "--purpose", "analysis", "--source", "synthetic-input"], capture_output=True, text=True)
        self.assertEqual(run.returncode, 1)
        self.assertEqual(json.loads(run.stdout), {"allowed": False, "reason": "RIGHTS_EVIDENCE_INCOMPLETE"})
        self.assertNotIn("DO-NOT-LOG-THIS", run.stdout + run.stderr)

    def test_duplicate_json_keys_rejected(self):
        with self.assertRaisesRegex(rights.RightsDenied, "DUPLICATE_JSON_KEY"):
            json.loads('{"enabled":false,"enabled":true}', object_pairs_hook=rights.reject_duplicate_keys)

    def test_timezone_required_and_impossible_date_rejected(self):
        self.deny(registry(), "TIMESTAMP_REQUIRES_TIMEZONE", now=datetime(2026, 10, 29))
        for value in ["2026-01-01T00:00:00", "2026-02-30T00:00:00Z"]:
            doc = registry()
            doc["sources"][0]["rights"]["valid_from"] = value
            self.deny(doc, "INVALID_RIGHTS_SCHEMA")


class DeploymentSourcePolicyTest(unittest.TestCase):
    def config(self):
        return {"product_profile": "agriculture", "requested_uses": {"synthetic-input": ["analysis"]}, "registry": registry()}

    def test_checked_in_config_explicitly_disables_external_sources(self):
        config = json.loads((ROOT / "deploy/agriculture/sources.json").read_text())
        result = gate.check(config, profile="agriculture", now=NOW)
        self.assertEqual(result["enabled_source_count"], 0)
        self.assertTrue(result["external_sources_disabled"])
        self.assertFalse(result["production_ready"])

    def test_activation_with_missing_license_denied(self):
        config = self.config()
        config["registry"]["sources"][0]["rights"]["license_ref"] = None
        with self.assertRaisesRegex(gate.rights.RightsDenied, "RIGHTS_EVIDENCE_INCOMPLETE"):
            gate.check(config, profile="agriculture", now=NOW)

    def test_cannot_skip_enabled_sources_or_unknown_purposes(self):
        config = self.config()
        config["requested_uses"] = {}
        with self.assertRaisesRegex(gate.rights.RightsDenied, "ENABLED_SOURCE_SELECTION_MISMATCH"):
            gate.check(config, profile="agriculture", now=NOW)
        for uses in [[], ["unknown"], ["analysis", "analysis"], True]:
            config["requested_uses"] = {"synthetic-input": uses}
            with self.assertRaisesRegex(gate.rights.RightsDenied, "EXPLICIT_KNOWN_PURPOSES_REQUIRED"):
                gate.check(config, profile="agriculture", now=NOW)

    def test_all_declared_purposes_checked(self):
        config = self.config()
        config["requested_uses"]["synthetic-input"].append("model_training")
        config["registry"]["sources"][0]["rights"]["permitted_uses"] = ["analysis"]
        with self.assertRaisesRegex(gate.rights.RightsDenied, "PURPOSE_NOT_PERMITTED"):
            gate.check(config, profile="agriculture", now=NOW)

    def test_explicit_profile_and_known_categories_required(self):
        with self.assertRaisesRegex(gate.rights.RightsDenied, "EXPLICIT_AGRICULTURE_PROFILE_REQUIRED"):
            gate.check(self.config(), profile="", now=NOW)
        config = self.config()
        config["registry"]["sources"][0]["kind"] = "unclassified"
        with self.assertRaisesRegex(gate.rights.RightsDenied, "INVALID_RIGHTS_SCHEMA"):
            gate.check(config, profile="agriculture", now=NOW)

    def test_real_cli_default_config_passes_zero_enabled(self):
        run = subprocess.run([sys.executable, str(ROOT / "scripts/agriculture/check-source-policy.py"), "--profile", "agriculture"], capture_output=True, text=True)
        self.assertEqual(run.returncode, 0, run.stderr)
        self.assertEqual(json.loads(run.stdout)["enabled_source_count"], 0)


if __name__ == "__main__":
    unittest.main()
