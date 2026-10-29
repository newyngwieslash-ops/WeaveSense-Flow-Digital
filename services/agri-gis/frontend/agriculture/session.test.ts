import { describe, expect, test } from 'bun:test'
import { SessionController, loginURL, selectedWorkspace, workspaceURL } from './session'
import { bindSessionLifecycle } from './session-lifecycle'

const user = { id: 'user-a', username: 'farmer', displayName: 'Test Farmer', role: 'user', status: 'active' }
const workspace = { id: 'workspace-a', tenantId: 'server-only-tenant', slug: 'farm', name: 'Farm workspace', tenantName: 'Farm organization', tenantStatus: 'active', roles: ['viewer'], canManageMembers: false, canManageOrganization: false }
const response = (data: unknown, status = 200) => Response.json({ code: 0, data }, { status })
function client(responses: (Response | Error)[]) {
  const requests: { path: string; init: RequestInit }[] = []
  const controller = new SessionController(async (path, init) => {
    requests.push({ path, init })
    const result = responses.shift()
    if (result instanceof Error) throw result
    if (!result) throw new Error('Unexpected request')
    return result
  })
  return { controller, requests }
}

describe('platform cookie session boundary', () => {
  test('reads real endpoint contracts, never passes tenant or user claims', async () => {
    const { controller, requests } = client([response({ user }), response([workspace])])
    await controller.refresh('?workspace_id=workspace-a')
    expect(controller.state.status).toBe('ready')
    expect(controller.state.selected).toEqual({ id: 'workspace-a', slug: 'farm', name: 'Farm workspace' })
    expect(requests.map((item) => item.path)).toEqual(['/api/auth/session', '/api/agriculture/workspaces'])
    for (const item of requests) {
      expect(item.init.credentials).toBe('same-origin')
      expect(item.init.cache).toBe('no-store')
      expect(item.init.redirect).toBe('error')
      expect(item.init.body).toBeUndefined()
      expect(item.init.headers).toEqual({ Accept: 'application/json' })
    }
    expect(JSON.stringify(controller.state)).not.toContain('server-only-tenant')
  })
  for (const [label, reply, expected] of [
    ['expired HTTP', response(null, 401), 'AUTH_EXPIRED'],
    ['expired envelope', Response.json({ code: 401, data: null }), 'AUTH_EXPIRED'],
    ['anonymous', response({ user: null }), 'AUTH_REQUIRED'],
    ['disabled', response({ user: { ...user, status: 'disabled' } }), 'ACCOUNT_DISABLED'],
    ['forbidden HTTP', response(null, 403), 'ACCESS_DENIED'],
    ['malformed identity', response({ user: { ...user, id: '', role: 'owner' } }), 'INVALID_RESPONSE'],
    ['malformed JSON', new Response('{broken'), 'INVALID_RESPONSE'],
    ['server error', response(null, 500), 'HTTP_ERROR'],
    ['offline', new Error('offline'), 'NETWORK_ERROR'],
  ] as const) {
    test(`fails closed on ${label}`, async () => {
      const { controller, requests } = client([reply])
      await controller.refresh()
      expect(controller.state.error?.code).toBe(expected)
      expect(controller.state.user).toBeNull()
      expect(controller.state.workspaces).toEqual([])
      expect(requests.length).toBe(1)
    })
  }
  for (const query of ['?tenant_id=other', '?user_id=admin', '?operator_code=admin', '?role=admin', '?next=https://evil.example', '?workspace_id=a&workspace_id=b', '?workspace_id=//evil.example']) {
    test(`rejects browser-supplied authority or unsafe navigation: ${query}`, async () => {
      const { controller, requests } = client([])
      await controller.refresh(query)
      expect(controller.state.status).toBe('error')
      expect(requests).toHaveLength(0)
      expect(() => loginURL(query)).toThrow()
    })
  }
  test('workspace ID from URL must occur in the authenticated server list', async () => {
    const { controller } = client([response({ user }), response([workspace])])
    await controller.refresh('?workspace_id=other-tenant-workspace')
    expect(controller.state.error?.code).toBe('WORKSPACE_FORBIDDEN')
    expect(controller.state.workspaces).toEqual([])
  })
  test('disabled owner governance records never become Vue business workspaces', async () => {
    const disabled = { ...workspace, tenantStatus: 'disabled', canManageOrganization: true }
    const { controller } = client([response({ user }), response([disabled]), response({ user }), response([disabled])])
    await controller.refresh(); expect(controller.state.status).toBe('ready'); expect(controller.state.workspaces).toEqual([])
    await controller.refresh('?workspace_id=workspace-a'); expect(controller.state.error?.code).toBe('WORKSPACE_FORBIDDEN')
  })
  test('legacy records, incomplete profile fields and noncanonical roles fail closed', async () => {
    for (const item of [{ id: 'old', name: 'Film', slug: 'default' }, { ...workspace, tenantName: null }, { ...workspace, roles: ['owner'] }, { ...workspace, roles: ['viewer', 'viewer'] }, { ...workspace, canManageMembers: 'true' }]) {
      const { controller } = client([response({ user }), response([item])]); await controller.refresh()
      expect(controller.state.error?.code).toBe('INVALID_RESPONSE'); expect(controller.state.user).toBeNull()
    }
  })
  test('workspace API 401 clears identity and previously loaded data', async () => {
    const { controller } = client([response({ user }), response([workspace]), response({ user }), response(null, 401)])
    await controller.refresh()
    expect(controller.state.user?.id).toBe('user-a')
    await controller.refresh()
    expect(controller.state.status).toBe('anonymous')
    expect(controller.state.user).toBeNull()
    expect(controller.state.workspaces).toEqual([])
  })
  test('empty list is honest and invalid/duplicate workspace lists fail closed', async () => {
    for (const list of [[], [workspace, workspace], [{ ...workspace, id: '../other' }]]) {
      const { controller } = client([response({ user }), response(list)])
      await controller.refresh()
      expect(controller.state.status).toBe(list.length === 0 ? 'ready' : 'error')
      expect(controller.state.workspaces).toEqual([])
    }
  })
  test('constructs only fixed same-origin return routes', () => {
    expect(selectedWorkspace('')).toBeNull()
    expect(loginURL('')).toBe('/login?next=%2Fagri%2F')
    expect(loginURL('?workspace_id=a')).toBe('/login?next=%2Fagri%2F%3Fworkspace_id%3Da')
    expect(workspaceURL('abc-123')).toBe('/agri/?workspace_id=abc-123')
    expect(() => workspaceURL('https://evil.example')).toThrow()
  })
  test('does not allow a stale session response to replace a newer account', async () => {
    let resolveOld!: (response: Response) => void
    const old = new Promise<Response>((resolve) => { resolveOld = resolve })
    let calls = 0
    const controller = new SessionController(async () => {
      if (++calls === 1) return old
      if (calls === 2) return response({ user: { ...user, id: 'user-b' } })
      return response([{ ...workspace, id: 'workspace-b' }])
    })
    const first = controller.refresh()
    await controller.refresh()
    resolveOld(response({ user }))
    await first
    expect(controller.state.user?.id).toBe('user-b')
    expect(controller.state.workspaces[0].id).toBe('workspace-b')
    expect(calls).toBe(3)
  })
})

describe('confirmed unified logout', () => {
  test('posts without client identity then confirms cookie session is gone', async () => {
    const { controller, requests } = client([response({ user }), response([workspace]), response({ ok: true }), response({ user: null })])
    await controller.refresh()
    await controller.logout()
    expect(controller.state.status).toBe('anonymous')
    expect(controller.state.error).toBeNull()
    expect(controller.state.user).toBeNull()
    expect(requests[2].path).toBe('/api/auth/logout')
    expect(requests[2].init.method).toBe('POST')
    expect(requests[2].init.body).toBe('{}')
    expect(requests[3].path).toBe('/api/auth/session')
  })
  for (const [label, replies] of [
    ['failed HTTP', [response(null, 500)]],
    ['false confirmation', [response({ ok: false })]],
    ['session remains', [response({ ok: true }), response({ user })]],
    ['confirmation offline', [response({ ok: true }), new Error('offline')]],
  ] as const) {
    test(`never reports success after ${label}`, async () => {
      const { controller } = client([...replies])
      await controller.logout()
      expect(controller.state.status).toBe('error')
      expect(controller.state.error?.code).toBe('LOGOUT_FAILED')
      expect(controller.state.workspaces).toEqual([])
    })
  }
  test('expired verification is a confirmed absent session', async () => {
    const { controller } = client([response({ ok: true }), response(null, 401)])
    await controller.logout()
    expect(controller.state.status).toBe('anonymous')
    expect(controller.state.error).toBeNull()
  })
  test('focus refresh cannot supersede an in-flight logout', async () => {
    let resolveLogout!: (value: Response) => void
    const pending = new Promise<Response>((resolve) => { resolveLogout = resolve })
    let calls = 0
    const controller = new SessionController(async () => ++calls === 1 ? pending : response({ user: null }))
    const logout = controller.logout()
    await controller.refresh()
    expect(controller.state.status).toBe('logging_out')
    resolveLogout(response({ ok: true }))
    await logout
    expect(controller.state.status).toBe('anonymous')
    expect(calls).toBe(2)
  })
})

test('BFCache pageshow clears restored actor, requests current server session, and unbinds on unmount', async () => {
  const { controller, requests } = client([response({ user }), response([workspace]), response({ user: null })])
  const windowEvents = new EventTarget()
  const documentEvents = new EventTarget()
  const stateReached = (status: string) => new Promise<void>((resolve) => {
    controller.onChange = (state) => { if (state.status === status) resolve() }
  })
  const initial = stateReached('ready')
  const unbind = bindSessionLifecycle(controller, windowEvents, documentEvents, () => 'visible', () => '')
  await initial
  expect(controller.state.user?.id).toBe('user-a')
  const restored = stateReached('anonymous')
  // Bun has no PageTransitionEvent; persisted=true models the browser event detail.
  const pageshow = new Event('pageshow')
  Object.defineProperty(pageshow, 'persisted', { value: true })
  windowEvents.dispatchEvent(pageshow)
  expect(controller.state.status).toBe('loading')
  expect(controller.state.user).toBeNull()
  expect(controller.state.workspaces).toEqual([])
  await restored
  expect(requests.map((item) => item.path)).toEqual(['/api/auth/session', '/api/agriculture/workspaces', '/api/auth/session'])
  expect(controller.state.error?.code).toBe('AUTH_REQUIRED')
  unbind()
  windowEvents.dispatchEvent(new Event('pageshow'))
  windowEvents.dispatchEvent(new Event('focus'))
  documentEvents.dispatchEvent(new Event('visibilitychange'))
  expect(requests).toHaveLength(3)
})
