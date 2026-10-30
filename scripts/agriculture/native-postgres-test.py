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
args = parser.parse_args()
base = repo / '.local/native-postgres'
root = args.package_root.resolve()
allowed = (repo/'.local').resolve()
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
    'GEO_MIGRATION_PASSWORD', 'GEO_RUNTIME_PASSWORD', 'GEO_WORKER_PASSWORD')}
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
        args=[str(repo/'.local/agri-identity-venv/bin/pytest'),'-q',*files,
              '--junitxml='+str(run/'tests.xml')]
        result=subprocess.run(args,cwd=repo/'services/agri-gis/backend',env=env,
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
    phase='post_domain_current' if module in {'test_agriculture_database','test_agriculture_database_postgres','test_agriculture_domain_boundary','test_agriculture_domain_rls_postgres'} else 'pre_domain_component'
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
}
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
scope += ['services/agri-gis/backend/app/dao/workbench_dao.py',
          'services/agri-gis/backend/app/services/workbench_service.py',
          'services/agri-gis/backend/app/services/project_user_service.py']
report={'kind':'local_real_postgres_fixture','remote_ci':False,
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
