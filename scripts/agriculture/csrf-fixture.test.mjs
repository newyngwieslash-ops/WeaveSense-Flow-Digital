import { test } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { csrfFetch, sessionCookieFrom } from './csrf-fixture.mjs';

test('fixture transport preserves guest cookie and never overwrites a negative Origin', async () => {
    const seen = [];
    const server = createServer((req, res) => {
        seen.push({ path: req.url, cookie: req.headers.cookie, origin: req.headers.origin, token: req.headers['x-csrf-token'] });
        if (req.url === '/api/auth/csrf') {
            res.setHeader('Set-Cookie', 'agriculture-preauth-dev=fixture; Path=/; HttpOnly');
            res.end(JSON.stringify({ code: 0, data: { token: 'a'.repeat(64) } }));
        } else { res.statusCode = 403; res.end(); }
    });
    await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
    try {
        const origin = `http://127.0.0.1:${server.address().port}`;
        const response = await csrfFetch(origin + '/api/auth/login', { method: 'POST', headers: { Origin: 'https://negative.invalid' } });
        assert.equal(response.status, 403);
        assert.equal(seen.length, 2);
        assert.equal(seen[0].origin, origin);
        assert.equal(seen[1].origin, 'https://negative.invalid');
        assert.equal(seen[1].cookie, 'agriculture-preauth-dev=fixture');
        assert.equal(seen[1].token, 'a'.repeat(64));
    } finally { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
});

test('fixture transport refuses external targets and selects session not clear-preauth cookie', async () => {
    await assert.rejects(csrfFetch('https://external.invalid/api/auth/login', { method: 'POST' }));
    const response = new Response(null, { headers: [
        ['Set-Cookie', 'agriculture-preauth-dev=; Path=/; Max-Age=0'],
        ['Set-Cookie', 'open_ai_canvas_session=session.value; Path=/; HttpOnly'],
    ] });
    assert.equal(sessionCookieFrom(response), 'session.value');
});
