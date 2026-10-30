"""Strict, secret-free gate for the actual browser/Go/GIS/PostgreSQL chain."""
import argparse
import json
from pathlib import Path
import re

ROOT = 'TestIdentityBrowserGISWriteChain'
REQUIRED = {ROOT} | {ROOT + '/' + name for name in (
    'browser_login_signed_write_and_actor_audit',
    'revoked_browser_write_has_no_domain_effect',
    'forged_cookie_has_no_domain_effect',
    'unauthenticated_open_endpoint_inventory',
)}


def reduce(raw):
    terminal, locations = {}, {}
    invalid, package_pass = False, False
    for line in raw.splitlines():
        try:
            event = json.loads(line)
            if not isinstance(event, dict):
                raise ValueError('event must be object')
        except (ValueError, TypeError):
            invalid = True
            continue
        action, name = event.get('Action'), event.get('Test')
        if action in {'skip', 'fail'}:
            invalid = True
        if not name and action == 'pass':
            package_pass = True
        if name in REQUIRED and action == 'output':
            hits = re.findall(r'\b([A-Za-z0-9_]+\.go):(\d+)\b', str(event.get('Output', '')))
            locations.setdefault(name, set()).update(hits)
        if name and action in {'pass', 'fail', 'skip'}:
            if name not in REQUIRED or name in terminal:
                invalid = True
                continue
            terminal[name] = action
    passed = not invalid and package_pass and set(terminal) == REQUIRED and all(v == 'pass' for v in terminal.values())
    return {
        'suite': 'actual_browser_go_gis_write_chain', 'passed': passed,
        'data_class': 'fixture', 'production_acceptance': False,
        'tests': [{'name': name, 'result': result} for name, result in sorted(terminal.items())],
        'missing': sorted(REQUIRED - terminal.keys()),
        'failure_locations': [{'test': name, 'locations': [{'file': f, 'line': int(n)} for f, n in sorted(locations.get(name, set()))[:20]]} for name, result in sorted(terminal.items()) if result != 'pass'],
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    try:
        result = reduce(args.input.read_text())
    except (OSError, UnicodeError):
        result = {'suite': 'actual_browser_go_gis_write_chain', 'passed': False, 'reason': 'missing_or_invalid_private_report'}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(result, indent=2) + '\n')
    return 0 if result['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
