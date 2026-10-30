import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('chain_result', Path(__file__).with_name('identity-chain-result.py'))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ChainResultTests(unittest.TestCase):
    def events(self):
        return [{'Action': 'pass', 'Test': name} for name in sorted(module.REQUIRED)] + [{'Action': 'pass'}]

    def reduced(self, events):
        return module.reduce('\n'.join(json.dumps(row) for row in events))

    def test_complete_actual_chain_required(self):
        self.assertTrue(self.reduced(self.events())['passed'])

    def test_each_missing_case_or_package_pass_fails(self):
        rows = self.events()
        for index in range(len(rows)):
            self.assertFalse(self.reduced(rows[:index] + rows[index + 1:])['passed'])

    def test_skip_fail_duplicates_unknown_and_malformed_fail(self):
        for action in ('skip', 'fail'):
            rows = self.events()
            rows[0]['Action'] = action
            self.assertFalse(self.reduced(rows)['passed'])
        self.assertFalse(self.reduced(self.events() + self.events()[:1])['passed'])
        self.assertFalse(self.reduced(self.events() + [{'Action': 'pass', 'Test': module.ROOT + '/unreviewed'}])['passed'])
        self.assertFalse(module.reduce('not-json')['passed'])
        self.assertFalse(module.reduce('[]')['passed'])

    def test_raw_secret_output_never_enters_report(self):
        rows = self.events()
        rows[0]['Action'] = 'fail'
        rows.append({'Action': 'output', 'Test': rows[0]['Test'], 'Output': 'identity_browser_chain_test.go:123 cookie=private-fixture-secret postgres://private-password'})
        result = self.reduced(rows)
        self.assertNotIn('private-', json.dumps(result))
        self.assertEqual(result['failure_locations'][0]['locations'], [{'file': 'identity_browser_chain_test.go', 'line': 123}])


if __name__ == '__main__':
    unittest.main()
