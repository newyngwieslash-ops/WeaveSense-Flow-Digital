#!/usr/bin/env python3
"""Reconstructed AST inventory; never imports scanned source or proves authorization."""
import argparse
import ast
import hashlib
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
REPORT = Path('docs/agriculture/evidence/w01/legacy-identity-inventory.json')
METHODS = {'get', 'post', 'put', 'patch', 'delete', 'head', 'options',
           'websocket', 'api_route', 'route'}
REGISTRATIONS = {'include_router', 'mount', 'add_api_route', 'add_route',
                 'add_websocket_route'}
CREDENTIALS = {'credential_code', 'credential_secret', 'api_key', 'api_key_hash'}
ACTORS = {'operator', 'reviewer', 'uploader', 'requester', 'user', 'investigator',
          'actor', 'applicant', 'assignee', 'approver', 'collector', 'inspector'}


def candidate(name):
    return (name in CREDENTIALS or name.endswith('_by_code') or
            (name.endswith('_code') and bool(set(name.split('_')) & ACTORS)))


def classification(name, path, scope):
    if name in CREDENTIALS:
        return 'service_credential_not_human_actor'
    if name == 'assignee_code':
        return 'assignment_target_not_actor'
    if '/models/' in path:
        return 'persisted_identity_reference_not_authentication'
    if '/schemas/' in path:
        if any(x in scope for x in ('Response', 'Record', 'Summary')):
            return 'response_or_internal_identity_reference_not_authentication'
        if any(x in scope for x in ('Request', 'Input', 'Create', 'Update')):
            return 'caller_claim_not_trusted_identity'
        return 'response_or_internal_identity_reference_not_authentication'
    return 'legacy_identity_reference_requires_trusted_context'


def literal(node, fallback='<dynamic>'):
    return node.value if isinstance(node, ast.Constant) and isinstance(node.value, str) else fallback


def scan(root):
    app = root / 'services/agri-gis/backend/app'
    files, classes, modules = [], {}, {}
    for path in sorted(app.rglob('*.py')):
        raw = path.read_bytes()
        rel = path.relative_to(root).as_posix()
        tree = ast.parse(raw, filename=rel)
        module = 'app.' + '.'.join(path.relative_to(app).with_suffix('').parts)
        if module.endswith('.__init__'):
            module = module[:-9]
        imports = {}
        for node in ast.walk(tree):
            if isinstance(node, ast.ImportFrom):
                base = node.module or ''
                if node.level:
                    base = '.'.join(module.split('.')[:-node.level]) + ('.' + base if base else '')
                imports.update({a.asname or a.name: base + '.' + a.name for a in node.names})
            elif isinstance(node, ast.Import):
                imports.update({a.asname or a.name: a.name for a in node.names})
        modules[rel] = (tree, imports, module)
        files.append({'path': rel, 'sha256': hashlib.sha256(raw).hexdigest()})
        for node in ast.walk(tree):
            if isinstance(node, ast.ClassDef):
                classes[module + '.' + node.name] = (node, imports, module)

    def schema_fields(annotation, imports, module, seen=()):
        result = []
        if annotation is None:
            return result
        for name in sorted({n.id for n in ast.walk(annotation) if isinstance(n, ast.Name)}):
            key = imports.get(name, module + '.' + name)
            if key not in classes or key in seen:
                continue
            cls, ci, cm = classes[key]
            for base in cls.bases:
                result.extend(schema_fields(base, ci, cm, (*seen, key)))
            for field in cls.body:
                if isinstance(field, ast.AnnAssign) and isinstance(field.target, ast.Name):
                    if candidate(field.target.id):
                        result.append({'schema': key, 'field': field.target.id, 'line': field.lineno})
                    result.extend(schema_fields(field.annotation, ci, cm, (*seen, key)))
        return sorted({json.dumps(x, sort_keys=True): x for x in result}.values(),
                      key=lambda x: (x['schema'], x['field'], x['line']))

    routes, sites, registrations = [], [], []
    for rel, (tree, imports, module) in sorted(modules.items()):
        prefixes = {}
        for node in ast.walk(tree):
            if isinstance(node, ast.Assign) and isinstance(node.value, ast.Call):
                call = node.value
                if ast.unparse(call.func).endswith(('APIRouter', 'FastAPI')):
                    prefix = next((literal(k.value) for k in call.keywords if k.arg == 'prefix'), '')
                    for target in node.targets:
                        if isinstance(target, ast.Name):
                            prefixes[target.id] = prefix

        def visit(node, scope=''):
            if isinstance(node, (ast.ClassDef, ast.FunctionDef, ast.AsyncFunctionDef)):
                scope = scope + '.' + node.name if scope else node.name
            name = (node.id if isinstance(node, ast.Name) else
                    node.attr if isinstance(node, ast.Attribute) else
                    node.arg if isinstance(node, (ast.arg, ast.keyword)) else
                    node.value if isinstance(node, ast.Constant) and isinstance(node.value, str) else None)
            if name and candidate(name):
                sites.append({'path': rel, 'line': node.lineno, 'column': node.col_offset,
                              'scope': scope, 'field': name, 'kind': type(node).__name__,
                              'classification': classification(name, rel, scope),
                              'evidence': ast.unparse(node)[:500]})
            if isinstance(node, ast.Call) and isinstance(node.func, ast.Attribute):
                if node.func.attr in REGISTRATIONS:
                    registrations.append({'path': rel, 'line': node.lineno,
                                          'evidence': ast.unparse(node)})
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
                for dec in node.decorator_list:
                    if not isinstance(dec, ast.Call) or not isinstance(dec.func, ast.Attribute) or dec.func.attr not in METHODS:
                        continue
                    router = ast.unparse(dec.func.value)
                    route_path = literal(dec.args[0]) if dec.args else '<dynamic>'
                    prefix = prefixes.get(router, '<dynamic>')
                    args = node.args.posonlyargs + node.args.args + node.args.kwonlyargs
                    fields = [x for arg in args for x in schema_fields(arg.annotation, imports, module)]
                    direct = [a.arg for a in args if candidate(a.arg)]
                    method = dec.func.attr.upper()
                    methods = [method]
                    if method in {'ROUTE', 'API_ROUTE'}:
                        methods = next(([literal(v) for v in k.value.elts] for k in dec.keywords
                                        if k.arg == 'methods' and isinstance(k.value, (ast.List, ast.Tuple))), ['<dynamic>'])
                    routes.append({'path': rel, 'line': node.lineno, 'function': scope,
                                   'router': router, 'route': prefix + route_path,
                                   'methods': methods, 'direct_identity_parameters': direct,
                                   'schema_identity_fields': fields,
                                   'no_caller_identity_field': not direct and not fields,
                                   'calls': sorted({ast.unparse(c.func) for c in ast.walk(node) if isinstance(c, ast.Call)}),
                                   'imports': dict(sorted(imports.items()))})
            for child in ast.iter_child_nodes(node):
                visit(child, scope)
        visit(tree)
    return {'schema_version': '2.0-reconstructed',
            'method': 'python_ast_static_inventory_not_runtime_authorization_proof',
            'summary': {'declared_routes': len(routes), 'identity_sites': len(sites),
                        'route_registrations': len(registrations),
                        'routes_without_caller_identity': sum(r['no_caller_identity_field'] for r in routes)},
            'source_files': files, 'routes': routes, 'identity_sites': sites,
            'route_registrations': registrations}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--report', type=Path, default=REPORT)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--write', action='store_true')
    mode.add_argument('--check', action='store_true')
    args = parser.parse_args()
    output = json.dumps(scan(args.root), ensure_ascii=False, indent=2, sort_keys=True) + '\n'
    path = args.root / args.report
    if args.write:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(output, encoding='utf-8')
    elif args.check:
        if not path.exists() or path.read_text(encoding='utf-8') != output:
            raise SystemExit('inventory drift: review source and explicitly refresh snapshot')
        print('inventory matches source')
    else:
        print(output, end='')


if __name__ == '__main__':
    main()
