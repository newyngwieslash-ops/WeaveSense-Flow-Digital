import { FarmTransport } from './transport'
import { contextFrom, farmFrom, invalid, operationFrom, pageFrom, previewFrom, type Intent, type Preview } from './types'
export class FarmAPI {
  constructor(private readonly transport = new FarmTransport()) {}
  private query(workspace: string) { return new URLSearchParams({ workspace_id: workspace }) }
  async context(workspace: string, signal: AbortSignal) { return contextFrom((await this.transport.request(`/api/agri/v1/context?${this.query(workspace)}`, signal)).envelope.data, workspace) }
  async list(workspace: string, signal: AbortSignal, status = 'active', search = '', cursor = '') {
    const query = this.query(workspace); query.set('status', status); query.set('limit', '50'); if (search) query.set('search', search); if (cursor) query.set('cursor', cursor)
    const { envelope } = await this.transport.request(`/api/agri/v1/farms?${query}`, signal)
    return pageFrom({ items: envelope.data, next_cursor: envelope.next_cursor, has_more: envelope.has_more }, workspace)
  }
  async detail(workspace: string, id: string, signal: AbortSignal) { return farmFrom((await this.transport.request(`/api/agri/v1/farms/${encodeURIComponent(id)}?${this.query(workspace)}`, signal)).envelope.data, workspace) }
  async preview(workspace: string, intent: Intent, signal: AbortSignal) { return previewFrom((await this.transport.request('/api/agri/v1/farm-previews', signal, 'POST', { workspace_id: workspace, ...intent })).envelope.data, intent) }
  async submit(workspace: string, intent: Intent, preview: Preview, key: string, signal: AbortSignal) {
    const { action, ...fields } = intent
    const body: Record<string, unknown> = { workspace_id: workspace, preview_id: preview.preview_id, ...fields }; delete body.farm_id
    const path = intent.action === 'create' ? '/api/agri/v1/farms' : `/api/agri/v1/farms/${encodeURIComponent(intent.farm_id)}${action === 'archive' ? '/archive' : ''}`
    const result = await this.transport.request(path, signal, action === 'update' ? 'PATCH' : 'POST', body, key)
    const operation = operationFrom(result.envelope.data, workspace)
    if (operation.operation_id !== preview.preview_id || operation.action !== action || ![200, 201, 202].includes(result.status) || (result.status === 202 ? operation.state === 'completed' : operation.state !== 'completed')) return invalid()
    return operation
  }
  async operation(workspace: string, id: string, signal: AbortSignal) {
    const operation = operationFrom((await this.transport.request(`/api/agri/v1/farm-operations/${encodeURIComponent(id)}?${this.query(workspace)}`, signal)).envelope.data, workspace)
    if (operation.operation_id !== id) return invalid()
    return operation
  }
}
