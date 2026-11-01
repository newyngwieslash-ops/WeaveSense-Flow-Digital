import { FarmTransport } from '../farms/transport'
import { invalid, kinds } from './metadata'
import { assetFrom, contextFrom, operationFrom, pageFrom, positive, previewFrom, type Intent, type Preview } from './types'
export class AssetAPI {
  constructor(private readonly transport: Pick<FarmTransport, 'request'> = new FarmTransport()) {}
  private query(workspace: string) { return new URLSearchParams({ workspace_id: workspace }) }
  async context(workspace: string, signal: AbortSignal) { return contextFrom((await this.transport.request(`/api/agri/v1/asset-context?${this.query(workspace)}`, signal)).envelope.data, workspace) }
  async list(workspace: string, signal: AbortSignal, kind = '', dataClass = '', cursor = '') {
    if ((kind && !kinds.includes(kind as typeof kinds[number])) || (dataClass && !['demo', 'operational'].includes(dataClass))) return invalid()
    const query = this.query(workspace); query.set('limit', '50'); if (kind) query.set('kind', kind); if (dataClass) query.set('data_class', dataClass); if (cursor) query.set('cursor', cursor)
    const { envelope } = await this.transport.request(`/api/agri/v1/assets?${query}`, signal)
    return pageFrom(envelope.data, envelope.next_cursor, envelope.has_more, workspace)
  }
  async detail(workspace: string, id: string, version: number, signal: AbortSignal) {
    if (!positive(version)) return invalid()
    const value = assetFrom((await this.transport.request(`/api/agri/v1/assets/${encodeURIComponent(id)}/versions/${version}?${this.query(workspace)}`, signal)).envelope.data, workspace)
    if (value.asset_id !== id || value.version !== version) return invalid()
    return value
  }
  async current(workspace: string, id: string, knownVersion: number, signal: AbortSignal) {
    const known = await this.detail(workspace, id, knownVersion, signal)
    signal.throwIfAborted()
    return known.version === known.revision ? known : this.detail(workspace, id, known.revision, signal)
  }
  async preview(workspace: string, intent: Intent, signal: AbortSignal) { return previewFrom((await this.transport.request('/api/agri/v1/asset-previews', signal, 'POST', { workspace_id: workspace, ...intent })).envelope.data, intent) }
  async submit(workspace: string, intent: Intent, preview: Preview, key: string, signal: AbortSignal) {
    const { action, ...fields } = intent
    const body: Record<string, unknown> = { workspace_id: workspace, preview_id: preview.preview_id, ...fields }; delete body.asset_id
    const path = intent.action === 'create' ? '/api/agri/v1/assets' : `/api/agri/v1/assets/${encodeURIComponent(intent.asset_id)}/versions`
    const result = await this.transport.request(path, signal, 'POST', body, key), value = operationFrom(result.envelope.data, workspace)
    if (value.operation_id !== preview.preview_id || value.action !== intent.action || (intent.action === 'version' && value.asset_id !== intent.asset_id) || (result.status === 202 ? !['pending', 'reconciliation_required'].includes(value.state) : (result.status !== (action === 'create' ? 201 : 200) || value.state !== 'completed'))) return invalid()
    return value
  }
  async operation(workspace: string, id: string, signal: AbortSignal) {
    const value = operationFrom((await this.transport.request(`/api/agri/v1/asset-operations/${encodeURIComponent(id)}?${this.query(workspace)}`, signal)).envelope.data, workspace)
    if (value.operation_id !== id) return invalid()
    return value
  }
}
