import { describe, expect, test } from 'bun:test'
import { FarmError, FarmTransport, type Fetcher } from './transport'
const response = (payload: unknown, status = 200) => new Response(JSON.stringify(payload), { status, headers: { 'Content-Type': 'application/json' } })
const success = { data: [], meta: { request_id: 'req_1', schema_version: '1.0' } }
describe('same-origin farm transport component', () => {
  test('GET uses real-cookie contract, no-store and abort signal without identity headers', async () => {
    const abort = new AbortController()
    const api = new FarmTransport(async (path, init) => {
      expect(path).toBe('/api/agri/v1/farms?workspace_id=w1')
      expect(init.credentials).toBe('same-origin'); expect(init.cache).toBe('no-store'); expect(init.redirect).toBe('error'); expect(init.signal).toBe(abort.signal)
      expect(init.headers).toEqual({ Accept: 'application/json' }); expect(init.body).toBeUndefined()
      return response(success)
    })
    expect((await api.request('/api/agri/v1/farms?workspace_id=w1', abort.signal)).envelope.data).toEqual([])
  })
  test('writes acquire existing platform CSRF and preserve explicit idempotency key', async () => {
    const requests: string[] = []
    const api = new FarmTransport(async (path, init) => {
      requests.push(path)
      if (path === '/api/auth/csrf') return response({ code: 0, data: { token: 'a'.repeat(64) } })
      expect(init.method).toBe('POST'); expect(new Headers(init.headers).get('X-CSRF-Token')).toBe('a'.repeat(64)); expect(new Headers(init.headers).get('Idempotency-Key')).toBe('req_keep'); expect(init.body).toBe('{"workspace_id":"w1"}')
      return response(success, 202)
    })
    expect((await api.request('/api/agri/v1/farms', new AbortController().signal, 'POST', { workspace_id: 'w1' }, 'req_keep')).status).toBe(202)
    expect(requests).toEqual(['/api/auth/csrf', '/api/agri/v1/farms'])
  })
  test('GET invokes an ordinary fetch function with a neutral receiver', async () => {
    const abort = new AbortController()
    let calls = 0
    const api = new FarmTransport(async function (this: unknown, path, init) {
      expect(this).toBeUndefined()
      calls++
      expect(path).toBe('/api/agri/v1/context?workspace_id=w1')
      expect(init.method).toBe('GET'); expect(init.credentials).toBe('same-origin'); expect(init.cache).toBe('no-store'); expect(init.redirect).toBe('error'); expect(init.signal).toBe(abort.signal)
      expect(init.headers).toEqual({ Accept: 'application/json' }); expect(init.body).toBeUndefined()
      return response(success)
    })
    expect((await api.request('/api/agri/v1/context?workspace_id=w1', abort.signal)).envelope.data).toEqual([])
    expect(calls).toBe(1)
  })
  test('POST invokes both CSRF and mutation fetch functions with neutral receivers', async () => {
    const abort = new AbortController()
    const requests: string[] = []
    const api = new FarmTransport(async function (this: unknown, path, init) {
      expect(this).toBeUndefined()
      requests.push(path)
      expect(init.credentials).toBe('same-origin'); expect(init.cache).toBe('no-store'); expect(init.redirect).toBe('error'); expect(init.signal).toBe(abort.signal)
      if (path === '/api/auth/csrf') {
        expect(init.method).toBe('GET'); expect(init.headers).toEqual({ Accept: 'application/json' }); expect(init.body).toBeUndefined()
        return response({ code: 0, data: { token: 'a'.repeat(64) } })
      }
      expect(path).toBe('/api/agri/v1/farms'); expect(init.method).toBe('POST')
      expect(init.headers).toEqual({ Accept: 'application/json', 'X-CSRF-Token': 'a'.repeat(64), 'Content-Type': 'application/json', 'Idempotency-Key': 'req_keep' })
      expect(init.body).toBe('{"workspace_id":"w1"}')
      return response(success, 202)
    })
    expect((await api.request('/api/agri/v1/farms', abort.signal, 'POST', { workspace_id: 'w1' }, 'req_keep')).status).toBe(202)
    expect(requests).toEqual(['/api/auth/csrf', '/api/agri/v1/farms'])
  })
  test('does not echo reflected sensitive error or accept old envelope, HTML, redirects or foreign origin', async () => {
    const bad = new FarmTransport(async () => response({ request_id: 'req_2', error: { code: 'REVISION_CONFLICT', message: 'contact@example.com secret raw diagnostics' } }, 409))
    try { await bad.request('/api/agri/v1/farms', new AbortController().signal); throw new Error('expected failure') } catch (e) { expect(e).toBeInstanceOf(FarmError); expect((e as FarmError).status).toBe(409); expect((e as Error).message).not.toContain('contact@') }
    for (const fetcher of [async () => response({ code: 0, data: [] }), async () => new Response('<html>login</html>'), async () => response({ ...success, meta: { ...success.meta, schema_version: '2.0' } })] as Fetcher[]) {
      await expect(new FarmTransport(fetcher).request('/api/agri/v1/context', new AbortController().signal)).rejects.toBeInstanceOf(FarmError)
    }
    let called = false
    await expect(new FarmTransport(async () => { called = true; return response(success) }).request('https://other.test/api/agri/v1/farms', new AbortController().signal)).rejects.toBeInstanceOf(FarmError)
    expect(called).toBe(false)
  })
  test('non-JSON denial preserves authority failure status so the controller clears protected state', async () => {
    for (const status of [401, 403, 404]) {
      const api = new FarmTransport(async () => new Response('<html>denied</html>', { status }))
      try { await api.request('/api/agri/v1/context', new AbortController().signal); throw new Error('expected denial') } catch (error) { expect((error as FarmError).status).toBe(status) }
    }
  })
})
