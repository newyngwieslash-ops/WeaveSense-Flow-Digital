import importlib.util
from pathlib import Path
import unittest
import xml.etree.ElementTree as ET

spec = importlib.util.spec_from_file_location("gate", Path(__file__).with_name("farm-python-result.py"))
gate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(gate)


class FarmPythonGateTests(unittest.TestCase):
    def xml(self):
        root = ET.Element("testsuite")
        for name in sorted(gate.REQUIRED):
            module, case = name.split("::", 1)
            ET.SubElement(root, "testcase", classname=module[:-3], name=case)
        return root

    def result(self, root):
        return gate.reduce(ET.tostring(root, encoding="unicode"))

    def test_all_exact_81_including_ten_actual_pg_cases(self):
        self.assertEqual(len(gate.REQUIRED), 81)
        self.assertEqual(sum(n.startswith("test_agriculture_farms_store_postgres.py::") for n in gate.REQUIRED), 8)
        self.assertTrue(self.result(self.xml())["passed"])

    def test_skip_failure_error_missing_duplicate_and_unknown_rejected(self):
        for tag in ("skipped", "failure", "error", "missing", "duplicate", "unknown"):
            with self.subTest(tag=tag):
                root = self.xml()
                if tag == "missing": root.remove(root[0])
                elif tag == "duplicate": root.append(root[0])
                elif tag == "unknown": root[0].set("name", "SECRET_MUST_NOT_EXPORT")
                else: ET.SubElement(root[0], tag).text = "SECRET_MUST_NOT_EXPORT"
                result = self.result(root)
                self.assertFalse(result["passed"])
                self.assertNotIn("SECRET_MUST_NOT_EXPORT", str(result))

    def test_empty_cannot_pass(self):
        self.assertFalse(gate.reduce("<testsuite/>")["passed"])

    def test_suite_error_without_named_case_cannot_pass(self):
        root = self.xml()
        root.set("errors", "1")
        self.assertFalse(self.result(root)["passed"])


if __name__ == "__main__":
    unittest.main()
