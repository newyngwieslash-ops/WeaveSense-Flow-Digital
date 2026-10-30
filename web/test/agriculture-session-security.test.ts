import { expect, test } from 'bun:test';
import axios, { AxiosError } from 'axios';
import { installAgricultureRequestSecurity } from '../src/features/agriculture/request-security';
import { SESSION_HINT_KEY, subscribeSessionHints, validSessionHint } from '../../contracts/agriculture/session-hints';
import { applyUserSession, beginSessionRevalidation } from '../src/features/agriculture/session';
import { appQueryClient } from '../src/lib/query-client';
import { useUserStore } from '../src/stores/use-user-store';
import { ConfirmationScope } from '../src/features/agriculture/confirmation-scope';

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
