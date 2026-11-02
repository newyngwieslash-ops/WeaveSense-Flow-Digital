import importlib.util
from pathlib import Path
import unittest
import xml.etree.ElementTree as ET

spec = importlib.util.spec_from_file_location("parcel_py_gate", Path(__file__).with_name("parcel-python-result.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class ParcelPythonGateTests(unittest.TestCase):
    def xml(self):
        root = ET.Element("testsuite")
        for name in sorted(gate.REQUIRED):
            module, case = name.split("::", 1)
            ET.SubElement(root, "testcase", classname=module[:-3], name=case)
        return root

    def result(self, root):
        return gate.reduce(ET.tostring(root, encoding="unicode"))

    def test_all_52_exact_cases_and_13_actual_pg_cases(self):
        self.assertEqual(len(gate.REQUIRED), 52)
        self.assertEqual(sum(n.startswith("test_agriculture_parcels_store_postgres.py::") for n in gate.REQUIRED), 8)
        self.assertEqual(sum(n.startswith("test_agriculture_farm_parcel_guard_postgres.py::") for n in gate.REQUIRED), 5)
        self.assertTrue(self.result(self.xml())["passed"])

    def test_each_required_case_missing_fails(self):
        for i in range(52):
            root = self.xml()
            root.remove(root[i])
            self.assertFalse(self.result(root)["passed"])

    def test_skips_failures_errors_duplicates_unknown_fail_closed(self):
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

    def test_collection_errors_and_empty_suite_cannot_pass(self):
        self.assertFalse(gate.reduce("<testsuite/>")["passed"])
        for key in ("errors", "failures", "skipped"):
            root = self.xml()
            root.set(key, "1")
            self.assertFalse(self.result(root)["passed"])

    def test_shell_keeps_old_farm_and_adds_exact_parcel_modules(self):
        source = (Path(__file__).resolve().parents[2] / "scripts/agriculture/identity-postgres-ci.sh").read_text()
        for module in {name.split("::")[0] for name in gate.REQUIRED}:
            self.assertIn("tests/"+module, source)
        self.assertIn("scripts/agriculture/farm-python-result.py", source)
        self.assertIn("[[ $parcel_python_code == 0 && $parcel_python_gate == 0 ]]", source)


if __name__ == "__main__":
    unittest.main()
