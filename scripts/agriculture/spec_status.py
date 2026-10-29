#!/usr/bin/env python3
"""Reconstructed status validator. Historical completion is not fresh acceptance."""
import argparse
import collections
import hashlib
import json
import re
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SPEC = ROOT / 'specs/agriculture/v1.0.0'
REQUIREMENTS_SHA = '1da296aa0230eb6223f5cd9c367ad50755eb46425fb91668569d6276f7b5db31'
BEGIN = '<!-- generated-task-status:start -->'
END = '<!-- generated-task-status:end -->'
HISTORICAL_DONE = {f'DT-{i:03}' for i in range(1, 13)}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def load_validate(spec):
    requirements_bytes = (spec / 'requirements.json').read_bytes()
    requirements = json.loads(requirements_bytes)
    document = json.loads((spec / 'tasks.json').read_text())
    manifest = json.loads((spec / 'MANIFEST.json').read_text())
    sha = hashlib.sha256(requirements_bytes).hexdigest()
    manifest_rows = [x for x in manifest['files'] if x['path'] == 'requirements.json']
    require(len(manifest_rows) == 1, 'baseline manifest must identify requirements once')
    require(sha == REQUIREMENTS_SHA == document['source_requirements_sha256'] == manifest_rows[0]['sha256'],
            'requirements differ from immutable reviewed baseline')
    expected_requirements = {f'AG-FR-{i:03}' for i in range(1, 81)} | {f'AG-NFR-{i:03}' for i in range(1, 21)}
    requirement_ids = [x['id'] for x in requirements['functional'] + requirements['nonfunctional']]
    require(len(requirements['functional']) == 80 and len(requirements['nonfunctional']) == 20,
            'expected 80 FR and 20 NFR')
    require(len(requirement_ids) == len(set(requirement_ids)) and set(requirement_ids) == expected_requirements,
            'requirement IDs missing, duplicated or unknown')
    tasks = document['tasks']
    ids = [t['id'] for t in tasks]
    require(len(ids) == 144 and len(set(ids)) == 144 and set(ids) == {f'DT-{i:03}' for i in range(1, 145)},
            'expected 144 unique consecutive task IDs')
    by_id = {t['id']: t for t in tasks}
    allowed = {'not_started', 'in_progress', 'blocked_external', 'blocked_internal', 'review', 'done'}
    require(set(document['status_policy']['allowed']) == allowed, 'status policy changed')
    mapped = {r: [] for r in requirement_ids}
    for task in tasks:
        require(task['status'] in allowed, f"invalid status: {task['id']}")
        require(len(task['dependencies']) == len(set(task['dependencies'])), 'duplicate dependencies')
        require(set(task['dependencies']) <= set(ids) and task['id'] not in task['dependencies'],
                f"unknown/self dependency: {task['id']}")
        require(set(task['requirements']) <= expected_requirements, 'unknown mapped requirement')
        for rid in task['requirements']:
            mapped[rid].append(task['id'])
    require(all(mapped.values()), 'unmapped requirements')
    require(document['requirement_to_tasks'] == mapped, 'requirement reverse index drift')
    visiting, visited = set(), set()

    def walk(task_id):
        require(task_id not in visiting, 'dependency cycle')
        if task_id in visited:
            return
        visiting.add(task_id)
        for dep in by_id[task_id]['dependencies']:
            walk(dep)
        visiting.remove(task_id)
        visited.add(task_id)

    for task_id in ids:
        walk(task_id)
    order = document['topological_execution_order']
    require(len(order) == len(ids) and set(order) == set(ids), 'invalid topological order IDs')
    position = {task_id: i for i, task_id in enumerate(order)}
    require(all(position[d] < position[t['id']] for t in tasks for d in t['dependencies']),
            'topological order violates dependency')
    packages = document['work_packages']
    require(len(packages) == 18 and {p['id'] for p in packages} == {f'W{i:02}' for i in range(18)},
            'work package count/IDs changed')
    package_members = []
    for package in packages:
        require(len(package['task_ids']) == 8, 'work package must contain eight tasks')
        for task_id in package['task_ids']:
            require(task_id in by_id and by_id[task_id]['work_package'] == package['id'], 'package mapping drift')
        package_members.extend(package['task_ids'])
    require(collections.Counter(package_members) == collections.Counter(ids), 'package coverage drift')
    for conditional in document['conditional_dependencies']:
        require(conditional['task_id'] in by_id and conditional['depends_on'] in by_id,
                'unknown conditional dependency')
    if 'recovery' in document:
        history = document['recovery']['historical_done']
        require(len(history) == 12 and set(history) == HISTORICAL_DONE,
                'historical completion exception is fixed to DT-001..012')
    for task in tasks:
        if task['status'] == 'done':
            require(all(by_id[dep]['status'] == 'done' for dep in task['dependencies']),
                    f"done task has incomplete dependencies: {task['id']}")
    return document


def recovery_pending(spec, document):
    path = spec / 'docs/implementation/RECOVERY_STATUS.json'
    recovery = json.loads(path.read_text()) if path.exists() else {'tasks': {}}
    require(set(recovery['tasks']) <= {t['id'] for t in document['tasks']}, 'unknown recovery task')
    pending = []
    for task in document['tasks']:
        row = recovery['tasks'].get(task['id'], {})
        if row:
            require(row['verification'] in {'pending_revalidation', 'restored_not_revalidated', 'verified'},
                    'unknown recovery verification status')
            for evidence in row.get('evidence', []):
                relative = Path(evidence['path'])
                require(not relative.is_absolute() and '..' not in relative.parts, 'unsafe evidence path')
                target = spec / relative
                require(target.is_file() and hashlib.sha256(target.read_bytes()).hexdigest() == evidence['sha256'],
                        f'evidence missing or changed: {relative}')
        if task['status'] == 'done':
            if row.get('verification') != 'verified' or not row.get('evidence'):
                require(task['id'] in HISTORICAL_DONE, f"new done task missing verified evidence: {task['id']}")
                pending.append(task['id'])
            else:
                commands = row.get('commands', [])
                require(commands and all(isinstance(c.get('command'), str) and c['command'].strip()
                                         and type(c.get('exit_code')) is int and c['exit_code'] == 0
                                         and c.get('result') == 'passed' for c in commands),
                        f"done verification missing successful commands: {task['id']}")
                sources = row.get('source_hashes', [])
                require(sources, f"done verification missing source hashes: {task['id']}")
                commit = row.get('source_commit', '')
                require(isinstance(commit, str) and re.fullmatch(r'[0-9a-f]{40}', commit),
                        f"done verification requires frozen source_commit: {task['id']}")
                repository = spec.parents[2]
                exists = subprocess.run(['git', 'cat-file', '-t', commit], cwd=repository,
                                        capture_output=True, text=True)
                require(exists.returncode == 0 and exists.stdout.strip() == 'commit', 'source commit missing/not a commit')
                ancestor = subprocess.run(['git', 'merge-base', '--is-ancestor', commit, 'HEAD'],
                                          cwd=repository, capture_output=True)
                require(ancestor.returncode == 0, 'source commit is not an ancestor of current HEAD')
                for source in sources:
                    relative = Path(source['path'])
                    require(not relative.is_absolute() and '..' not in relative.parts
                            and relative.as_posix() == source['path'] and str(relative) != '.', 'unsafe source path')
                    frozen = subprocess.run(['git', 'show', f'{commit}:{relative.as_posix()}'],
                                            cwd=repository, capture_output=True)
                    require(frozen.returncode == 0 and hashlib.sha256(frozen.stdout).hexdigest() == source['sha256'],
                            f'source missing or changed: {relative}')
    return pending


def block(document, pending):
    counts = collections.Counter(t['status'] for t in document['tasks'])
    ready = [t['id'] for t in document['tasks'] if t['status'] == 'not_started' and
             all(next(x for x in document['tasks'] if x['id'] == dep)['status'] == 'done' for dep in t['dependencies'])]
    rows = [BEGIN, '## 当前任务状态（从 tasks.json 生成）', '',
            '历史完成状态与本次恢复验证分开记录；done 不表示恢复后重新验收通过。', '',
            '| 状态 | 数量 |', '| --- | ---: |']
    rows += [f'| {state} | {count} |' for state, count in sorted(counts.items())]
    rows += ['', '历史 done、恢复证据待补验：' + ('、'.join(pending) or '无') + '。',
             '依赖状态就绪但仍须检查恢复条件：' + ('、'.join(ready) or '无') + '。', '',
             '| 任务 | 状态 |', '| --- | --- |']
    rows += [f"| {t['id']} | {t['status']} |" for t in document['tasks']]
    return '\n'.join([*rows, END])


def render_markdown(spec, document, pending):
    checklist = (spec / 'DEVELOPMENT_CHECKLIST.md').read_text()
    for task in document['tasks']:
        pattern = rf"(### {task['id']} · [^\n]+\n\n- 状态：`)[^`]+(`)"
        checklist, count = re.subn(pattern, lambda m: m[1] + task['status'] + m[2], checklist)
        require(count == 1, f"checklist task missing/duplicated: {task['id']}")
    generated = block(document, pending)
    outputs = {'DEVELOPMENT_CHECKLIST.md': checklist,
               'docs/implementation/STATUS.md': (spec / 'docs/implementation/STATUS.md').read_text()}
    for name, text in outputs.items():
        require(text.count(BEGIN) == text.count(END) <= 1, 'malformed generated status markers')
        outputs[name] = (re.sub(re.escape(BEGIN) + r'.*?' + re.escape(END), generated, text, flags=re.S)
                         if BEGIN in text else text.rstrip() + '\n\n' + generated + '\n')
    return outputs


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--spec-root', type=Path, default=SPEC)
    parser.add_argument('--require-evidence', action='store_true')
    modes = parser.add_mutually_exclusive_group()
    modes.add_argument('--sync', action='store_true')
    modes.add_argument('--check', action='store_true')
    args = parser.parse_args()
    try:
        document = load_validate(args.spec_root)
        pending = recovery_pending(args.spec_root, document)
        outputs = render_markdown(args.spec_root, document, pending)
        for name, value in outputs.items():
            path = args.spec_root / name
            if args.sync:
                path.write_text(value)
            else:
                require(path.read_text() == value, f'Markdown status drift: {name}; use --sync after review')
        if pending:
            print('WARNING historical done evidence pending: ' + ', '.join(pending))
        require(not args.require_evidence or not pending, 'recovery evidence incomplete')
        print('structure and synchronized status valid; this is not product acceptance')
    except (ValueError, KeyError, OSError, TypeError) as error:
        parser.exit(1, str(error) + '\n')


if __name__ == '__main__':
    main()
