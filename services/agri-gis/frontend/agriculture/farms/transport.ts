export type Fetcher = (path: string, init: RequestInit) => Promise<Response>
export type Envelope = { data: unknown; meta: { request_id: string; schema_version: '1.0' }; next_cursor?: string; has_more?: boolean }
export class FarmError extends Error {
  constructor(public readonly status: number, public readonly code: string, message: string, public readonly requestID = '') { super(message) }
}
export const record = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value)
const safeID = (value: unknown): value is string => typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value)
const invalid = () => new FarmError(0, 'INVALID_RESPONSE', '服务器响应格式异常；请重新验证，不能视为操作成功。')
const messages: Record<number, string> = {
  400: '请求内容无效，请检查输入。', 401: '会话已失效，请重新登录。', 403: '权限已改变或当前账号无权操作。',
  404: '资源不存在或当前账号无权访问。', 409: '版本或幂等请求冲突。草稿已保留，请读取当前版本后比较，不会自动覆盖。',
  422: '边界、坐标系或其他字段不符合约束，请检查输入。', 428: '缺少预期版本，请重新读取对象。',
  503: '服务依赖暂不可用；请查询原操作状态或用原请求重试。',
}
async function json(response: Response): Promise<unknown> {
  if (!response.headers.get('content-type')?.includes('application/json')) throw invalid()
  const text = await response.text()
  if (text.length > 2 * 1024 * 1024) throw invalid()
  try { return JSON.parse(text) } catch { throw invalid() }
}
export class FarmTransport {
  constructor(private readonly fetcher: Fetcher = fetch) {}
  async request(path: string, signal: AbortSignal, method: 'GET' | 'POST' | 'PATCH' = 'GET', body?: unknown, key?: string): Promise<{ status: number; envelope: Envelope }> {
    if (!/^\/api\/agri\/v1\/(?:context|farm-previews|farms(?:\/[A-Za-z0-9_-]+(?:\/archive)?)?|farm-operations\/[A-Za-z0-9_-]+)(?:\?[^#]*)?$/.test(path)) throw invalid()
    // Invoke native Window.fetch without binding this transport as its receiver.
    const fetcher = this.fetcher
    const headers: Record<string, string> = { Accept: 'application/json' }
    const options: RequestInit = { credentials: 'same-origin', cache: 'no-store', redirect: 'error', signal }
    if (method !== 'GET') {
      const response = await fetcher('/api/auth/csrf', { ...options, method: 'GET', headers })
      if (!response.ok) throw new FarmError(response.status, 'CSRF_UNAVAILABLE', messages[response.status] ?? '无法确认请求安全凭据，请重新验证会话。')
      const csrf = await json(response)
      if (!record(csrf) || csrf.code !== 0 || !record(csrf.data) || typeof csrf.data.token !== 'string' || !/^[a-f0-9]{64}$/.test(csrf.data.token)) throw invalid()
      headers['X-CSRF-Token'] = csrf.data.token
      headers['Content-Type'] = 'application/json'
      if (key) { if (!safeID(key)) throw invalid(); headers['Idempotency-Key'] = key }
    }
    const response = await fetcher(path, { ...options, method, headers, ...(method === 'GET' ? {} : { body: JSON.stringify(body) }) })
    if ([401, 403, 404].includes(response.status)) throw new FarmError(response.status, 'AUTHORITY_UNAVAILABLE', messages[response.status]!)
    const payload = await json(response)
    if (!response.ok) {
      const requestID = record(payload) && safeID(payload.request_id) ? payload.request_id : ''
      // Never render arbitrary server diagnostics, reflected input, or contact data.
      const code = record(payload) && record(payload.error) && typeof payload.error.code === 'string' && /^[A-Z_]{1,64}$/.test(payload.error.code) ? payload.error.code : 'REQUEST_FAILED'
      throw new FarmError(response.status, code, messages[response.status] ?? '请求未得到成功确认；草稿保留，请检查原操作状态。', requestID)
    }
    if (!record(payload) || !('data' in payload) || !record(payload.meta) || !safeID(payload.meta.request_id) || payload.meta.schema_version !== '1.0') throw invalid()
    return { status: response.status, envelope: payload as Envelope }
  }
}
