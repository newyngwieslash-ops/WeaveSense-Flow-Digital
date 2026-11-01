import importlib.util
from pathlib import Path
import unittest
import xml.etree.ElementTree as ET

spec = importlib.util.spec_from_file_location("parser_result", Path(__file__).with_name("parser-ci-result.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ParserResultTests(unittest.TestCase):
    def test_only_closed_structure_is_exported(self):
        case = ET.fromstring('''<testcase><properties>
          <property name="parser_stage" value="valid_raster"/>
          <property name="parser_diagnostic" value='{&quot;exception&quot;:&quot;ImportError&quot;,&quot;line&quot;:93,&quot;secret&quot;:&quot;SECRET&quot;}'/>
          <property name="secret" value="SECRET"/></properties>
          <failure message="IngestionDenied: parser_rejected_input">SECRET TRACE</failure></testcase>''')
        self.assertEqual(module.diagnostic(case), {"stage":"valid_raster", "probe_exception":"ImportError",
                         "probe_line":93, "failure_kind":"failure", "reason":"parser_rejected_input"})
        self.assertFalse(module.passed(case))

    def test_untrusted_values_are_dropped_and_properties_do_not_fail(self):
        case = ET.fromstring('''<testcase><properties><property name="parser_stage" value="SECRET"/>
          <property name="parser_diagnostic" value='{&quot;exception&quot;:&quot;SECRET&quot;}'/></properties></testcase>''')
        self.assertEqual(module.diagnostic(case), {})
        self.assertTrue(module.passed(case))
        for tag in ("failure", "error", "skipped"):
            failed = ET.fromstring(f'<testcase><{tag} message="SECRET"/></testcase>')
            self.assertFalse(module.passed(failed))
            self.assertEqual(module.diagnostic(failed), {"failure_kind":tag})


if __name__ == "__main__":
    unittest.main()
