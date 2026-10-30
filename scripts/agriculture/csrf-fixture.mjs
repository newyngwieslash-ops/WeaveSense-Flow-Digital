// Test-only real HTTP transport. No production identity or request mocks.
export function sessionCookieFrom(response) {
    const values = response.headers.getSetCookie();
    const session = values.find(value => value.startsWith('open_ai_canvas_session='));
    return session?.split(';')[0].slice('open_ai_canvas_session='.length);
}

export async function csrfFetch(url, options = {}) {
    const target = new URL(url);
    if (target.protocol !== 'http:' || target.hostname !== '127.0.0.1') throw new Error('fixture transport requires literal local origin');
    const method = (options.method || 'GET').toUpperCase();
    if (['GET', 'HEAD', 'OPTIONS'].includes(method)) return fetch(url, options);
    const headers = new Headers(options.headers);
    const tokenHeaders = new Headers({ Origin: target.origin });
    if (headers.has('Cookie')) tokenHeaders.set('Cookie', headers.get('Cookie'));
    const response = await fetch(target.origin + '/api/auth/csrf', { headers: tokenHeaders, redirect: 'error', cache: 'no-store' });
    if (!response.ok) throw new Error('fixture CSRF acquisition failed');
    const body = await response.json();
    if (body.code !== 0 || !/^[a-f0-9]{64}$/.test(body.data?.token || '')) throw new Error('invalid fixture CSRF response');
    const guest = response.headers.getSetCookie().find(value => value.startsWith('agriculture-preauth-dev='));
    if (guest) headers.set('Cookie', [headers.get('Cookie'), guest.split(';')[0]].filter(Boolean).join('; '));
    // Explicit caller Origin is preserved so negative tests remain negative.
    if (!headers.has('Origin')) headers.set('Origin', target.origin);
    headers.set('X-CSRF-Token', body.data.token);
    return fetch(url, { ...options, headers, redirect: 'error' });
}
