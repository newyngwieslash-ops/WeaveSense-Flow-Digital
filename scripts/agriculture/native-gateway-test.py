"""Real unprivileged nginx + built assets + Go API HTTP matrix, not a browser mock."""

import argparse
import hashlib
import http.cookiejar
import json
import os
import re
import secrets
import shutil
import socket
import subprocess
import time
import urllib.error
import urllib.request
import uuid
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--nginx', type=Path, required=True)
    parser.add_argument('--mime-types', type=Path, required=True)
    parser.add_argument('--binary', type=Path, default=ROOT/'.local/bin/agriculture-platform')
    args = parser.parse_args()
    if os.geteuid() == 0:
        raise SystemExit('non-root fixture execution required')
    react = ROOT/'web/dist-agriculture'
    vue = ROOT/'services/agri-gis/frontend/dist-agriculture'
    for path in [args.nginx, args.mime_types, args.binary, react/'index.html', vue/'index.html']:
        if not path.is_file(): raise SystemExit('required real executable/build missing')
    run = ROOT/'.local/native-gateway'/('run_'+uuid.uuid4().hex)
    if not (ROOT/'.local').resolve().is_relative_to(ROOT.resolve()) or not run.resolve().is_relative_to((ROOT/'.local').resolve()):
        raise SystemExit('fixture path escape')
    os.umask(0o077)
    run.mkdir(parents=True)
    static = run/'html'
    shutil.copytree(react, static)
    shutil.copytree(vue, static/'agri')
    shutil.copytree(ROOT/'deploy/agriculture/errors', static/'_agri_errors')
    (run/'client').mkdir()
    (run/'proxy').mkdir()
    for name in ('fastcgi','uwsgi','scgi'):
        (run/name).mkdir()
    (run/'backend').mkdir()
    api_port, port = free_port(), free_port()
    origin = f'http://127.0.0.1:{port}'
    csrf_key = run/'csrf.key'
    csrf_key.write_bytes(secrets.token_bytes(32))
    csrf_key.chmod(0o600)
    template_path = ROOT/'deploy/agriculture/nginx.conf.template'
    template = template_path.read_text()
    replacements = {
        '${AGRI_LISTEN}': f'127.0.0.1:{port}', '${AGRI_STATIC_ROOT}': str(static),
        '${AGRI_PLATFORM_UPSTREAM}': f'127.0.0.1:{api_port}',
        '/etc/nginx/mime.types': str(args.mime_types.resolve()),
        '/tmp/agriculture-nginx.pid': str(run/'nginx.pid'),
        '/tmp/agriculture-client': str(run/'client'), '/tmp/agriculture-proxy': str(run/'proxy'),
        '/tmp/agriculture-fastcgi': str(run/'fastcgi'),
        '/tmp/agriculture-uwsgi': str(run/'uwsgi'),
        '/tmp/agriculture-scgi': str(run/'scgi'),
    }
    for old, new in replacements.items():
        if any(c in new for c in [';', '\n', '\r', '{', '}']):
            raise SystemExit('unsafe native path replacement')
        template = template.replace(old, new)
    if '${AGRI_' in template: raise SystemExit('unresolved gateway placeholder')
    config = run/'nginx.conf'
    config.write_text(template)
    results = []
    jar = http.cookiejar.CookieJar()
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}),
        urllib.request.HTTPCookieProcessor(jar), NoRedirect())
    anonymous = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def request(path, method='GET', body=None, headers=None, authenticated=True):
        hdr = dict(headers or {})
        transport = opener if authenticated else urllib.request.build_opener(
            urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()), NoRedirect())
        if method not in ('GET', 'HEAD', 'OPTIONS'):
            token_headers = {'Origin': origin}
            if 'Cookie' in hdr: token_headers['Cookie'] = hdr['Cookie']
            with transport.open(urllib.request.Request(origin+'/api/auth/csrf', headers=token_headers), timeout=10) as token_response:
                token = json.load(token_response)['data']['token']
            if not re.fullmatch('[a-f0-9]{64}', token): raise AssertionError('invalid CSRF fixture response')
            hdr.setdefault('Origin', origin)
            hdr['X-CSRF-Token'] = token
        if body is not None: hdr['Content-Type'] = 'application/json'
        req = urllib.request.Request(origin+path, method=method, headers=hdr,
            data=None if body is None else json.dumps(body).encode())
        try:
            response = transport.open(req, timeout=10)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            return response.status, dict(response.headers), response.read(4*1024*1024)

    def check(condition, name):
        if not condition: raise AssertionError(name)
        results.append({'name': name, 'status': 'pass'})

    def header(headers, name):
        return next((v for k,v in headers.items() if k.lower()==name.lower()), '')

    def no_store(headers):
        return 'no-store' in header(headers,'cache-control')

    processes = []
    report = {'kind':'real_native_nginx_go_http_matrix','browser_executed':False,
        'production_deployment':False,'live_gis_business_data':False,
        'source_commit':subprocess.check_output(['git','rev-parse','HEAD'],cwd=ROOT,text=True).strip(),
        'working_tree_changes_included':True,'started_at':datetime.now(timezone.utc).isoformat(),
        'results':results,'limitations':['HTTP matrix is not a JavaScript browser navigation test',
            'Go SQLite fixture is not PostgreSQL tenancy acceptance',
            'GIS business routes remain unavailable until domain integration'],
        'nginx_environment_adaptations':['owned native pid/temp paths and mime file',
            'loopback high port', 'master_process off to avoid unavailable Unix-domain worker IPC'],
        'source_hashes':{'deploy/agriculture/nginx.conf.template':digest(template_path)},
        'backend_sha256':digest(args.binary),'nginx_sha256':digest(args.nginx),
        'asset_hashes':{str(p.relative_to(static)):digest(p) for p in static.rglob('*') if p.is_file()}}
    for folder in ['backend/cmd', 'backend/internal', 'web/src', 'services/agri-gis/frontend/agriculture',
                   'services/agri-gis/frontend/src/security', 'contracts/agriculture']:
        for path in (ROOT/folder).rglob('*'):
            if path.is_file() and path.suffix in {'.go','.ts','.tsx','.vue','.json','.css'}:
                report['source_hashes'][str(path.relative_to(ROOT))]=digest(path)
    for relative in ['scripts/agriculture/native-gateway-test.py','web/vite.agriculture.config.ts',
                     'web/package.json','web/bun.lock','services/agri-gis/frontend/package.json',
                     'services/agri-gis/frontend/bun.lock','services/agri-gis/frontend/vite.agriculture.config.ts']:
        path=ROOT/relative
        if path.is_file(): report['source_hashes'][relative]=digest(path)
    version=subprocess.run([str(args.nginx.resolve()),'-v'],capture_output=True,text=True,check=True)
    report['nginx_version']=(version.stdout+version.stderr).strip()
    report['python_version']=subprocess.check_output(['python','--version'],text=True).strip()
    code = 1
    with (run/'private-process.log').open('w') as log:
        try:
            backend = subprocess.Popen([str(args.binary.resolve())],cwd=ROOT/'backend',env={
                'PATH':os.environ['PATH'],'HOME':str(run/'backend'),'GIN_MODE':'release',
                'CANVAS_DATABASE_DRIVER':'sqlite','CANVAS_BACKEND_DATA_DIR':str(run/'backend'),
                'CANVAS_BACKEND_ADDR':f'127.0.0.1:{api_port}','CANVAS_AUTO_MIGRATE':'true',
                'CANVAS_CORS_ORIGINS':origin,'FLOW_AGRICULTURE_ENABLED':'true',
                'FLOW_AGRICULTURE_EXTERNAL_ORIGIN':origin,'FLOW_AGRICULTURE_ALLOW_HTTP_LOOPBACK_DEV':'true',
                'FLOW_AGRICULTURE_CSRF_KEY_FILE':str(csrf_key),
                'FLOW_AGRICULTURE_ALLOW_SQLITE_DEV':'true'},stdout=log,stderr=log)
            processes.append(backend)
            nginx_cmd=[str(args.nginx.resolve()),'-p',str(run)+'/', '-c',str(config),
                       '-g','daemon off; master_process off;']
            subprocess.run(nginx_cmd+['-t'],stdout=log,stderr=log,check=True)
            nginx=subprocess.Popen(nginx_cmd,stdout=log,stderr=log)
            processes.append(nginx)
            deadline=time.monotonic()+60
            while time.monotonic()<deadline:
                if backend.poll() is not None or nginx.poll() is not None:
                    raise RuntimeError('real gateway/backend exited during startup')
                try:
                    if request('/api/auth/settings')[0]==200: break
                except (OSError, urllib.error.URLError): pass
                time.sleep(.1)
            else: raise RuntimeError('real gateway/backend readiness timed out')
            check(True,'real_nginx_and_go_started')
            forbidden_material=[b'-----BEGIN PRIVATE KEY-----',b'-----BEGIN RSA PRIVATE KEY-----',
                                b'-----BEGIN OPENSSH PRIVATE KEY-----']
            check(not any(marker in p.read_bytes() for p in static.rglob('*') if p.is_file()
                          for marker in forbidden_material),'built_assets_have_no_private_key_material')
            for path, expected in [('/',react/'index.html'),('/login',react/'index.html'),
                                   ('/agri/',vue/'index.html'),('/agri/workspaces/deep/link',vue/'index.html')]:
                status,headers,body=request(path)
                check(status==200 and body==expected.read_bytes() and no_store(headers), 'history_'+path)
                check(header(headers,'x-content-type-options')=='nosniff','nosniff_'+path)
            status,headers,body=request('/agri?workspace=fixture')
            check(status==308 and header(headers,'location')=='/agri/?workspace=fixture','agri_relative_redirect_preserves_query')
            for prefix, dist in [('',react),('/agri',vue)]:
                assets=re.findall(r'(?:src|href)="([^\"]+\.(?:js|css))"',(dist/'index.html').read_text())
                check(bool(assets),'actual_assets_present_'+prefix)
                for path in assets:
                    status,headers,body=request(path)
                    check(status==200 and 'immutable' in header(headers,'cache-control') and body== (static/path.lstrip('/')).read_bytes(),'real_hashed_asset_'+path)
            for path in ['/assets/missing-12345678.js','/agri/assets/missing-12345678.js',
                         '/agri/missing.json','/internal','/internal/agri/v1/ownership/resolve',
                         '/api/internal/agri/v1/authority/check','/api/v1/projects',
                         '/agri/api/projects','/.env','/agri/.env','/_agri_errors/404.html',
                         '/assets/.hidden-12345678.js','/agri/assets/.hidden-12345678.js',
                         '/agri/%2e%2e/internal/agri/v1/ownership/resolve']:
                status,headers,body=request(path)
                check(status==404 and no_store(headers) and b'<div id="app">' not in body,'private_or_missing_'+path)
            status,headers,body=request('/api/agriculture/workspaces',authenticated=False)
            check(status==401 and no_store(headers),'anonymous_workspace_denied')
            status,headers,body=request('/api/agriculture/gis/farms',authenticated=False)
            check(status in {401,404,503} and no_store(headers) and b'<!doctype html>' not in body.lower(),'gis_unavailable_not_spa_success')
            username='nginx_'+secrets.token_hex(6)
            password=secrets.token_urlsafe(24)
            status,headers,body=request('/api/auth/register','POST',{'username':username,'password':password,'displayName':'Gateway Fixture','acceptedTerms':True})
            actor=json.loads(body).get('data',{}).get('user',{}).get('id')
            check(status in {200,201} and bool(actor),'real_fixture_registration')
            status,headers,body=request('/api/auth/login','POST',{'username':username,'password':password})
            check(status==200 and no_store(headers),'real_login')
            cookie=next((c for c in jar if c.name=='open_ai_canvas_session'),None)
            check(cookie is not None and cookie.path=='/' and cookie.has_nonstandard_attr('HttpOnly') and cookie.get_nonstandard_attr('SameSite')=='Lax','shared_httponly_root_cookie')
            previous_cookie=cookie.value
            status,headers,body=request('/api/agriculture/organizations','POST',
                {'name':'Native Gateway Fixture'},headers={'Origin':origin,'X-Idempotency-Key':uuid.uuid4().hex})
            workspace=json.loads(body).get('data',{}).get('workspace',{}).get('id')
            check(status==200 and bool(workspace) and no_store(headers),'real_same_origin_mutation_preserves_host_port')
            status,headers,body=request('/api/agriculture/organizations','POST',
                {'name':'Must Not Create'},headers={'Origin':'https://attacker.invalid',
                'X-Forwarded-Host':'attacker.invalid','X-Idempotency-Key':uuid.uuid4().hex})
            check(status==403 and no_store(headers),'cross_origin_mutation_denied')
            for referer in [origin+'/',origin+'/agri/workspaces/deep/link']:
                status,headers,body=request('/api/auth/session',headers={'Referer':referer})
                check(status==200 and json.loads(body)['data']['user']['id']==actor and no_store(headers),'same_actor_session_'+referer.split(origin)[1])
            status,headers,body=request('/api/agriculture/workspaces')
            check(status==200 and no_store(headers) and workspace.encode() in body,'authenticated_workspace_api')
            status,headers,body=request('/api/agriculture/organizations','POST',
                {'name':'Wrong Port'},headers={'Origin':f'http://127.0.0.1:{port+1}',
                'X-Idempotency-Key':uuid.uuid4().hex})
            check(status==403 and no_store(headers),'wrong_origin_port_denied')
            forged='open_ai_canvas_session='+secrets.token_hex(32)
            status,headers,body=request('/api/auth/session',headers={'Cookie':forged},authenticated=False)
            check(status==200 and json.loads(body).get('data',{}).get('user') is None and no_store(headers),'forged_cookie_is_anonymous')
            status,headers,body=request('/api/agriculture/workspaces',headers={'Cookie':forged},authenticated=False)
            check(status==401 and no_store(headers),'forged_cookie_protected_api_denied')
            status,headers,body=request('/api/auth/logout','POST',{})
            check(status==200 and no_store(headers),'real_logout')
            status,headers,body=request('/api/auth/session',headers={'Cookie':'open_ai_canvas_session='+previous_cookie},authenticated=False)
            check(status==200 and json.loads(body).get('data',{}).get('user') is None,'old_cookie_replay_is_anonymous')
            status,headers,body=request('/api/agriculture/workspaces',headers={'Cookie':'open_ai_canvas_session='+previous_cookie},authenticated=False)
            check(status==401 and no_store(headers),'old_cookie_protected_api_denied')
            status,headers,body=request('/api/auth/login','POST',{'username':username,'password':password})
            check(status==200 and any(c.name=='open_ai_canvas_session' and c.value!=previous_cookie for c in jar),'real_relogin_new_cookie')
            backend.terminate();backend.wait(timeout=10)
            query_probe='never_log_'+secrets.token_hex(12)
            status,headers,body=request('/api/auth/session?probe='+query_probe)
            check(status==502 and no_store(headers) and body==(static/'_agri_errors/50x.html').read_bytes(),'upstream_failure_real_error_no_store')
            log.flush()
            check(query_probe not in (run/'private-process.log').read_text(),'gateway_logs_do_not_include_query_values')
            report['passed']=True
            code=0
        except Exception as error:
            report['passed']=False
            report['failure_type']=type(error).__name__
            # Assertion labels contain only fixed paths, never user/session values.
            if isinstance(error,AssertionError): report['failure_check']=str(error)
        finally:
            for process in reversed(processes):
                if process.poll() is None:
                    process.terminate()
                    try: process.wait(timeout=10)
                    except subprocess.TimeoutExpired: process.kill();process.wait()
            jar.clear()
    report['exit_code']=code
    report['completed_at']=datetime.now(timezone.utc).isoformat()
    (run/'report.json').write_text(json.dumps(report,indent=2)+'\n')
    print(json.dumps({'passed':report['passed'],'checks':len(results),'failure_check':report.get('failure_check'),'report':str(run/'report.json')}))
    return code


if __name__=='__main__':
    raise SystemExit(main())
