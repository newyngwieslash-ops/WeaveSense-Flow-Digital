import test from 'node:test';
import assert from 'node:assert/strict';
import { chmodSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { continuation, readPrivateConfig, safeAPIStatuses, safeIdentityFailure, validateConfig, validNetworkEvidence } from './identity-chain-browser.mjs';

function fixture(directory) {
    return { platform_url: 'http://127.0.0.1:18081', gateway_port: 18082, username: 'chain_fixture', password: 'fixture-not-a-real-password', workspace_id: 'workspace_a', foreign_workspace_id: 'workspace_b', second_workspace_id: 'workspace_c', operation_id: 'operation_fixture', task_code: 'task_fixture', plot_code: 'plot_fixture', parcel_id: 'parcel_fixture', crop_type: '玉米', report_path: join(directory, 'browser-report.json') };
}

test('configuration allows only explicit literal-loopback fixture topology and fixed fields', () => {
    const good = fixture('/tmp/owned-fixture');
    assert.equal(validateConfig(good, '/tmp/owned-fixture/config.json'), good);
    for (const platform_url of ['https://example.invalid', 'http://localhost:18081', 'http://127.0.0.1:18081/path', 'http://127.0.0.1:18081?x=y', 'http://user@127.0.0.1:18081', 'http://127.0.0.1:80', 'http://127.0.0.1:65536']) assert.throws(() => validateConfig({ ...good, platform_url }, '/tmp/owned-fixture/config.json'));
    for (const gateway_port of [18081, 0, 80, 65536, 1.5, '18082']) assert.throws(() => validateConfig({ ...good, gateway_port }, '/tmp/owned-fixture/config.json'));
    for (const extra of [{ user_id: 'forged' }, { private_key: 'fixture' }, { bridge_url: '/alternate' }]) assert.throws(() => validateConfig({ ...good, ...extra }, '/tmp/owned-fixture/config.json'));
});

test('scope and report validation reject ambiguities and output path escape', () => {
    const good = fixture('/tmp/owned-fixture');
    for (const key of ['workspace_id', 'operation_id', 'task_code', 'plot_code', 'parcel_id']) assert.throws(() => validateConfig({ ...good, [key]: '../scope' }, '/tmp/owned-fixture/config.json'));
    assert.throws(() => validateConfig({ ...good, second_workspace_id: good.workspace_id }, '/tmp/owned-fixture/config.json'));
    for (const report_path of ['/tmp/elsewhere/report.json', '/tmp/owned-fixture/config.json', '/tmp/owned-fixture/../report.json', '/tmp/owned-fixture/report.log', 'relative.json']) assert.throws(() => validateConfig({ ...good, report_path }, '/tmp/owned-fixture/config.json'));
    for (const crop_type of ['', 'x'.repeat(65), '\n', null]) assert.throws(() => validateConfig({ ...good, crop_type }, '/tmp/owned-fixture/config.json'));
});

test('private configuration requires owned 0700 directory and 0600 regular non-symlink file', () => {
    const directory = mkdtempSync(join(tmpdir(), 'identity-chain-test-'));
    const path = join(directory, 'config.json');
    try {
        writeFileSync(path, JSON.stringify(fixture(directory)), { mode: 0o600 });
        assert.equal(readPrivateConfig(path).crop_type, '玉米');
        chmodSync(path, 0o644); assert.throws(() => readPrivateConfig(path)); chmodSync(path, 0o600);
        const alias = join(directory, 'alias.json'); symlinkSync(path, alias); assert.throws(() => readPrivateConfig(alias));
        chmodSync(directory, 0o755); assert.throws(() => readPrivateConfig(path)); chmodSync(directory, 0o700);
        writeFileSync(path, ' '.repeat(16385)); assert.throws(() => readPrivateConfig(path));
    } finally { chmodSync(directory, 0o700); rmSync(directory, { recursive: true, force: true }); }
});

test('revocation continuation is an exact secret-free parent protocol, never an authority claim', () => {
    assert.equal(continuation('{"event":"continue_revoked"}'), true);
    for (const value of ['{"event":"done"}', '{"event":"continue_revoked","role":"viewer"}', '{"event":"continue_revoked","user_id":"a"}', 'null', '[]', 'bad json', 'x'.repeat(129), null]) assert.equal(continuation(value), false);
});

test('failures never emit arbitrary credential, HTTP or database diagnostics', () => {
    assert.equal(safeIdentityFailure(new Error('assertion failed: same_cookie')), 'assertion failed: same_cookie');
    for (const value of ['password=fixture-secret', 'Cookie: sensitive', 'postgres://role:password@host/db', 'assertion failed: token=secret']) assert.equal(safeIdentityFailure(new Error(value)), 'identity chain browser fixture failed; private diagnostics withheld');
});

test('zero or missing network observations cannot prove credential non-exposure', () => {
    assert.equal(validNetworkEvidence({ observed: 12, violations: 0 }), true);
    for (const evidence of [null, {}, { observed: 0, violations: 0 }, { observed: -1, violations: 0 }, { observed: NaN, violations: 0 }, { observed: 0.5, violations: 0 }, { observed: 12 }, { observed: 12, violations: 1 }]) assert.equal(validNetworkEvidence(evidence), false);
});

test('API failure diagnostics keep only literal route classes and bounded status flags', () => {
    const good = { path: '/api/auth/login', status: 503, completed: true, response: 'password-secret' };
    assert.deepEqual(safeAPIStatuses([good, { ...good, path: '/api/auth/login?token=secret' }, { ...good, path: '/api/agriculture/workspaces/private-scope' }, { ...good, status: '503' }, { ...good, completed: 'true' }]), [{ path: '/api/auth/login', status: 503, completed: true }]);
    assert.equal(safeAPIStatuses(Array.from({ length: 100 }, () => good)).length, 80);
    assert.deepEqual(safeAPIStatuses(null), []);
});

test('runner uses production nginx, actual login, no cookie injection or sandbox bypass', () => {
    const source = readFileSync(new URL('./identity-chain-browser.mjs', import.meta.url), 'utf8');
    assert.ok(source.includes('deploy/agriculture/nginx.conf.template'));
    assert.ok(source.includes('spawn(nginxBin'));
    assert.ok(source.includes("cdp.fill('#login-password'"));
    assert.ok(source.includes('same_browser_cookie_no_relogin_after_revocation'));
    assert.ok(!source.includes('Network.setCookie'));
    assert.ok(!source.includes('startProxy'));
    assert.ok(!source.includes('--no-sandbox'));
    assert.ok(!source.includes('--disable-setuid-sandbox'));
    assert.ok(source.includes("await control.wait()"));
    assert.ok(source.includes("if (process.argv[1] && import.meta.url === pathToFileURL"));
});
