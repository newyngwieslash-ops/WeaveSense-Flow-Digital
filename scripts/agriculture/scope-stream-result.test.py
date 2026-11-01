import importlib.util
import json
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location('result', Path(__file__).with_name('scope-stream-result.py'))
result = importlib.util.module_from_spec(spec)
spec.loader.exec_module(result)


class ScopeStreamGates(unittest.TestCase):
    def test_all_required_roots_and_children(self):
        for kind, required in result.REQUIRED.items():
            events = [dict(Action='pass', Test=name) for name in sorted(required)] + [dict(Action='pass')]
            raw = '\n'.join(map(json.dumps, events))
            self.assertTrue(result.reduce(kind, raw)['passed'])
            for index in range(len(events)):
                self.assertFalse(result.reduce(kind, '\n'.join(map(json.dumps, events[:index] + events[index + 1:])))['passed'])
            for action in ('skip', 'fail'):
                changed = events + [dict(Action=action, Test='unexpected', Output='Bearer SECRET')]
                reduced = result.reduce(kind, '\n'.join(map(json.dumps, changed)))
                self.assertFalse(reduced['passed'])
                self.assertNotIn('SECRET', json.dumps(reduced))
            self.assertFalse(result.reduce(kind, raw + '\n' + json.dumps(events[0]))['passed'])


if __name__ == '__main__':
    unittest.main()
