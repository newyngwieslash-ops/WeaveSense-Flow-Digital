// Display-only projection of the platform cookie session. Never an authority for GIS.
import { AgricultureNavigationError, agricultureRoute, agricultureWorkspaceURL } from '../../../../contracts/agriculture/navigation'
export type User = { id: string; username: string; displayName: string; role: 'admin' | 'user'; status: 'active' }
export type Workspace = { id: string; name: string; slug: string }
export type State = {
  status: 'loading' | 'ready' | 'anonymous' | 'disabled' | 'error' | 'logging_out'
  user: User | null
  workspaces: Workspace[]
  selected: Workspace | null
  error: SessionError | null
}
export class SessionError extends Error {
  constructor(public readonly code: string, message: string) { super(message) }
}
type Fetcher = (input: string, init: RequestInit) => Promise<Response>
const record = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null && !Array.isArray(value)
const id = (value: unknown): value is string => typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value)
const text = (value: unknown): value is string => typeof value === 'string' && value.length > 0 && value.length <= 256
const invalid = () => new SessionError('INVALID_RESPONSE', '平台返回格式异常，已停止显示身份和工作区。')
const empty = (status: State['status'], error: SessionError | null = null): State => ({ status, user: null, workspaces: [], selected: null, error })

export function selectedWorkspace(search: string): string | null {
  return agricultureRoute('/agri/', search).workspaceId
}
export function workspaceURL(workspaceId: string): string {
  return agricultureWorkspaceURL(workspaceId)
}
export function loginURL(search: string, pathname = '/agri/', hash = ''): string {
  const next = agricultureRoute(pathname, search, hash).href
  return `/login?next=${encodeURIComponent(next)}`
}

async function request(fetcher: Fetcher, path: string, signal: AbortSignal, method = 'GET'): Promise<unknown> {
  const response = await fetcher(path, {
    method, credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal,
    headers: method === 'POST' ? { Accept: 'application/json', 'Content-Type': 'application/json' } : { Accept: 'application/json' },
    ...(method === 'POST' ? { body: '{}' } : {}),
  })
  if (response.status === 401) throw new SessionError('AUTH_EXPIRED', '登录已失效，请重新登录。')
  if (response.status === 403) throw new SessionError('ACCESS_DENIED', '账号已停用或没有访问权限。')
  if (!response.ok) throw new SessionError('HTTP_ERROR', '平台请求失败，请稍后重试。')
  let envelope: unknown
  try { envelope = await response.json() } catch { throw invalid() }
  if (!record(envelope) || typeof envelope.code !== 'number' || !('data' in envelope)) throw invalid()
  if (envelope.code === 401) throw new SessionError('AUTH_EXPIRED', '登录已失效，请重新登录。')
  if (envelope.code === 403) throw new SessionError('ACCESS_DENIED', '账号已停用或没有访问权限。')
  if (envelope.code !== 0) throw new SessionError('SERVER_ERROR', '平台未完成请求，请重试。')
  return envelope.data
}
function userFrom(payload: unknown): User | null {
  if (!record(payload) || !('user' in payload)) throw invalid()
  if (payload.user === null) return null
  const user = payload.user
  if (!record(user) || !id(user.id) || !text(user.username) || !text(user.displayName) || !['admin', 'user'].includes(String(user.role))) throw invalid()
  if (user.status === 'disabled') throw new SessionError('ACCOUNT_DISABLED', '账号已停用，请联系管理员。')
  if (user.status !== 'active') throw invalid()
  return { id: user.id, username: user.username, displayName: user.displayName, role: user.role as User['role'], status: 'active' }
}
function workspacesFrom(payload: unknown): Workspace[] {
  if (!Array.isArray(payload)) throw invalid()
  const seen = new Set<string>()
  return payload.map((item: unknown) => {
    const roles = ['viewer', 'analyst', 'editor', 'field_operator', 'reviewer', 'farm_manager', 'tenant_admin', 'billing_admin']
    if (!record(item) || !id(item.id) || !id(item.tenantId) || !text(item.name) || !text(item.slug) || !text(item.tenantName)
      || !['active', 'disabled'].includes(String(item.tenantStatus)) || typeof item.canManageMembers !== 'boolean' || typeof item.canManageOrganization !== 'boolean'
      || !Array.isArray(item.roles) || item.roles.some(role => typeof role !== 'string' || !roles.includes(role)) || new Set(item.roles).size !== item.roles.length || seen.has(item.id)) throw invalid()
    seen.add(item.id)
    // tenantId is intentionally not retained or forwarded. GIS authority comes later from Go.
    return item.tenantStatus === 'active' ? { id: item.id, name: item.name, slug: item.slug } : null
  }).filter((item): item is Workspace => item !== null)
}

export class SessionController {
  state: State = empty('loading')
  onChange: (state: State) => void = () => {}
  private generation = 0
  private active: AbortController | null = null
  private deadline: ReturnType<typeof setTimeout> | undefined
  constructor(private readonly fetcher: Fetcher = fetch) {}
  private publish(state: State) { this.state = state; this.onChange(state) }
  private begin(status: State['status']) {
    this.active?.abort()
    clearTimeout(this.deadline)
    const controller = new AbortController()
    this.active = controller
    this.deadline = setTimeout(() => controller.abort(), 20000)
    this.publish(empty(status))
    return { generation: ++this.generation, signal: controller.signal }
  }
  private failure(error: unknown, logout = false) {
    const failure = logout ? new SessionError('LOGOUT_FAILED', '退出未确认，不能认为服务器会话已注销。请重试或重新检查登录状态。')
      : error instanceof AgricultureNavigationError ? new SessionError(error.code, error.message)
      : error instanceof SessionError ? error : new SessionError('NETWORK_ERROR', '平台暂时无法连接或请求超时，身份和工作区已清空。')
    const status = ['AUTH_REQUIRED', 'AUTH_EXPIRED'].includes(failure.code) ? 'anonymous' : failure.code === 'ACCOUNT_DISABLED' ? 'disabled' : 'error'
    this.publish(empty(status, failure))
  }
  async refresh(search = '', pathname = '/agri/', hash = '') {
    if (this.state.status === 'logging_out') return
    const { generation, signal } = this.begin('loading')
    try {
      const requested = agricultureRoute(pathname, search, hash).workspaceId
      const user = userFrom(await request(this.fetcher, '/api/auth/session', signal))
      if (generation !== this.generation) return
      if (!user) throw new SessionError('AUTH_REQUIRED', '尚未登录或会话已失效，请使用平台统一登录。')
      const workspaces = workspacesFrom(await request(this.fetcher, '/api/agriculture/workspaces', signal))
      const selected = requested === null ? null : workspaces.find((workspace) => workspace.id === requested)
      if (selected === undefined) throw new SessionError('WORKSPACE_FORBIDDEN', '该工作区不存在或当前账号无权访问。')
      if (generation === this.generation) this.publish({ status: 'ready', user, workspaces, selected, error: null })
    } catch (error) {
      if (generation === this.generation) this.failure(error)
    } finally {
      if (generation === this.generation) clearTimeout(this.deadline)
    }
  }
  async logout() {
    if (this.state.status === 'logging_out') return
    const { generation, signal } = this.begin('logging_out')
    try {
      const result = await request(this.fetcher, '/api/auth/logout', signal, 'POST')
      if (!record(result) || result.ok !== true) throw invalid()
      try {
        const user = userFrom(await request(this.fetcher, '/api/auth/session', signal))
        if (user !== null) throw new SessionError('SESSION_REMAINS', '会话仍有效。')
      } catch (error) {
        if (!(error instanceof SessionError && error.code === 'AUTH_EXPIRED')) throw error
      }
      if (generation === this.generation) this.publish(empty('anonymous'))
    } catch (error) {
      if (generation === this.generation) this.failure(error, true)
    } finally {
      if (generation === this.generation) clearTimeout(this.deadline)
    }
  }
  dispose() { ++this.generation; this.active?.abort(); clearTimeout(this.deadline); this.publish(empty('loading')) }
}
