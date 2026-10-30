"""Workspace-only real PostgreSQL fixture; no system installation or deployment."""
import argparse
import hashlib
import json
import os
import secrets
import socket
import subprocess
import sys
import time
import uuid
import xml.etree.ElementTree as ET
from datetime import datetime, timezone
from pathlib import Path
from native_test_environment import native_test_environment

repo = Path(__file__).resolve().parents[2]
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--package-root', type=Path, required=True)
parser.add_argument('--python', type=Path, default=repo/'.local/agri-identity-venv/bin/python')
parser.add_argument('--phase', choices=('components', 'migration', 'query', 'objects', 'jobs'), default='components')
parser.add_argument('--go', type=Path, help='explicit local Go binary for the fixed jobs integration suite')
args = parser.parse_args()
phase_mode = args.phase
test_python = args.python.absolute()
if not test_python.is_file():
    raise SystemExit('explicit locked Python test environment required')
base = repo / '.local/native-postgres'
root = args.package_root.resolve()
allowed = (repo/'.local').resolve()
test_go = args.go.resolve() if args.go else None
if phase_mode == 'jobs' and (test_go is None or not test_go.is_file() or not test_go.is_relative_to(allowed)):
    raise SystemExit('jobs phase requires an explicit prepared Go binary inside repository .local')
if phase_mode != 'jobs' and test_go is not None:
    raise SystemExit('--go is only supported for the fixed jobs phase')
if not allowed.is_relative_to(repo.resolve()) or not root.is_relative_to(allowed) or not base.resolve().is_relative_to(allowed):
    raise SystemExit('fixture paths must remain inside repository .local')
if os.geteuid() == 0:
    raise SystemExit('refusing root execution')
if not (root/'usr/lib/postgresql/17/bin/postgres').is_file():
    raise SystemExit('prepared PostgreSQL package root required')
package_manifest = json.loads((repo/'deploy/agriculture/native-postgres-packages.json').read_text())
receipt = json.loads((root/'prepared.json').read_text())
if receipt['manifest_sha256'] != hashlib.sha256((repo/'deploy/agriculture/native-postgres-packages.json').read_bytes()).hexdigest():
    raise SystemExit('package manifest changed; prepare a new package root')
for relative, expected in receipt['files'].items():
    path = (root/relative).resolve()
    if not path.is_relative_to(root) or hashlib.sha256(path.read_bytes()).hexdigest() != expected:
        raise SystemExit('prepared package content changed')
for relative, expected in receipt['links'].items():
    path = root/relative
    if not path.resolve().is_relative_to(root) or not path.is_symlink() or os.readlink(path) != expected:
        raise SystemExit('prepared package link changed')
binary = root / 'usr/lib/postgresql/17/bin'
share = root / 'usr/share/postgresql/17'
run = base / 'runs' / ('run_' + uuid.uuid4().hex)
if not run.resolve().is_relative_to(allowed):
    raise SystemExit('fixture run path escapes repository .local')
os.umask(0o077)
run.mkdir(parents=True)
passwords = {name: secrets.token_hex(24) for name in (
    'POSTGRES_PASSWORD', 'PLATFORM_MIGRATION_PASSWORD', 'PLATFORM_RUNTIME_PASSWORD',
    'INTEL_MIGRATION_PASSWORD', 'INTEL_RUNTIME_PASSWORD',
    'GEO_MIGRATION_PASSWORD', 'GEO_RUNTIME_PASSWORD', 'GEO_WORKER_PASSWORD', 'PLATFORM_SYSTEM_PASSWORD', 'INTEL_SYSTEM_PASSWORD')}
with socket.socket() as sock:
    sock.bind(('127.0.0.1',0))
    port = str(sock.getsockname()[1])
env = {**native_test_environment(os.environ, run), **passwords, 'POSTGRES_USER':'postgres',
       'PGHOST':'127.0.0.1', 'PGPORT':port, 'PGPASSWORD':passwords['POSTGRES_PASSWORD'],
       'PATH':str(binary)+os.pathsep+os.environ['PATH'],
       'LD_LIBRARY_PATH':str(root/'usr/lib/x86_64-linux-gnu'),
       'AGRI_TEST_PG_ISOLATED':'1'}
for role,key,target in [('flow_geo_migrate','GEO_MIGRATION_PASSWORD','AGRI_TEST_GEO_MIGRATION_DSN'),
                        ('flow_geo_app','GEO_RUNTIME_PASSWORD','AGRI_TEST_GEO_RUNTIME_DSN'),
                        ('flow_geo_worker','GEO_WORKER_PASSWORD','AGRI_TEST_GEO_WORKER_DSN')]:
    env[target]=f'postgresql://{role}:{passwords[key]}@127.0.0.1:{port}/flow_geo?sslmode=disable'
# Only the exact fixture identities needed by tests cross the subprocess edge.
# Bootstrap secrets/PGPASSWORD stay in initialization/version/shutdown processes.
test_env = {k: env[k] for k in ('PATH','HOME','LANG','LANGUAGE','LC_ALL','LC_CTYPE','TZ',
            'LD_LIBRARY_PATH','AGRI_TEST_PG_ISOLATED','AGRI_TEST_GEO_MIGRATION_DSN',
            'AGRI_TEST_GEO_RUNTIME_DSN','AGRI_TEST_GEO_WORKER_DSN') if k in env}
test_env['AGRI_IDENTITY_PYTHON'] = str(test_python)
if test_go is not None:
    test_env['AGRI_TEST_GO_BIN'] = str(test_go)
pw = run/'bootstrap-password'
pw.write_text(passwords['POSTGRES_PASSWORD']+'\n')
files = [
    'tests/test_identity_canonical.py', 'tests/test_agriculture_identity.py',
    'tests/test_agriculture_actors.py', 'tests/test_legacy_actor_gate.py',
    'tests/test_agriculture_identity_postgres.py',
    'tests/test_agriculture_actors_postgres.py',
    'tests/test_agriculture_legacy_write_integration.py',
    'tests/test_legacy_identity_contracts.py',
    'tests/test_agriculture_database.py', 'tests/test_agriculture_database_postgres.py',
    'tests/test_agriculture_domain_boundary.py', 'tests/test_agriculture_domain_rls_postgres.py',
]
if phase_mode == 'migration':
    files = ['tests/test_agriculture_migration.py', 'tests/test_agriculture_migration_postgres.py']
elif phase_mode == 'query':
    files = ['tests/test_agriculture_query_scope.py', 'tests/test_agriculture_query_scope_postgres.py']
elif phase_mode == 'objects':
    files = ['tests/test_agriculture_object_storage.py', 'tests/test_agriculture_object_gateway.py', 'tests/test_agriculture_objects_postgres.py']
elif phase_mode == 'jobs':
    files = ['tests/test_agriculture_job_authority.py', 'tests/test_agriculture_jobs_postgres.py']
started=False
start=time.monotonic()
with (run/'private.log').open('w') as log:
    try:
        subprocess.run([str(binary/'initdb'),'-D',str(run/'cluster'),'-L',str(share),
                        '-U','postgres','--pwfile',str(pw),'--auth=scram-sha-256',
                        '--no-locale','--encoding=UTF8'],env=env,stdout=log,stderr=log,check=True)
        started=True  # Also clean up if startup succeeds but readiness wait fails.
        subprocess.run([str(binary/'pg_ctl'),'-D',str(run/'cluster'),'-l',str(run/'server.log'),
                        '-o',f"-h 127.0.0.1 -p {port} -c unix_socket_directories='' -c jit=off",
                        '-w','start'],env=env,stdout=log,stderr=log,check=True)
        subprocess.run(['sh',str(repo/'deploy/agriculture/postgres/init.sh')],env=env,
                       stdout=log,stderr=log,check=True)
        version=subprocess.check_output([str(binary/'psql'),'-U','postgres','-d','flow_geo',
                         '-Atc',"SELECT version(); SELECT postgis_lib_version();"],env=env,text=True).splitlines()
        args=[str(test_python),'-m','pytest','-q',*files,
              '--junitxml='+str(run/'tests.xml')]
        result=subprocess.run(args,cwd=repo/'services/agri-gis/backend',env=test_env,
                              stdout=log,stderr=log,timeout=180)
    finally:
        try:
            if started:
                stopped=subprocess.run([str(binary/'pg_ctl'),'-D',str(run/'cluster'),
                                        '-m','fast','-w','stop'],env=env,stdout=log,stderr=log,timeout=30)
        finally:
            pw.unlink(missing_ok=True)
cases=[]
for t in ET.parse(run/'tests.xml').getroot().iter('testcase'):
    statuses=[e.tag for e in t if e.tag in {'failure','error','skipped'}]
    module=t.attrib.get('classname','').split('.')[-1]
    phase='job_authority_current' if phase_mode == 'jobs' else 'private_objects_current' if phase_mode == 'objects' else 'independent_fresh_database_migration' if phase_mode == 'migration' else 'post_query_current' if phase_mode == 'query' else 'post_domain_current' if module in {'test_agriculture_database','test_agriculture_database_postgres','test_agriculture_domain_boundary','test_agriculture_domain_rls_postgres'} else 'pre_domain_component'
    cases.append({'name':t.attrib['name'],'schema_phase':phase,'status':statuses[0] if statuses else 'pass',
                  'seconds':float(t.attrib.get('time','0'))})
required_pg = {
    'test_postgres_replay_parallel_persistence_scope_and_fail_closed',
    'test_postgres_actual_verifier_dependency_nonce_order_and_parallel_http',
    'test_postgres_actor_mapping_strict_scope_and_atomic_audit',
    'test_postgres_actor_binding_lock_blocks_concurrent_revocation',
    'test_postgres_real_workbench_write_commit_and_stable_audit',
    'test_postgres_real_workbench_audit_failure_rolls_back',
    'test_postgres_runtime_factory_role_admission',
    'test_postgres_runtime_scope_pool_reuse_and_rollback',
    'test_postgres_worker_factory_admission_without_business_privileges',
    'test_postgres_domain_factory_rejects_incomplete_boundary',
    'test_postgres_domain_scope_inventory_and_unmapped_denial',
    'test_postgres_domain_cross_workspace_parent_and_policy_denial',
    'test_postgres_domain_workbench_commit_and_audit_rollback',
}
if phase_mode == 'migration':
    required_pg = {'test_postgres_migration_command_new_repeat_and_drift'}
elif phase_mode == 'query':
    required_pg = {'test_postgres_query_scope_same_bbox_and_history',
                   'test_postgres_query_scope_mixed_task_and_core',
                   'test_postgres_query_scope_missing_mapping_and_rebinding'}
elif phase_mode == 'objects':
    required_pg = {'test_postgres_private_objects_scope_immutability',
                   'test_postgres_private_object_http_bytes_and_withdrawal'}
elif phase_mode == 'jobs':
    required_pg = {'test_postgres_job_sidecar_scope_immutable_and_worker_privileges',
                   'test_postgres_job_execution_lease_fencing_and_retry_scope',
                   'test_real_go_python_job_authority_chain'}
missing_pg = sorted(required_pg-{c['name'] for c in cases if c['status']=='pass'})
scope=['deploy/agriculture/postgres/init.sh', 'deploy/agriculture/native-postgres-packages.json',
       'scripts/agriculture/native_test_environment.py',
       'tests/agriculture/test_native_test_environment.py',
       'tests/agriculture/test_native_postgres_helper.py',
       'scripts/agriculture/native-postgres-test.py', 'scripts/agriculture/native-postgres-prepare.py',
       'services/agri-gis/backend/pyproject.toml', 'services/agri-gis/backend/poetry.lock']
for directory in ['services/agri-gis/backend/app',
                  'services/agri-gis/backend/scripts/migrations']:
    scope += [str(p.relative_to(repo)) for p in (repo/directory).rglob('*')
              if p.is_file() and p.suffix in {'.py','.sql','.json'}]
scope += ['services/agri-gis/backend/'+f for f in files]
scope += ['services/agri-gis/backend/tests/domain_fixture_support.py']
if phase_mode in {'objects', 'jobs'}:
    scope += ['services/agri-gis/backend/tests/test_agriculture_identity.py',
              'services/agri-gis/backend/tests/test_agriculture_actors_postgres.py']
if phase_mode == 'jobs':
    scope += ['backend/go.mod', 'backend/go.sum',
              'services/agri-gis/backend/tests/job_fixture_support.py',
              'services/agri-gis/backend/tests/job_http_fixture.py']
    scope += [str(p.relative_to(repo)) for p in (repo/'backend').rglob('*.go') if '.local' not in p.parts]
scope += ['services/agri-gis/backend/app/dao/workbench_dao.py',
          'services/agri-gis/backend/app/services/workbench_service.py',
          'services/agri-gis/backend/app/services/project_user_service.py']
report={'kind':'local_real_postgres_fixture','remote_ci':False,'phase_mode':phase_mode,
        'recorded_at':datetime.now(timezone.utc).isoformat(),'elapsed_seconds':round(time.monotonic()-start,3),
        'fresh_cluster':True,'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=repo,text=True).strip(),
        'worktree_changes_included':True,'production_acceptance':False,'live_farm_data':False,
        'postgres':version[0],'postgis':version[1],'python':sys.version.split()[0],
        'exit_code':result.returncode,'shutdown_exit_code':stopped.returncode,
        'missing_required_pg_pass':missing_pg,
        'passed':sum(c['status']=='pass' for c in cases),'failed':sum(c['status'] in {'failure','error'} for c in cases),
        'skipped':sum(c['status']=='skipped' for c in cases),'tests':cases,
        'source_files':{f:hashlib.sha256((repo/f).read_bytes()).hexdigest() for f in sorted(set(scope))},
        'packages':package_manifest['packages']}
(run/'report.json').write_text(json.dumps(report,indent=2)+'\n')
print(json.dumps({k:report[k] for k in ['passed','failed','skipped','exit_code','shutdown_exit_code']}))
print('report='+str(run/'report.json'))
sys.exit(result.returncode or stopped.returncode or int(bool(missing_pg)))
