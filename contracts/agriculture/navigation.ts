// Shared display navigation contract, never a source of identity or permission.
export class AgricultureNavigationError extends Error {
  constructor(public readonly code: 'ROUTE_NOT_FOUND' | 'UNTRUSTED_CONTEXT' | 'INVALID_WORKSPACE', message: string) { super(message) }
}
const identifier = (value: string) => /^[A-Za-z0-9_-]{1,128}$/.test(value)
export type AgricultureRoute = { path: string; search: string; href: string; workspaceId: string | null; page: 'home' | 'workspaces' | 'workspace' }

export function agricultureRoute(pathname: string, search = '', hash = ''): AgricultureRoute {
  if (hash || /[\\%\s\u0000-\u001f\u007f?#]/.test(pathname) || !pathname.startsWith('/agri'))
    throw new AgricultureNavigationError('ROUTE_NOT_FOUND', '农业页面地址不存在。')
  const path = pathname === '/agri' ? '/agri/' : pathname
  const match = /^\/agri\/workspaces\/([A-Za-z0-9_-]{1,128})$/.exec(path)
  const page = path === '/agri/' ? 'home' : path === '/agri/workspaces' ? 'workspaces' : match ? 'workspace' : null
  if (!page) throw new AgricultureNavigationError('ROUTE_NOT_FOUND', '农业页面地址不存在。')
  if (search && (!search.startsWith('?') || /[\u0000-\u0020\u007f#]/.test(search)))
    throw new AgricultureNavigationError('UNTRUSTED_CONTEXT', '链接包含不受支持的导航参数。')
  const params = new URLSearchParams(search)
  for (const key of params.keys()) if (key !== 'workspace_id')
    throw new AgricultureNavigationError('UNTRUSTED_CONTEXT', '链接包含不受支持的身份或导航参数。')
  const values = params.getAll('workspace_id')
  if (values.length > 1 || (values.length === 1 && !identifier(values[0])))
    throw new AgricultureNavigationError('INVALID_WORKSPACE', '工作区链接无效。')
  const pathID = match?.[1] ?? null
  const queryID = values[0] ?? null
  if (pathID && queryID && pathID !== queryID)
    throw new AgricultureNavigationError('INVALID_WORKSPACE', '路径和参数中的工作区不一致。')
  const canonicalSearch = queryID === null ? '' : `?workspace_id=${encodeURIComponent(queryID)}`
  return { path, search: canonicalSearch, href: path + canonicalSearch, workspaceId: pathID ?? queryID, page }
}

export function agricultureWorkspaceURL(workspaceId: string): string {
  if (!identifier(workspaceId)) throw new AgricultureNavigationError('INVALID_WORKSPACE', '工作区链接无效。')
  return agricultureRoute(`/agri/workspaces/${workspaceId}`).href
}
