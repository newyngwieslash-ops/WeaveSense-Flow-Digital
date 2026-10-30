import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("result", Path(__file__).with_name("identity-ci-result.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class GateTests(unittest.TestCase):
    def go(self):
        return [dict(Action="pass", Test=x) for x in module.GO_CASES] + [dict(Action="pass")]

    def test_go_full_required(self):
        self.assertTrue(module.reduce("go", "\n".join(map(json.dumps, self.go())))["passed"])

    def test_go_missing_skip_and_failure(self):
        for events in (self.go()[1:], self.go()+[dict(Action="skip", Test="unknown")], self.go()+[dict(Action="fail")]):
            self.assertFalse(module.reduce("go", "\n".join(map(json.dumps, events)))["passed"])

    def xml(self):
        cases = [("test_agriculture_identity_postgres", n) for n in module.PG_REQUIRED]
        cases += [("test_agriculture_identity", "test_probe"), ("test_identity_canonical", "test_canonical")]
        return '<testsuites><testsuite>' + ''.join(f'<testcase classname="{c}" name="{n}"/>' for c,n in cases) + '</testsuite></testsuites>'

    def test_python_requires_both_pg_and_regular_suites(self):
        self.assertTrue(module.reduce("python", self.xml())["passed"])
        self.assertFalse(module.reduce("python", self.xml().replace('test_probe','bad'))["passed"])

    def test_no_secrets_in_failure_or_parameter_ids(self):
        raw = self.xml().replace('name="test_probe"/>', 'name="test_probe[SECRET]" ><failure>postgres://SECRET Bearer SECRET</failure></testcase>')
        report = module.reduce("python", raw)
        self.assertFalse(report["passed"])
        self.assertNotIn("SECRET", json.dumps(report))
        for marker in ('<skipped/>', '<error>SECRET</error>'):
            self.assertFalse(module.reduce("python", self.xml().replace('/>', '>'+marker+'</testcase>', 1))["passed"])

    def test_failure_coordinates_without_raw_traceback(self):
        raw = self.xml().replace('name="test_probe"/>', 'name="test_probe"><failure>/private/SECRET/test_identity.py:42: Bearer SECRET</failure></testcase>')
        report = module.reduce("python", raw)
        self.assertEqual(report["failure_locations"], [{"test": "test_probe", "locations": [{"file": "test_identity.py", "line": 42}]}])
        self.assertNotIn("SECRET", json.dumps(report))
        self.assertNotIn("/private", json.dumps(report))

    def test_actors_require_real_pg_and_actual_service_module(self):
        cases = [("test_agriculture_actors_postgres", n) for n in module.ACTOR_PG_REQUIRED]
        cases += [(m, "test_module_probe") for m in module.ACTOR_MODULES - {"test_agriculture_actors_postgres"}]
        raw = '<testsuites><testsuite>' + ''.join(
            f'<testcase classname="{c}" name="{n}"/>' for c, n in cases
        ) + '</testsuite></testsuites>'
        self.assertTrue(module.reduce("actors", raw)["passed"])
        for required in module.ACTOR_PG_REQUIRED:
            self.assertFalse(module.reduce("actors", raw.replace(required, "test_unrelated"))["passed"])
        self.assertFalse(module.reduce("actors", raw.replace("test_agriculture_legacy_write_integration", "test_unknown"))["passed"])
        self.assertFalse(module.reduce("actors", raw.replace('/>', '><skipped/></testcase>', 1))["passed"])


if __name__ == "__main__":
    unittest.main()
