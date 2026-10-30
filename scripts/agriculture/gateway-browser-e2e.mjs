#!/usr/bin/env node
// Actual production nginx + built React/Vue + Go SQLite + Chromium. No API mocks.
import { spawn, spawnSync } from 'node:child_process';
import { createHash, randomBytes } from 'node:crypto';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { connect, freePort, ready, stop } from './session-e2e.mjs';
const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '../..');
const digest = path => createHash('sha256').update(readFileSync(path)).digest('hex');

export async function reloadDocument({ send, evaluate, timeout = 20000 }) {
    const previous = (await send('Page.getFrameTree'))?.frameTree?.frame;
    if (!previous?.id || !previous.loaderId || !previous.url) throw new Error('reload requires current document loader');
    await send('Page.reload', { ignoreCache: true });
    const deadline = Date.now() + timeout;
    while (Date.now() < deadline) {
        try {
            const current = (await send('Page.getFrameTree'))?.frameTree?.frame;
            if (current?.id === previous.id && typeof current.loaderId === 'string' && current.loaderId
                && current.loaderId !== previous.loaderId && current.url === previous.url
                && await evaluate(`document.readyState === 'complete' && location.href === ${JSON.stringify(previous.url)} && performance.getEntriesByType('navigation')[0]?.name === ${JSON.stringify(previous.url)}`)) return;
        } catch { /* a genuine reload can replace the execution context */ }
        await new Promise(resolve => setTimeout(resolve, 10));
    }
    throw new Error('reload did not commit a new complete document');
}

export async function waitForWorkspaceRevalidation(statuses, firstNewResponse, timeout = 20000) {
    const deadline = Date.now() + timeout;
    while (Date.now() < deadline) {
        if (statuses.slice(firstNewResponse).some(row => row.path === '/api/agriculture/workspaces' && row.status === 200 && row.completed)) return;
        await new Promise(resolve => setTimeout(resolve, 10));
    }
    throw new Error('back navigation did not complete a fresh workspace response');
}

export function renderGateway(template, { directory, port, apiPort, mimeTypes }) {
    if (![port, apiPort].every(p => Number.isInteger(p) && p >= 1024 && p <= 65535)) throw new Error('invalid local fixture port');
    if (![directory, mimeTypes].every(p => typeof p === 'string' && p.startsWith('/') && !/[\s;{}$"'\\]/.test(p))) throw new Error('unsafe nginx fixture path');
    const replacements = { '${AGRI_LISTEN}': `127.0.0.1:${port}`, '${AGRI_PLATFORM_UPSTREAM}': `127.0.0.1:${apiPort}`, '${AGRI_STATIC_ROOT}': join(directory, 'html'), '/etc/nginx/mime.types': mimeTypes, '/tmp/agriculture-nginx.pid': join(directory, 'nginx.pid') };
    for (const name of ['client', 'proxy', 'fastcgi', 'uwsgi', 'scgi']) replacements[`/tmp/agriculture-${name}`] = join(directory, name);
    for (const [key, value] of Object.entries(replacements)) template = template.replaceAll(key, value);
    if (template.includes('${AGRI_')) throw new Error('unresolved nginx fixture placeholder');
    return template;
}

export function safeGatewayFailure(error) {
    return /^assertion failed: [a-z0-9_]+$/.test(error?.message || '') ? error.message : 'gateway browser step unavailable or failed; raw diagnostics withheld';
}

export function requestLeaksInternalCredential(request, origin) {
    let url;
    try { url = new URL(request.url); } catch { return true; }
    if (['data:', 'blob:', 'about:'].includes(url.protocol)) return false;
    if (url.origin !== origin || url.pathname.startsWith('/internal/')) return true;
    const headers = Object.entries(request.headers || {});
    if (headers.some(([key]) => /^(authorization|x-agri-delegation|x-service-token|x-service-identity)$/i.test(key))) return true;
    // Inspect transient bytes only; never retain cookie/header/body values.
    return /(?:private_seed|private_key|weavesense-agri-(?:service|delegation)\+jws)/i.test(request.url + '\n' + JSON.stringify(headers) + '\n' + (request.postData || ''));
}

async function watchRequests(port, origin) {
    const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
    const socket = new WebSocket(targets.find(t => t.type === 'page').webSocketDebuggerUrl);
    await new Promise((ok, no) => { socket.addEventListener('open', ok, { once: true }); socket.addEventListener('error', no, { once: true }); });
    let violations = 0, observed = 0;
    const workspaceRequests = [], pendingWorkspaces = new Map();
    socket.addEventListener('message', event => {
        const message = JSON.parse(event.data);
        if (message.method === 'Network.requestWillBeSent') {
            observed++;
            if (requestLeaksInternalCredential(message.params.request, origin)) violations++;
            const url = new URL(message.params.request.url);
            if (url.origin === origin && url.pathname === '/api/agriculture/workspaces') {
                const row = { path: '/api/agriculture/workspaces', status: null, completed: false };
                workspaceRequests.push(row);
                pendingWorkspaces.set(message.params.requestId, row);
            }
        } else if (message.method === 'Network.responseReceived') {
            const row = pendingWorkspaces.get(message.params.requestId);
            if (row) row.status = message.params.response.status;
        } else if (message.method === 'Network.loadingFinished') {
            const row = pendingWorkspaces.get(message.params.requestId);
            if (row) { row.completed = true; pendingWorkspaces.delete(message.params.requestId); }
        } else if (message.method === 'Network.loadingFailed') {
            pendingWorkspaces.delete(message.params.requestId);
        }
    });
    await new Promise((ok, no) => {
        const timer = setTimeout(() => no(new Error('network observer unavailable')), 10000);
        const listener = event => { const message = JSON.parse(event.data); if (message.id === 1) { clearTimeout(timer); socket.removeEventListener('message', listener); message.error ? no(new Error('network observer unavailable')) : ok(); } };
        socket.addEventListener('message', listener);
        socket.send(JSON.stringify({ id: 1, method: 'Network.enable', params: {} }));
    });
    return { counts: () => ({ observed, violations }), workspaceRequests, close: () => socket.close() };
}

export async function main() {
    const report = { schema_version: '1.0', result: 'not_run', stage: 'preflight', command: 'node scripts/agriculture/gateway-browser-e2e.mjs', results: [], limitations: ['Local SQLite fixture is not PostgreSQL RLS or live farm acceptance', 'No external email, provider, payment or deployment', 'Browser sandbox remains enabled; unsupported environment fails, never substitutes a mocked browser'] };
    let temp, backend, nginx, browser, cdp, observer;
    const check = (condition, name) => { if (!condition) throw new Error(`assertion failed: ${name}`); report.results.push({ name, result: 'pass' }); };
    try {
        if (process.getuid?.() === 0) throw new Error('non-root fixture execution required');
        const binary = resolve(process.env.AGRI_E2E_BINARY || join(ROOT, '.local/bin/agriculture-platform'));
        const chrome = process.env.CHROME_BIN || '/usr/bin/chromium';
        const nginxBin = process.env.NGINX_BIN || '/usr/sbin/nginx';
        const mimeTypes = resolve(process.env.NGINX_MIME_TYPES || '/etc/nginx/mime.types');
        const react = resolve(process.env.AGRI_E2E_REACT_DIST || join(ROOT, 'web/dist-agriculture'));
        const vue = resolve(process.env.AGRI_E2E_VUE_DIST || join(ROOT, 'services/agri-gis/frontend/dist-agriculture'));
        const template = join(ROOT, 'deploy/agriculture/nginx.conf.template');
        for (const file of [binary, chrome, nginxBin, mimeTypes, join(react, 'index.html'), join(vue, 'index.html'), template]) if (!existsSync(file)) throw new Error('required executable or production artifact missing');
        report.artifact_hashes = { backend: digest(binary), nginx_template: digest(template), react_index: digest(join(react, 'index.html')), vue_index: digest(join(vue, 'index.html')) };
        report.checkout_head = spawnSync('git', ['rev-parse', 'HEAD'], { cwd: ROOT, encoding: 'utf8' }).stdout.trim();
        report.source_commit = process.env.AGRI_E2E_SOURCE_COMMIT || report.checkout_head;
        if (!/^[a-f0-9]{40}$/.test(report.source_commit)) throw new Error('invalid source provenance');
        report.working_tree_changes_included = Boolean(spawnSync('git', ['status', '--porcelain'], { cwd: ROOT, encoding: 'utf8' }).stdout.trim());
        const provenance = join(ROOT, '.local/evidence/session-ci-provenance.json');
        if (existsSync(provenance)) {
            if (JSON.parse(readFileSync(provenance, 'utf8')).source_commit !== report.source_commit) throw new Error('source provenance mismatch');
            report.ci_provenance_sha256 = digest(provenance);
        }
        temp = mkdtempSync(join(tmpdir(), 'agri-gateway-browser-'));
        cpSync(react, join(temp, 'html'), { recursive: true });
        cpSync(vue, join(temp, 'html/agri'), { recursive: true });
        cpSync(join(ROOT, 'deploy/agriculture/errors'), join(temp, 'html/_agri_errors'), { recursive: true });
        for (const name of ['client', 'proxy', 'fastcgi', 'uwsgi', 'scgi', 'backend']) mkdirSync(join(temp, name));
        const apiPort = await freePort(), port = await freePort(), chromePort = await freePort();
        const origin = `http://127.0.0.1:${port}`;
        const config = join(temp, 'nginx.conf');
        writeFileSync(config, renderGateway(readFileSync(template, 'utf8'), { directory: temp, port, apiPort, mimeTypes }));
        check(spawnSync(nginxBin, ['-t', '-p', temp + '/', '-c', config], { stdio: 'ignore' }).status === 0, 'production_nginx_configuration_valid');
        report.stage = 'real_platform_startup';
        backend = spawn(binary, [], { cwd: join(ROOT, 'backend'), env: { PATH: process.env.PATH, HOME: temp, GIN_MODE: 'release', CANVAS_DATABASE_DRIVER: 'sqlite', CANVAS_BACKEND_DATA_DIR: join(temp, 'backend'), CANVAS_BACKEND_ADDR: `127.0.0.1:${apiPort}`, CANVAS_AUTO_MIGRATE: 'true', CANVAS_CORS_ORIGINS: origin, FLOW_AGRICULTURE_ENABLED: 'true', FLOW_AGRICULTURE_ALLOW_SQLITE_DEV: 'true' }, stdio: 'ignore' });
        nginx = spawn(nginxBin, ['-p', temp + '/', '-c', config, '-g', 'daemon off;'], { stdio: 'ignore' });
        await ready(`${origin}/api/auth/settings`, backend);
        check((await fetch(origin + '/api/agriculture/workspaces')).status === 401, 'anonymous_api_denied');
        const username = 'gateway_' + randomBytes(6).toString('hex'), password = randomBytes(24).toString('base64url');
        const registration = await fetch(origin + '/api/auth/register', { method: 'POST', headers: { 'content-type': 'application/json', Origin: origin }, body: JSON.stringify({ username, password, displayName: 'Local Gateway Fixture', acceptedTerms: true }) });
        const cookie = registration.headers.getSetCookie().find(value => value.startsWith('open_ai_canvas_session='))?.split(';')[0];
        check(registration.ok && Boolean(cookie), 'reserved_local_fixture_registration');
        const organization = await fetch(origin + '/api/agriculture/organizations', { method: 'POST', headers: { 'content-type': 'application/json', Origin: origin, Cookie: cookie, 'X-Idempotency-Key': randomBytes(16).toString('hex') }, body: JSON.stringify({ name: 'Local Gateway Fixture Organization' }) });
        const workspace = (await organization.json()).data?.workspace?.id;
        check(organization.ok && typeof workspace === 'string', 'actual_authorized_workspace_created');
        report.stage = 'browser_startup';
        browser = spawn(chrome, ['--headless=new', '--disable-background-networking', '--no-first-run', `--remote-debugging-port=${chromePort}`, `--user-data-dir=${join(temp, 'chrome')}`, 'about:blank'], { env: { ...process.env, HOME: temp, XDG_CONFIG_HOME: join(temp, 'config'), XDG_CACHE_HOME: join(temp, 'cache') }, stdio: 'ignore' });
        await ready(`http://127.0.0.1:${chromePort}/json/version`, browser, 30000);
        cdp = await connect(chromePort, origin);
        observer = await watchRequests(chromePort, origin);
        const deep = `/agri/workspaces/${workspace}`;
        const selected = `document.querySelector('[data-testid=agri-workspace-context]')?.dataset.workspaceId === ${JSON.stringify(workspace)}`;
        const click = async (selector, label) => { await cdp.poll(`!!document.querySelector(${JSON.stringify(selector)})`, label); await cdp.evaluate(`document.querySelector(${JSON.stringify(selector)}).click()`, label); };
        const login = async () => {
            await cdp.fill('#login-account', username, 'fixture account');
            await cdp.fill('#login-password', password, 'fixture password');
            await click('form button[type=submit]', 'login submit');
            await cdp.poll(selected, 'server authorized deep context');
        };
        report.stage = 'deep_link_authentication';
        await cdp.navigate(deep);
        await cdp.poll("!!document.querySelector('[data-testid=agri-login]')", 'deep anonymous page');
        check(await cdp.evaluate(`document.querySelector('[data-testid=agri-login]').getAttribute('href') === ${JSON.stringify('/login?next=' + encodeURIComponent(deep))}`), 'deep_link_next_preserved');
        await click('[data-testid=agri-login]', 'platform login link');
        await cdp.poll("!!document.querySelector('#login-account')", 'login form');
        await click('nav[aria-label="账号操作"] a[href^="/register?"]', 'register interruption');
        await cdp.poll("location.pathname === '/register'", 'register route');
        check(await cdp.evaluate(`new URLSearchParams(location.search).get('next') === ${JSON.stringify(deep)}`), 'register_preserves_deep_return');
        await cdp.evaluate('history.back()', 'back to login');
        await cdp.poll("location.pathname === '/login' && !!document.querySelector('#login-account')", 'back login');
        await login();
        check(await cdp.evaluate(`location.pathname === ${JSON.stringify(deep)}`), 'real_login_returns_to_exact_vue_deep_path');
        await reloadDocument(cdp);
        await cdp.poll(selected, 'deep refresh retains authorized context');
        check(true, 'deep_refresh_through_production_nginx');
        await click('nav a[href="/organization"]', 'cross application organization');
        await cdp.poll("!!document.querySelector('[data-testid=organization-management]')", 'React organization loaded');
        const responsesBeforeBack = cdp.statuses.length;
        const requestsBeforeBack = observer.workspaceRequests.length;
        await cdp.evaluate('history.back()', 'back to Vue');
        await waitForWorkspaceRevalidation(cdp.statuses, responsesBeforeBack);
        // Also bind to request start, not only response arrival: a previous page's
        // in-flight request completing after Back cannot validate the restored tree.
        await waitForWorkspaceRevalidation(observer.workspaceRequests, requestsBeforeBack);
        await cdp.poll(selected, 'back revalidates Vue context');
        check(true, 'cross_application_and_back');
        await click('[data-testid=session-logout]', 'logout');
        await cdp.poll("!!document.querySelector('main[data-session-status=anonymous]')", 'confirmed logout');
        check(await cdp.evaluate("!document.querySelector('[data-testid=agri-workspace-context]')"), 'logout_clears_workspace');
        await click('[data-testid=agri-login]', 'relogin');
        await cdp.poll("!!document.querySelector('#login-account')", 'relogin form');
        await login();
        check(true, 'relogin_restores_exact_authorized_context');
        await cdp.navigate('/agri/workspaces/fixture_forbidden');
        await cdp.poll("!!document.querySelector('[data-route-error=WORKSPACE_FORBIDDEN]')", 'forbidden workspace');
        check(await cdp.evaluate("!document.querySelector('[data-testid=agri-workspace-context]')"), 'foreign_workspace_not_rendered');
        await cdp.navigate('/agri/unknown-route');
        await cdp.poll("!!document.querySelector('[data-route-error=ROUTE_NOT_FOUND]')", 'unknown route');
        check(await cdp.evaluate("!document.querySelector('[data-testid=session-user]')"), 'unknown_route_explicit_404_no_actor');
        report.network = observer.counts();
        check(report.network.observed > 0 && report.network.violations === 0, 'no_internal_credential_or_cross_origin_browser_requests');
        check(cdp.exceptions.length === 0 && cdp.outboundBlocked() === 0, 'no_browser_exceptions_or_outbound_requests');
        report.result = 'pass'; report.stage = 'complete';
    } catch (error) {
        report.result = ['preflight', 'browser_startup'].includes(report.stage) ? 'not_run' : 'fail';
        report.failure = safeGatewayFailure(error);
        process.exitCode = 1;
    } finally {
        observer?.close(); cdp?.close();
        await stop(browser); await stop(nginx); await stop(backend);
        if (temp) rmSync(temp, { recursive: true, force: true });
        const output = resolve(process.env.AGRI_GATEWAY_BROWSER_REPORT || join(ROOT, '.local/evidence/gateway-browser-e2e.json'));
        mkdirSync(dirname(output), { recursive: true });
        writeFileSync(output, JSON.stringify(report, null, 2) + '\n');
        console.log(JSON.stringify({ result: report.result, stage: report.stage, checks: report.results.length }));
    }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
    main().catch(() => { console.error('gateway browser cleanup/preflight failed; diagnostics withheld'); process.exitCode = 1; });
}
