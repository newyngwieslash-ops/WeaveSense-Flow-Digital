#!/usr/bin/env node
// DT016 test fixture only: real browser cookie -> Gin -> signed GIS request.
// The test-only bridge is registered by the owning Go test, never by production.
import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { constants, closeSync, cpSync, existsSync, fstatSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { createInterface } from 'node:readline';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { connect, freePort, ready, stop } from './session-e2e.mjs';
import { renderGateway, requestLeaksInternalCredential } from './gateway-browser-e2e.mjs';
import { csrfFetch } from './csrf-fixture.mjs';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const BRIDGE = '/api/agriculture/_test/workbench-attributes';
const COOKIE = 'open_ai_canvas_session';
const hash = value => createHash('sha256').update(value).digest('hex');
const fileHash = path => hash(readFileSync(path));
const id = value => typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value);
const port = value => Number.isInteger(value) && value >= 1024 && value <= 65535;

export function validateConfig(value, configPath) {
    const keys = ['platform_url', 'gateway_port', 'username', 'password', 'workspace_id', 'foreign_workspace_id', 'second_workspace_id', 'operation_id', 'task_code', 'plot_code', 'parcel_id', 'crop_type', 'report_path'];
    if (!value || typeof value !== 'object' || Array.isArray(value) || Object.keys(value).sort().join() !== [...keys].sort().join()) throw new Error('invalid private fixture configuration');
    if (!/^http:\/\/127\.0\.0\.1:[0-9]+$/.test(value.platform_url)) throw new Error('invalid local platform origin');
    const platform = new URL(value.platform_url);
    if (!port(Number(platform.port)) || !port(value.gateway_port) || Number(platform.port) === value.gateway_port) throw new Error('invalid fixture port');
    for (const key of ['workspace_id', 'foreign_workspace_id', 'second_workspace_id', 'operation_id', 'task_code', 'plot_code', 'parcel_id']) if (!id(value[key])) throw new Error('invalid fixture identifier');
    if (typeof value.crop_type !== 'string' || !value.crop_type || value.crop_type.length > 64 || /[\u0000-\u001f\u007f]/.test(value.crop_type)) throw new Error('invalid fixture crop label');
    if (new Set([value.workspace_id, value.foreign_workspace_id, value.second_workspace_id]).size !== 3) throw new Error('fixture scopes must differ');
    if (typeof value.username !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(value.username) || typeof value.password !== 'string' || value.password.length < 12 || value.password.length > 256) throw new Error('invalid fixture account');
    if (typeof value.report_path !== 'string' || resolve(value.report_path) !== value.report_path || dirname(value.report_path) !== dirname(resolve(configPath)) || !/^[A-Za-z0-9_-]+\.json$/.test(value.report_path.split('/').at(-1)) || value.report_path === resolve(configPath)) throw new Error('report must stay in the owned fixture directory');
    return value;
}

export function readPrivateConfig(path) {
    const parent = statSync(dirname(resolve(path)));
    if (!parent.isDirectory() || (parent.mode & 0o777) !== 0o700 || (process.getuid && parent.uid !== process.getuid())) throw new Error('owned private fixture directory required');
    const descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW);
    try {
        const stat = fstatSync(descriptor);
        if (!stat.isFile() || (stat.mode & 0o777) !== 0o600 || stat.size > 16384 || stat.nlink !== 1 || (process.getuid && stat.uid !== process.getuid())) throw new Error('private fixture config permissions invalid');
        return validateConfig(JSON.parse(readFileSync(descriptor, 'utf8')), path);
    } finally { closeSync(descriptor); }
}

export function continuation(line) {
    if (typeof line !== 'string' || line.length > 128) return false;
    try { const message = JSON.parse(line); return Boolean(message && Object.keys(message).join() === 'event' && message.event === 'continue_revoked'); } catch { return false; }
}

export function safeIdentityFailure(error) {
    return /^assertion failed: [a-z0-9_]+$/.test(error?.message || '') ? error.message : 'identity chain browser fixture failed; private diagnostics withheld';
}

export function validNetworkEvidence(value) {
    return Boolean(value && Number.isSafeInteger(value.observed) && value.observed > 0 && value.violations === 0);
}

export function safeAPIStatuses(values) {
    const allowed = ['/api/auth/session', '/api/auth/login', '/api/auth/logout', '/api/auth/settings', '/api/agriculture/workspaces'];
    return Array.isArray(values) ? values.slice(-80).filter(row => row && allowed.includes(row.path) && Number.isInteger(row.status) && row.status >= 100 && row.status <= 599 && typeof row.completed === 'boolean').map(row => ({ path: row.path, status: row.status, completed: row.completed })) : [];
}

function parentContinuation(input) {
    const lines = createInterface({ input, crlfDelay: Infinity });
    let released = false, failed = false, waiter;
    lines.on('line', line => {
        if (released || !continuation(line)) { failed = true; waiter?.reject(new Error('invalid parent continuation')); return; }
        released = true; waiter?.resolve();
    });
    lines.on('close', () => { if (!released) { failed = true; waiter?.reject(new Error('parent closed before revocation')); } });
    return { wait: () => new Promise((resolve, reject) => {
        if (failed) { reject(new Error('invalid parent continuation')); return; }
        if (released) { resolve(); return; }
        const timer = setTimeout(() => reject(new Error('parent revocation deadline expired')), 120000);
        waiter = { resolve: () => { clearTimeout(timer); resolve(); }, reject: error => { clearTimeout(timer); reject(error); } };
    }), close: () => lines.close() };
}

async function observeBrowser(portNumber, origin) {
    const targets = await (await fetch(`http://127.0.0.1:${portNumber}/json/list`)).json();
    const target = targets.find(value => value.type === 'page');
    if (!target) throw new Error('browser page unavailable');
    const socket = new WebSocket(target.webSocketDebuggerUrl);
    await new Promise((resolve, reject) => { socket.addEventListener('open', resolve, { once: true }); socket.addEventListener('error', reject, { once: true }); });
    let observed = 0, violations = 0;
    socket.addEventListener('message', event => {
        const message = JSON.parse(event.data);
        if (message.method === 'Network.requestWillBeSent') {
            ++observed;
            if (requestLeaksInternalCredential(message.params.request, origin)) ++violations;
        }
    });
    await new Promise((resolve, reject) => {
        const timeout = setTimeout(() => reject(new Error('browser observer unavailable')), 10000);
        const response = event => { const message = JSON.parse(event.data); if (message.id === 1) { clearTimeout(timeout); socket.removeEventListener('message', response); message.error ? reject(new Error('browser observer rejected')) : resolve(); } };
        socket.addEventListener('message', response);
        socket.send(JSON.stringify({ id: 1, method: 'Network.enable' }));
    });
    return { counts: () => ({ observed, violations }), close: () => socket.close() };
}

async function browserWrite(cdp, body, withCSRF = true) {
    // Only returned status/known code/correlation survive the CDP evaluation.
    return cdp.evaluate(`(async () => {
        const headers = { 'Content-Type': 'application/json' };
        if (${JSON.stringify(withCSRF)}) {
            const csrf = await fetch('/api/auth/csrf', { credentials: 'same-origin', cache: 'no-store' });
            const envelope = await csrf.json();
            if (!csrf.ok || envelope.code !== 0 || !/^[a-f0-9]{64}$/.test(envelope.data?.token || '')) return { csrf: false };
            headers['X-CSRF-Token'] = envelope.data.token;
        }
        const response = await fetch(${JSON.stringify(BRIDGE)}, { method: 'POST', credentials: 'same-origin', headers, body: ${JSON.stringify(JSON.stringify(body))} });
        const envelope = await response.json();
        const operationMatches = envelope.data?.operationId === ${JSON.stringify(body.operationId)};
        const secretLeak = /(?:private[_-]?(?:seed|key)|weavesense-agri-(?:service|delegation)\\+jws|eyJ[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+\\.[A-Za-z0-9_-]+)/i.test(JSON.stringify(envelope));
        const reason = ['forbidden', 'unauthenticated', 'csrf_invalid', 'origin_denied', 'invalid_argument', 'resource_unavailable', 'resource_not_found', 'not_found'].includes(envelope.reason) ? envelope.reason : null;
        return { status: response.status, code: envelope.code, csrf: true, operationMatches, secretLeak, reason };
    })()`, 'authorized chain request');
}

export async function main(configPath = process.argv[2]) {
    const report = { schema_version: '1.0', result: 'not_run', stage: 'preflight', results: [], limitations: ['Test-only orchestration bridge, not a completed farm-editing UI', 'Fixture data only; no farm, external provider or production deployment acceptance', 'Browser uses actual React form and production nginx; internal service credentials never enter browser'] };
    let config, temp, nginx, browser, cdp, observer, control;
    let cleanupPromise;
    const cleanup = () => cleanupPromise ||= (async () => {
        control?.close(); observer?.close(); cdp?.close();
        await Promise.all([stop(browser), stop(nginx)]);
        if (temp) rmSync(temp, { recursive: true, force: true });
    })();
    const interrupted = () => {
        report.result = 'fail'; report.failure = 'owned fixture interrupted';
        void cleanup().finally(() => process.exit(1));
    };
    process.once('SIGTERM', interrupted); process.once('SIGINT', interrupted);
    const check = (condition, name) => { if (!condition) throw new Error(`assertion failed: ${name}`); report.results.push({ name, result: 'pass' }); };
    try {
        if (process.getuid?.() === 0 || !configPath) throw new Error('non-root owned configuration required');
        config = readPrivateConfig(configPath);
        if (existsSync(config.report_path)) throw new Error('report already exists');
        control = parentContinuation(process.stdin);
        const chrome = process.env.CHROME_BIN || '/usr/bin/chromium';
        const nginxBin = process.env.NGINX_BIN || '/usr/sbin/nginx';
        const mimeTypes = resolve(process.env.NGINX_MIME_TYPES || '/etc/nginx/mime.types');
        const react = resolve(process.env.AGRI_E2E_REACT_DIST || join(ROOT, 'web/dist-agriculture'));
        const vue = resolve(process.env.AGRI_E2E_VUE_DIST || join(ROOT, 'services/agri-gis/frontend/dist-agriculture'));
        const template = join(ROOT, 'deploy/agriculture/nginx.conf.template');
        for (const file of [chrome, nginxBin, mimeTypes, template, join(react, 'index.html'), join(vue, 'index.html')]) if (!existsSync(file)) throw new Error('required executable or built artifact missing');
        report.artifact_hashes = { nginx_template: fileHash(template), react_index: fileHash(join(react, 'index.html')), vue_index: fileHash(join(vue, 'index.html')) };
        report.source_commit = process.env.AGRI_E2E_SOURCE_COMMIT || spawnSync('git', ['rev-parse', 'HEAD'], { cwd: ROOT, encoding: 'utf8' }).stdout.trim();
        if (!/^[a-f0-9]{40}$/.test(report.source_commit)) throw new Error('source provenance missing');
        const provenance = join(ROOT, '.local/evidence/session-ci-provenance.json');
        if (existsSync(provenance)) { if (JSON.parse(readFileSync(provenance, 'utf8')).source_commit !== report.source_commit) throw new Error('source provenance mismatch'); report.ci_provenance_sha256 = fileHash(provenance); }
        report.operation_correlation_sha256 = hash(config.operation_id);
        temp = mkdtempSync(join(tmpdir(), 'agri-identity-chain-browser-'));
        cpSync(react, join(temp, 'html'), { recursive: true });
        cpSync(vue, join(temp, 'html/agri'), { recursive: true });
        cpSync(join(ROOT, 'deploy/agriculture/errors'), join(temp, 'html/_agri_errors'), { recursive: true });
        for (const name of ['client', 'proxy', 'fastcgi', 'uwsgi', 'scgi']) mkdirSync(join(temp, name));
        const origin = `http://127.0.0.1:${config.gateway_port}`;
        const nginxConfig = join(temp, 'nginx.conf');
        writeFileSync(nginxConfig, renderGateway(readFileSync(template, 'utf8'), { directory: temp, port: config.gateway_port, apiPort: Number(new URL(config.platform_url).port), mimeTypes }), { mode: 0o600 });
        check(spawnSync(nginxBin, ['-t', '-p', temp + '/', '-c', nginxConfig], { stdio: 'ignore' }).status === 0, 'production_nginx_configuration_valid');
        nginx = spawn(nginxBin, ['-p', temp + '/', '-c', nginxConfig, '-g', 'daemon off;'], { stdio: 'ignore', env: { PATH: process.env.PATH } });
        await ready(origin + '/api/auth/settings', nginx, 30000);
        const chromePort = await freePort();
        browser = spawn(chrome, ['--headless=new', '--disable-background-networking', '--no-first-run', '--remote-debugging-port=' + chromePort, '--user-data-dir=' + join(temp, 'chrome'), 'about:blank'], { stdio: 'ignore', env: { PATH: process.env.PATH, HOME: temp, XDG_CONFIG_HOME: join(temp, 'config'), XDG_CACHE_HOME: join(temp, 'cache') } });
        await ready(`http://127.0.0.1:${chromePort}/json/version`, browser, 30000);
        cdp = await connect(chromePort, origin);
        observer = await observeBrowser(chromePort, origin);
        const payload = { workspaceId: config.workspace_id, taskCode: config.task_code, plotCode: config.plot_code, cropType: config.crop_type, operationId: config.operation_id };
        report.stage = 'anonymous_request';
        await cdp.navigate('/login');
        await cdp.poll("!!document.querySelector('#login-account')", 'real login form');
        const anonymous = await browserWrite(cdp, payload);
        check(anonymous.csrf && anonymous.status === 401 && !anonymous.secretLeak, 'anonymous_browser_write_rejected');
        report.stage = 'real_login_navigation';
        await cdp.navigate('/login?next=' + encodeURIComponent('/agri/workspaces/' + config.workspace_id));
        report.stage = 'real_login_account';
        await cdp.fill('#login-account', config.username, 'fixture account');
        report.stage = 'real_login_password';
        await cdp.fill('#login-password', config.password, 'fixture password');
        report.stage = 'real_login_submit';
        await cdp.evaluate("document.querySelector('form button[type=submit]').click()", 'submit real login form');
        report.stage = 'real_login_confirmed_workspace';
        await cdp.poll("location.pathname === " + JSON.stringify('/agri/workspaces/' + config.workspace_id) + " && document.querySelector('main[data-session-status=ready]') && document.querySelector('[data-testid=agri-workspace-context]')?.dataset.workspaceId === " + JSON.stringify(config.workspace_id), 'trusted Vue workspace after real login');
        check(true, 'real_react_form_login_and_trusted_vue_workspace');
        const cookies = (await cdp.send('Network.getCookies', { urls: [origin] })).cookies;
        const session = cookies.find(cookie => cookie.name === COOKIE);
        check(Boolean(session?.httpOnly && session.value), 'platform_session_cookie_is_httponly');
        const cookie = `${COOKIE}=${session.value}`;
        report.stage = 'authorized_chain_write';
        const success = await browserWrite(cdp, payload);
        check(success.status === 200 && success.code === 0 && success.csrf && success.operationMatches && !success.secretLeak, 'browser_to_gis_single_authorized_write');
        report.stage = 'scope_and_identity_negatives';
        for (const [name, body, status] of [
            ['foreign_tenant_workspace_rejected', { ...payload, workspaceId: config.foreign_workspace_id }, 404],
            ['same_tenant_other_workspace_rejected', { ...payload, workspaceId: config.second_workspace_id }, 404],
            ['forged_operator_body_rejected', { ...payload, operatorCode: 'forged-actor' }, 400],
            ['forged_user_body_rejected', { ...payload, user_id: 'forged-actor' }, 400],
            ['forged_tenant_body_rejected', { ...payload, tenant_id: 'forged-tenant' }, 400],
        ]) {
            const result = await browserWrite(cdp, body);
            check(result.csrf && result.status === status && !result.secretLeak, name);
        }
        const noCSRF = await browserWrite(cdp, payload, false);
        check(noCSRF.status === 403 && noCSRF.reason === 'csrf_invalid', 'browser_write_without_csrf_rejected');
        // HTTP negative fixtures, not a claim that browser JavaScript can spoof Origin/Cookie.
        const crossSite = await csrfFetch(origin + BRIDGE, { method: 'POST', headers: { Cookie: cookie, Origin: 'https://attacker.invalid', 'Content-Type': 'application/json' }, body: JSON.stringify(payload) });
        check(crossSite.status === 403 && (await crossSite.json()).reason === 'origin_denied', 'cross_origin_http_write_rejected');
        report.stage = 'awaiting_real_platform_revocation';
        process.stdout.write(JSON.stringify({ event: 'phase1_done' }) + '\n');
        await control.wait();
        report.stage = 'same_cookie_after_revocation';
        const after = (await cdp.send('Network.getCookies', { urls: [origin] })).cookies.find(value => value.name === COOKIE);
        check(after?.value === session.value, 'same_browser_cookie_no_relogin_after_revocation');
        const revoked = await browserWrite(cdp, payload);
        check(revoked.csrf && revoked.status === 403 && revoked.reason === 'forbidden' && !revoked.secretLeak, 'revoked_same_browser_write_denied_not_csrf_failure');
        report.network = observer.counts();
        check(validNetworkEvidence(report.network) && cdp.exceptions.length === 0 && cdp.outboundBlocked() === 0, 'no_browser_internal_credentials_or_external_requests');
        report.stage = 'complete'; report.result = 'pass';
    } catch (error) {
        report.result = 'fail'; report.failure = safeIdentityFailure(error); process.exitCode = 1;
        if (cdp) {
            report.api_statuses = safeAPIStatuses(cdp.statuses);
            report.browser_runtime_exception_count = cdp.exceptions.length;
            try {
                report.dom_flags = await cdp.evaluate("({loginRoute:location.pathname==='/login',agriRoute:location.pathname.startsWith('/agri/'),documentComplete:document.readyState==='complete',accountInput:!!document.querySelector('#login-account'),passwordInput:!!document.querySelector('#login-password'),submitButton:!!document.querySelector('form button[type=submit]'),actorVisible:!!document.querySelector('[data-testid=session-user]'),vueLoading:!!document.querySelector('main[data-session-status=loading]'),vueReady:!!document.querySelector('main[data-session-status=ready]'),vueError:!!document.querySelector('main[data-session-status=error]'),alertVisible:!!document.querySelector('[role=alert]')})", 'sanitized failure flags');
            } catch { /* No raw CDP exception text or DOM content is retained. */ }
        }
    }
    finally {
        await cleanup();
        process.removeListener('SIGTERM', interrupted); process.removeListener('SIGINT', interrupted);
        if (config && !existsSync(config.report_path)) writeFileSync(config.report_path, JSON.stringify(report, null, 2) + '\n', { flag: 'wx', mode: 0o600 });
        process.stdout.write(JSON.stringify({ event: report.result === 'pass' ? 'done' : 'failed', stage: report.stage }) + '\n');
    }
    return report;
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await main();
