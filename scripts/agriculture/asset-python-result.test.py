import importlib.util
from pathlib import Path
import unittest
import xml.etree.ElementTree as ET

spec = importlib.util.spec_from_file_location("asset_py_gate", Path(__file__).with_name("asset-python-result.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class AssetPythonGateTests(unittest.TestCase):
    def xml(self):
        root = ET.Element("testsuite")
        for name in sorted(gate.REQUIRED):
            module, case = name.split("::", 1)
            ET.SubElement(root, "testcase", classname=module[:-3], name=case)
        return root

    def result(self, root):
        return gate.reduce(ET.tostring(root, encoding="unicode"))

    def test_exact_109_cases_and_17_actual_pg_cases(self):
        self.assertEqual(len(gate.REQUIRED), 109)
        self.assertEqual(sum(n.startswith("test_agriculture_asset_catalog_postgres.py::") for n in gate.REQUIRED), 13)
        self.assertEqual(sum(n.startswith("test_agriculture_asset_object_postgres.py::") for n in gate.REQUIRED), 4)
        self.assertEqual(sum(n.startswith("test_agriculture_asset_download_fixture.py::") for n in gate.REQUIRED), 11)
        self.assertTrue(self.result(self.xml())["passed"])

    def test_each_missing_case_fails(self):
        for i in range(109):
            root = self.xml()
            root.remove(root[i])
            self.assertFalse(self.result(root)["passed"])

    def test_skips_errors_failures_duplicates_unknown_fail_closed(self):
        for tag in ("skipped", "failure", "error", "duplicate", "unknown"):
            root = self.xml()
            if tag == "duplicate":
                root.append(root[0])
            elif tag == "unknown":
                root[0].set("name", "SECRET_MUST_NOT_ESCAPE")
            else:
                ET.SubElement(root[0], tag).text = "SECRET_MUST_NOT_ESCAPE"
            result = self.result(root)
            self.assertFalse(result["passed"])
            self.assertNotIn("SECRET_MUST_NOT_ESCAPE", str(result))

    def test_collection_errors_empty_and_wrong_totals_fail(self):
        self.assertFalse(gate.reduce("<testsuite/>")["passed"])
        for key in ("errors", "failures", "skipped", "tests"):
            root = self.xml()
            root.set(key, "1")
            self.assertFalse(self.result(root)["passed"])

    def test_shell_preserves_previous_and_adds_exact_asset_modules(self):
        root = Path(__file__).resolve().parents[2]
        source = (root / "scripts/agriculture/identity-postgres-ci.sh").read_text()
        for module in {name.split("::")[0] for name in gate.REQUIRED}:
            self.assertIn("tests/"+module, source)
        self.assertIn("scripts/agriculture/farm-python-result.py", source)
        self.assertIn("scripts/agriculture/parcel-python-result.py", source)
        self.assertIn("[[ $asset_python_code == 0 && $asset_python_gate == 0 ]]", source)
        self.assertIn("identity-asset-python.json", source)
        workflow = (root / ".github/workflows/agriculture-identity.yml").read_text()
        self.assertIn("scaffold/scripts/agriculture/asset-python-result.test.py", workflow)


if __name__ == "__main__":
    unittest.main()
