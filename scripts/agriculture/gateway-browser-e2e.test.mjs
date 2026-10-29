import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { reloadDocument, waitForWorkspaceRevalidation, renderGateway, requestLeaksInternalCredential, safeGatewayFailure } from './gateway-browser-e2e.mjs';

const template = readFileSync(new URL('../../deploy/agriculture/nginx.conf.template', import.meta.url), 'utf8');
const options = { directory: '/tmp/agri-fixture-123', port: 18090, apiPort: 18091, mimeTypes: '/etc/nginx/mime.types' };

test('reload cannot pass on old same-URL document or another frame', async () => {
    for (const replacement of [{ id: 'main', loaderId: 'old' }, { id: 'other', loaderId: 'new' }, { id: 'main', loaderId: '' }]) {
        let reads = 0, evaluated = 0;
        await assert.rejects(reloadDocument({ timeout: 15,
            send: async method => method === 'Page.reload' ? {} : { frameTree: { frame: { ...(reads++ === 0 ? { id: 'main', loaderId: 'old' } : replacement), url: 'http://127.0.0.1/agri/workspaces/a' } } },
            evaluate: async () => { evaluated++; return true; },
        }), /reload did not commit/);
        assert.equal(evaluated, 0);
    }
});

test('reload waits for new loader then complete current document', async () => {
    let reads = 0, evaluated = 0, reloads = 0;
    await reloadDocument({
        send: async method => {
            if (method === 'Page.reload') { reloads++; return {}; }
            return { frameTree: { frame: { id: 'main', loaderId: ++reads <= 2 ? 'old' : 'new', url: 'http://127.0.0.1/agri/workspaces/a' } } };
        },
        evaluate: async expression => { assert.ok(reads >= 3); assert.match(expression, /document.readyState/); return ++evaluated === 2; },
    });
    assert.equal(reloads, 1);
    assert.equal(evaluated, 2);
});

test('Back cannot pass from prior completed or incomplete workspace responses', async () => {
    const statuses = [{ path: '/api/agriculture/workspaces', status: 200, completed: true }];
    await assert.rejects(waitForWorkspaceRevalidation(statuses, 1, 15), /fresh workspace/);
    statuses.push({ path: '/api/agriculture/workspaces', status: 200, completed: false });
    await assert.rejects(waitForWorkspaceRevalidation(statuses, 1, 15), /fresh workspace/);
    statuses[1].completed = true;
    await waitForWorkspaceRevalidation(statuses, 1, 15);
    // Taking the boundary at request start excludes even late completion of an
    // earlier page's in-flight request.
    const old = [{ path: '/api/agriculture/workspaces', status: 200, completed: false }];
    const boundary = old.length;
    old[0].completed = true;
    await assert.rejects(waitForWorkspaceRevalidation(old, boundary, 15), /fresh workspace/);
});

test('renders actual production nginx template without replacing its routing policy', () => {
    const rendered = renderGateway(template, options);
    assert.ok(rendered.includes('127.0.0.1:18090'));
    assert.ok(rendered.includes('127.0.0.1:18091'));
    assert.ok(rendered.includes('/tmp/agri-fixture-123/html'));
    assert.ok(!rendered.includes('${AGRI_'));
    const locationLines = value => value.split('\n').filter(line => /location\s/.test(line));
    assert.deepEqual(locationLines(rendered), locationLines(template));
});

test('configuration injection and nonlocal fixture ports fail closed', () => {
    for (const directory of ['/tmp/a;evil', '/tmp/a b', '/tmp/a\ninclude', '/tmp/$secret', '/tmp/"x', 'relative']) {
        assert.throws(() => renderGateway(template, { ...options, directory }));
    }
    for (const port of [0, 80, 65536, '18090', 1.5]) assert.throws(() => renderGateway(template, { ...options, port }));
    assert.throws(() => renderGateway(template + '${AGRI_UNKNOWN}', options));
});

test('network observer detects service auth without retaining request bytes', () => {
    const origin = 'http://127.0.0.1:18090';
    const good = { url: origin + '/api/auth/login', headers: { 'Content-Type': 'application/json', Cookie: 'platform-session-fixture' }, postData: '{"username":"fixture","password":"fixture"}' };
    assert.equal(requestLeaksInternalCredential(good, origin), false);
    for (const request of [
        { ...good, url: 'https://external.invalid/' },
        { ...good, url: origin + '/internal/agri/v1/ownership/resolve' },
        { ...good, headers: { Authorization: 'Bearer fixture' } },
        { ...good, headers: { 'X-Service-Token': 'fixture' } },
        { ...good, postData: '{"private_seed":"fixture"}' },
        { ...good, url: origin + '/?private_key=fixture' },
    ]) assert.equal(requestLeaksInternalCredential(request, origin), true);
});

test('arbitrary errors never serialize fixture passwords or cookies', () => {
    assert.equal(safeGatewayFailure(new Error('assertion failed: deep_link')), 'assertion failed: deep_link');
    for (const error of [new Error('Cookie=session_secret password=secret'), new Error('https://host.invalid/?token=secret'), null]) {
        assert.equal(safeGatewayFailure(error), 'gateway browser step unavailable or failed; raw diagnostics withheld');
    }
});

test('runner uses actual nginx, no node proxy or sandbox-disable workaround', () => {
    const source = readFileSync(new URL('./gateway-browser-e2e.mjs', import.meta.url), 'utf8');
    assert.ok(source.includes("spawn(nginxBin"));
    assert.ok(source.includes("deploy/agriculture/nginx.conf.template"));
    assert.ok(!source.includes('startProxy'));
    assert.ok(!source.includes('--no-sandbox'));
    assert.ok(!source.includes('--disable-setuid-sandbox'));
    assert.ok(source.includes('import.meta.url === pathToFileURL'));
});
