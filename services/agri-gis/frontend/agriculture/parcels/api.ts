import { FarmTransport } from '../farms/transport'
import { contextFrom, contextPageFrom, pageFrom, date, historyFrom, invalid, operationFrom, parcelFrom, positive, previewFrom, timeValue, type Intent, type Preview, type Selector } from './types'
export class ParcelAPI {
  constructor(private readonly transport: Pick<FarmTransport, 'request'> = new FarmTransport()) {}
  private query(workspace: string) { return new URLSearchParams({ workspace_id: workspace }) }
  async farms(workspace: string, signal: AbortSignal, cursor = '') {
    const query = this.query(workspace); query.set('limit', '50'); if (cursor) query.set('cursor', cursor)
    const { envelope } = await this.transport.request(`/api/agri/v1/parcel-farms?${query}`, signal)
    return contextPageFrom(envelope.data, envelope.next_cursor, envelope.has_more, workspace)
  }
  async context(workspace: string, parent: string, signal: AbortSignal) {
    return contextFrom((await this.transport.request(`/api/agri/v1/farms/${encodeURIComponent(parent)}/parcel-context?${this.query(workspace)}`, signal)).envelope.data, workspace, parent)
  }
  async list(workspace: string, parent: string, signal: AbortSignal, cursor = '') {
    const query = this.query(workspace); query.set('limit', '50'); if (cursor) query.set('cursor', cursor)
    const { envelope } = await this.transport.request(`/api/agri/v1/farms/${encodeURIComponent(parent)}/parcels?${query}`, signal)
    return pageFrom(envelope.data, envelope.next_cursor, envelope.has_more, workspace, parent)
  }
  async detail(workspace: string, parent: string, id: string, signal: AbortSignal, selector?: Selector) {
    const query = this.query(workspace)
    if (selector) {
      if (Object.keys(selector).length !== 1) return invalid()
      if ('revision' in selector && positive(selector.revision)) query.set('revision', String(selector.revision))
      else if ('at' in selector && date(selector.at)) query.set('at', selector.at)
      else return invalid()
    }
    const value = parcelFrom((await this.transport.request(`/api/agri/v1/parcels/${encodeURIComponent(id)}?${query}`, signal)).envelope.data, workspace, parent)
    if (value.id !== id || (!selector && value.geometry_revision !== value.current_geometry_revision) || (selector && 'revision' in selector && value.geometry_revision !== selector.revision) || (selector && 'at' in selector && (timeValue(selector.at) < timeValue(value.valid_from) || (value.valid_to !== null && timeValue(selector.at) >= timeValue(value.valid_to))))) return invalid()
    return value
  }
  async history(workspace: string, parent: string, id: string, signal: AbortSignal, cursor = '') {
    const query = this.query(workspace); query.set('limit', '50'); if (cursor) query.set('cursor', cursor)
    const { envelope } = await this.transport.request(`/api/agri/v1/parcels/${encodeURIComponent(id)}/history?${query}`, signal)
    return historyFrom(envelope.data, envelope.next_cursor, envelope.has_more, workspace, parent, id)
  }
  async preview(workspace: string, parent: string, intent: Intent, signal: AbortSignal) {
    return previewFrom((await this.transport.request('/api/agri/v1/parcel-previews', signal, 'POST', { workspace_id: workspace, ...intent })).envelope.data, intent, parent)
  }
  async submit(workspace: string, parent: string, intent: Intent, preview: Preview, key: string, signal: AbortSignal) {
    const { action, ...fields } = intent
    const body: Record<string, unknown> = { workspace_id: workspace, preview_id: preview.preview_id, ...fields }; delete body.farm_id; delete body.parcel_id
    const path = intent.action === 'create' ? `/api/agri/v1/farms/${encodeURIComponent(intent.farm_id)}/parcels` : `/api/agri/v1/parcels/${encodeURIComponent(intent.parcel_id)}/revisions`
    const result = await this.transport.request(path, signal, 'POST', body, key)
    const operation = operationFrom(result.envelope.data, workspace, parent)
    if (operation.operation_id !== preview.preview_id || operation.action !== action || (intent.action === 'revise' && operation.parcel_id !== intent.parcel_id) || ![200, 201, 202].includes(result.status) || (result.status === 202 ? !['pending', 'reconciliation_required'].includes(operation.state) : operation.state !== 'completed') || (result.status !== 202 && result.status !== (action === 'create' ? 201 : 200))) return invalid()
    return operation
  }
  async operation(workspace: string, parent: string, id: string, signal: AbortSignal) {
    const operation = operationFrom((await this.transport.request(`/api/agri/v1/parcel-operations/${encodeURIComponent(id)}?${this.query(workspace)}`, signal)).envelope.data, workspace, parent)
    if (operation.operation_id !== id) return invalid()
    return operation
  }
}
