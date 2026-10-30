import { expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import axios, { AxiosError } from 'axios';
import { installAgricultureRequestSecurity } from '../src/features/agriculture/request-security';
import { publishSessionHint, SESSION_HINT_KEY, subscribeSessionHints, validSessionHint } from '../../contracts/agriculture/session-hints';
import { applyUserSession, beginSessionRevalidation } from '../src/features/agriculture/session';
import { appQueryClient } from '../src/lib/query-client';
import { useUserStore } from '../src/stores/use-user-store';
import { ConfirmationScope } from '../src/features/agriculture/confirmation-scope';
import { authoritySnapshot } from '../../contracts/agriculture/authority-snapshot';
import { acceptDisplayedAuthority, clearDisplayedAuthority, currentProjectionGeneration, displayedAuthority, needsSessionBootstrap, setRoutineChecking } from '../src/features/agriculture/routine-session';

test('protected confirmation holder is page-owned and structurally unmounted during revalidation', () => {
    // Architecture regression only; real portal lifetime is asserted by browser CI.
    const page = readFileSync(new URL('../src/features/agriculture/organization.tsx', import.meta.url), 'utf8');
    expect(page).toContain('const [modal, modalHolder] = Modal.useModal()');
    expect(page).toContain('{!checking && modalHolder}');
    expect(page).not.toContain('App.useApp()');
    expect(page).not.toContain('destroyAll');
});

test('trusted apply actor notification does not bootstrap twice; hard invalidation and external actors still do', () => {
    expect(needsSessionBootstrap(null, undefined, undefined)).toBe(true);
    expect(needsSessionBootstrap('trusted-anonymous', undefined, undefined)).toBe(false);
    expect(needsSessionBootstrap('trusted-a', 'a', 'a')).toBe(false);
    expect(needsSessionBootstrap(null, 'a', 'a')).toBe(true);
    expect(needsSessionBootstrap('trusted-a', 'a', 'b')).toBe(true);
    expect(needsSessionBootstrap('trusted-a', 'a', undefined)).toBe(true);
    expect(needsSessionBootstrap('trusted-anonymous', undefined, 'a')).toBe(true);
});

test('authority snapshots reject missing/duplicate digests and ignore unrelated audit metadata', () => {
    const actor = { id: 'a', role: 'user', status: 'active' };
    const row = { id: 'w', authorityFingerprint: 'a'.repeat(64) };
    const baseline = authoritySnapshot(actor, [row]);
    expect(authoritySnapshot({ ...actor, displayName: 'new label' } as typeof actor, [{ ...row, auditEpoch: 19 } as typeof row])).toBe(baseline);
    expect(authoritySnapshot(actor, [{ ...row, authorityFingerprint: 'b'.repeat(64) }])).not.toBe(baseline);
    expect(() => authoritySnapshot(actor, [row, row])).toThrow();
    expect(() => authoritySnapshot(actor, [{ ...row, authorityFingerprint: '' }])).toThrow();
});

test('late old actor projection cannot replace the current displayed authority after hard invalidation', async () => {
    clearDisplayedAuthority();
    const old = currentProjectionGeneration();
    let release!: () => void;
    const pending = new Promise<void>(resolve => { release = resolve; });
    const oldRequest = pending.then(() => acceptDisplayedAuthority('a', [], old));
    clearDisplayedAuthority();
    acceptDisplayedAuthority('b', [], currentProjectionGeneration());
    release(); await oldRequest;
    expect(displayedAuthority()?.actorId).toBe('b');
    clearDisplayedAuthority();
});

test('routine comparison disables new writes without aborting an already dispatched mutation', async () => {
    let release!: () => void, writes = 0;
    const pending = new Promise<void>(resolve => { release = resolve; });
    const client = axios.create({ baseURL: '/api', adapter: async config => { writes++; await pending; return { config, status: 200, statusText: 'OK', headers: {}, data: {} }; } });
    const uninstall = installAgricultureRequestSecurity(client, (async () => Response.json({ code: 0, data: { token: 'a'.repeat(64) } })) as typeof fetch);
    try {
        const oldWrite = client.post('/agriculture/workspaces/w/invites', {});
        while (!writes) await Promise.resolve();
        setRoutineChecking(true);
        await expect(client.post('/agriculture/workspaces/w/invites', {})).rejects.toThrow();
        expect(writes).toBe(1);
        release(); await oldWrite;
    } finally { setRoutineChecking(false); uninstall(); }
});

test('page confirmation scope clears repeated dialogs, preserves canceled and unrelated dialogs', () => {
    const own = new ConfirmationScope(), unrelated = new ConfirmationScope();
    let first = 0, second = 0, canceled = 0, outside = 0;
    own.add(() => { first++; }); own.add(() => { second++; });
    const cancel = own.add(() => { canceled++; }); cancel();
    unrelated.add(() => { outside++; });
    own.clear(); own.clear();
    expect([first, second, canceled, outside]).toEqual([1, 1, 0, 0]);
    own.add(() => { first++; }); own.clear();
    expect(first).toBe(2);
});

test('hints accept only secret-free invalidation, never identity or roles', () => {
    const hint = { version: 1, type: 'invalidate', event: 'a'.repeat(32) };
    expect(validSessionHint(hint)).toBe(true);
    for (const item of [{ ...hint, user: { id: 'owner' } }, { ...hint, role: 'admin' }, { ...hint, token: 'secret' }, { ...hint, version: 2 }, { ...hint, event: '../actor' }, null]) expect(validSessionHint(item)).toBe(false);
    class Channel extends EventTarget { close() {} postMessage() {} }
    const channel = new Channel();
    const target = Object.assign(new EventTarget(), { localStorage: {} as Storage });
    let invalidated = 0;
    const unbind = subscribeSessionHints(() => { invalidated++; }, target as unknown as Window, () => channel);
    channel.dispatchEvent(new MessageEvent('message', { data: { ...hint, role: 'admin' } }));
    expect(invalidated).toBe(0);
    channel.dispatchEvent(new MessageEvent('message', { data: hint }));
    channel.dispatchEvent(new MessageEvent('message', { data: hint }));
    expect(invalidated).toBe(1);
    const storage = new Event('storage');
    Object.assign(storage, { key: SESSION_HINT_KEY, newValue: JSON.stringify({ ...hint, event: 'b'.repeat(32) }), storageArea: target.localStorage });
    target.dispatchEvent(storage);
    expect(invalidated).toBe(2);
    unbind();
    channel.dispatchEvent(new MessageEvent('message', { data: { ...hint, event: 'c'.repeat(32) } }));
    target.dispatchEvent(storage);
    expect(invalidated).toBe(2);
});

test('same-document channel echo preserves local write result while foreign hints still invalidate', () => {
    class Channel extends EventTarget { close() {} postMessage() {} }
    const receiver = new Channel();
    const values: unknown[] = [];
    const target = Object.assign(new EventTarget(), { localStorage: {
        setItem(_key: string, value: string) { values.push(JSON.parse(value)); }, removeItem() {},
    } as unknown as Storage });
    let invalidated = 0;
    const unbind = subscribeSessionHints(() => { invalidated++; }, target as unknown as Window, () => receiver);
    const sender = () => ({ close() {}, addEventListener() {}, removeEventListener() {}, postMessage(value: unknown) {
        // BroadcastChannel excludes only the sending object, not its document.
        receiver.dispatchEvent(new MessageEvent('message', { data: value }));
    } });
    try {
        publishSessionHint(target, sender); publishSessionHint(target, sender);
        expect(invalidated).toBe(0);
        expect(values.length).toBe(2);
        expect(values.every(validSessionHint)).toBe(true);
        expect(values[0]).not.toEqual(values[1]);
        // A delayed local echo remains suppressed, without an expiring event cache.
        receiver.dispatchEvent(new MessageEvent('message', { data: values[0] }));
        expect(invalidated).toBe(0);
        const foreign = { version: 1, type: 'invalidate', event: 'd'.repeat(32) };
        receiver.dispatchEvent(new MessageEvent('message', { data: foreign }));
        expect(invalidated).toBe(1);
    } finally { unbind(); }
});

test('agriculture writes fetch fresh CSRF each time and retain no token in response config', async () => {
    const tokens: unknown[] = [], fetches: RequestInit[] = [];
    const client = axios.create({ baseURL: '/api', adapter: async config => {
        tokens.push(config.headers.get('X-CSRF-Token'));
        return { config, status: 200, statusText: 'OK', headers: {}, data: { code: 0, data: {} } };
    } });
    let n = 0;
    const uninstall = installAgricultureRequestSecurity(client, (async (path, init) => {
        expect(path).toBe('/api/auth/csrf'); fetches.push(init!);
        return Response.json({ code: 0, data: { token: (++n === 1 ? 'a' : 'b').repeat(64) } });
    }) as typeof fetch);
    try {
        const first = await client.post('/auth/login', {});
        await client.patch('/agriculture/workspaces/w/members/u', {});
        expect(tokens).toEqual(['a'.repeat(64), 'b'.repeat(64)]);
        expect(first.config.headers.get('X-CSRF-Token')).toBeUndefined();
        for (const init of fetches) { expect(init.cache).toBe('no-store'); expect(init.credentials).toBe('same-origin'); expect(init.redirect).toBe('error'); }
    } finally { uninstall(); }
});

test('CSRF failure never sends or retries the business write; film client untouched', async () => {
    for (const response of [new Response('', { status: 403 }), Response.json({ code: 0, data: { token: 'bad' } }), Response.json({ code: 403, data: { token: 'a'.repeat(64) } })]) {
        let writes = 0;
        const client = axios.create({ baseURL: '/api', adapter: async config => { writes++; return { config, status: 200, statusText: 'OK', headers: {}, data: {} }; } });
        const uninstall = installAgricultureRequestSecurity(client, (async () => response) as typeof fetch);
        await expect(client.post('/auth/logout')).rejects.toThrow();
        expect(writes).toBe(0);
        uninstall();
        await client.post('/auth/logout');
        expect(writes).toBe(1);
    }
});

test('failed request diagnostics do not retain CSRF header', async () => {
    const client = axios.create({ baseURL: '/api', adapter: async config => { throw new AxiosError('fixture failure', 'ERR_NETWORK', config); } });
    const uninstall = installAgricultureRequestSecurity(client, (async () => Response.json({ code: 0, data: { token: 'a'.repeat(64) } })) as typeof fetch);
    try { await client.post('/auth/logout'); throw new Error('expected rejection'); }
    catch (error) { expect((error as AxiosError).config?.headers.get('X-CSRF-Token')).toBeUndefined(); }
    finally { uninstall(); }
});

test('invalidating while session projection awaits cancellation prevents late identity revival', async () => {
    await applyUserSession({ user: null });
    const original = appQueryClient.cancelQueries;
    let release!: () => void;
    const pending = new Promise<void>(resolve => { release = resolve; });
    appQueryClient.cancelQueries = () => pending;
    try {
        const old = applyUserSession({ user: { id: 'old', username: 'old', displayName: 'Old', role: 'user', status: 'active', createdAt: '', updatedAt: '' } });
        beginSessionRevalidation();
        release(); await old;
        expect(useUserStore.getState().user).toBeNull();
        expect(useUserStore.getState().hydrated).toBe(false);
    } finally { appQueryClient.cancelQueries = original; await applyUserSession({ user: null }); }
});
