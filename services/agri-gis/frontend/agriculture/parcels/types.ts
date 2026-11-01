import { parseGeometry, type FarmGeometry } from '../farms/geometry'
import { FarmError, record } from '../farms/transport'

export const capabilities = ['agri.parcel.create', 'agri.parcel.read', 'agri.parcel.update'] as const
export type Capability = typeof capabilities[number]
export type Boundary = { geometry: FarmGeometry; source_crs: 'EPSG:4326' | 'OGC:CRS84'; coordinate_order: 'longitude_latitude' }
export type ParcelInput = Boundary & { external_code: string; name: string }
export type Intent = { action: 'create'; farm_id: string; expected_revision: 0; expected_farm_revision: number; parcel: ParcelInput; reason: string } | (Boundary & { action: 'revise'; parcel_id: string; expected_revision: number; reason: string })
export type Selector = { revision: number } | { at: string } | undefined
export type Impact = { availability: 'not_yet_integrated'; count: null }
export type Preview = { preview_id: string; expires_at: string; request_hash: string; action: Intent['action']; farm_id: string; parcel_id: string; expected_revision: number; summary: Record<string, unknown> }
export type Operation = { operation_id: string; action: Intent['action']; state: 'preview' | 'pending' | 'completed' | 'rejected' | 'reconciliation_required'; farm_id: string; parcel_id: string; geometry_revision: number; error_code: string; job_id: null; events_url: null; status_url: string }
export type RevisionSummary = { id: string; workspace_id: string; farm_id: string; geometry_revision: number; current_geometry_revision: number; valid_from: string; valid_to: string | null; created_at: string; reason: string; actor_user_id: string; geometry_hash: string; area_m2: string; area_ha: string; area_mu: string }
export type Parcel = RevisionSummary & ParcelInput & { status: 'active'; data_class: 'demo' | 'operational'; provenance: Record<string, unknown>; affected_dependencies: Impact; allowed_actions: Capability[] }
export type HistoryPage = { items: RevisionSummary[]; next_cursor: string; has_more: boolean }
export type ParcelContext = { workspace_id: string; farm_id: string; expected_farm_revision: number; allowed_actions: Capability[] }
export type ContextPage = { items: ParcelContext[]; next_cursor: string; has_more: boolean }
export type ParcelSummary = Pick<Parcel, 'id' | 'workspace_id' | 'farm_id' | 'name' | 'external_code' | 'status' | 'data_class' | 'geometry_revision' | 'current_geometry_revision' | 'area_m2' | 'allowed_actions'>
export type ParcelPage = { items: ParcelSummary[]; next_cursor: string; has_more: boolean }
export const invalid = (): never => { throw new FarmError(0, 'INVALID_RESPONSE', '地块响应格式异常，已拒绝使用。') }
export const id = (v: unknown): v is string => typeof v === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(v)
export const farmID = (v: unknown): v is string => typeof v === 'string' && /^farm_[a-f0-9]{32}$/.test(v)
export const parcelID = (v: unknown): v is string => typeof v === 'string' && /^parcel_[a-f0-9]{32}$/.test(v)
export const operationID = (v: unknown): v is string => typeof v === 'string' && /^pop_[A-Za-z0-9_-]{1,120}$/.test(v)
export const positive = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) > 0
export const date = (v: unknown): v is string => typeof v === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(v) && Number.isFinite(Date.parse(v)) && new Date(v).toISOString().slice(0, 19) === v.slice(0, 19)
// PostgreSQL timestamps can differ within one JS millisecond; retain sub-ms
// precision when checking immutable revision interval boundaries.
export function timeValue(v: string): bigint {
  if (!date(v)) return invalid()
  return BigInt(Date.parse(v.slice(0, 19) + 'Z')) * 1000000n + BigInt((v.match(/\.(\d+)Z$/)?.[1] ?? '').padEnd(9, '0'))
}
export const hash = (v: unknown): v is string => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v)
export const text = (v: unknown, max = 500): v is string => typeof v === 'string' && v.trim().length > 0 && v.length <= max
export const decimal = (v: unknown): v is string => typeof v === 'string' && /^\d+(?:\.\d+)?$/.test(v)
export const actions = (v: unknown): v is Capability[] => Array.isArray(v) && new Set(v).size === v.length && v.every(x => capabilities.includes(x))
export function boundaryFrom(v: unknown): Boundary {
  if (!record(v) || !['EPSG:4326', 'OGC:CRS84'].includes(String(v.source_crs)) || v.coordinate_order !== 'longitude_latitude') return invalid()
  try { return { geometry: parseGeometry(v.geometry), source_crs: v.source_crs as Boundary['source_crs'], coordinate_order: 'longitude_latitude' } } catch { return invalid() }
}
export function contextFrom(v: unknown, workspace: string, parent: string): ParcelContext {
  if (!record(v) || v.workspace_id !== workspace || v.farm_id !== parent || !Number.isSafeInteger(v.expected_farm_revision) || Number(v.expected_farm_revision) < 0 || !actions(v.allowed_actions) || (v.allowed_actions.includes('agri.parcel.create') && !positive(v.expected_farm_revision))) return invalid()
  return structuredClone(v) as ParcelContext
}
export function contextPageFrom(items: unknown, next_cursor: unknown, has_more: unknown, workspace: string): ContextPage {
  if (!Array.isArray(items) || typeof next_cursor !== 'string' || next_cursor.length > 8192 || typeof has_more !== 'boolean' || (has_more && !next_cursor)) return invalid()
  const rows = items.map(v => { if (!record(v) || !farmID(v.farm_id)) return invalid(); return contextFrom(v, workspace, v.farm_id) })
  if (new Set(rows.map(v => v.farm_id)).size !== rows.length || rows.some(v => !v.allowed_actions.length)) return invalid()
  return { items: rows, next_cursor, has_more }
}
export function pageFrom(items: unknown, next_cursor: unknown, has_more: unknown, workspace: string, parent: string): ParcelPage {
  if (!Array.isArray(items) || typeof next_cursor !== 'string' || next_cursor.length > 8192 || typeof has_more !== 'boolean' || (has_more && !next_cursor)) return invalid()
  for (const v of items) if (!record(v) || !parcelID(v.id) || v.workspace_id !== workspace || v.farm_id !== parent || !text(v.name, 200) || !text(v.external_code, 64) || v.status !== 'active' || !['demo', 'operational'].includes(String(v.data_class)) || !positive(v.geometry_revision) || v.geometry_revision !== v.current_geometry_revision || !decimal(v.area_m2) || !actions(v.allowed_actions)) return invalid()
  if (new Set(items.map(v => v.id)).size !== items.length) return invalid()
  return { items: structuredClone(items) as ParcelSummary[], next_cursor, has_more }
}
export function revisionFrom(v: unknown, workspace: string, parent: string): RevisionSummary {
  if (!record(v) || !parcelID(v.id) || v.workspace_id !== workspace || v.farm_id !== parent || !positive(v.geometry_revision) || !positive(v.current_geometry_revision) || v.geometry_revision > v.current_geometry_revision || !date(v.valid_from) || !(v.valid_to === null || (date(v.valid_to) && timeValue(v.valid_to) > timeValue(v.valid_from))) || !date(v.created_at) || !text(v.reason) || !id(v.actor_user_id) || !hash(v.geometry_hash) || !['area_m2', 'area_ha', 'area_mu'].every(k => decimal(v[k]))) return invalid()
  return structuredClone(v) as RevisionSummary
}
export function parcelFrom(v: unknown, workspace: string, parent: string): Parcel {
  revisionFrom(v, workspace, parent); boundaryFrom(v)
  if (!record(v) || !text(v.name, 200) || typeof v.external_code !== 'string' || !/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(v.external_code) || v.status !== 'active' || !['demo', 'operational'].includes(String(v.data_class)) || !record(v.provenance) || !record(v.affected_dependencies) || v.affected_dependencies.availability !== 'not_yet_integrated' || v.affected_dependencies.count !== null || !actions(v.allowed_actions)) return invalid()
  if (v.geometry_revision !== v.current_geometry_revision && v.allowed_actions.some(a => a !== 'agri.parcel.read')) return invalid()
  return structuredClone(v) as Parcel
}
export function historyFrom(items: unknown, next_cursor: unknown, has_more: unknown, workspace: string, parent: string, parcel: string): HistoryPage {
  if (!Array.isArray(items) || typeof next_cursor !== 'string' || next_cursor.length > 8192 || typeof has_more !== 'boolean' || (has_more && !next_cursor)) return invalid()
  const rows = items.map(v => revisionFrom(v, workspace, parent))
  if (rows.some(v => v.id !== parcel) || new Set(rows.map(v => v.geometry_revision)).size !== rows.length) return invalid()
  return { items: rows, next_cursor, has_more }
}
export function previewFrom(v: unknown, intent: Intent, parent: string): Preview {
  if (!record(v) || !operationID(v.preview_id) || !date(v.expires_at) || !hash(v.request_hash) || v.action !== intent.action || v.farm_id !== parent || v.parcel_id !== (intent.action === 'create' ? '' : intent.parcel_id) || v.expected_revision !== intent.expected_revision || !record(v.summary)) return invalid()
  const summary = v.summary, expected = intent.action === 'create' ? intent.parcel : intent
  const boundary = boundaryFrom(summary)
  if (summary.farm_id !== parent || summary.parcel_id !== v.parcel_id || summary.expected_revision !== intent.expected_revision || summary.reason !== intent.reason || !hash(summary.geometry_hash) || !['area_m2', 'area_ha', 'area_mu'].every(k => decimal(summary[k])) || !record(summary.affected_dependencies) || summary.affected_dependencies.availability !== 'not_yet_integrated' || summary.affected_dependencies.count !== null || boundary.source_crs !== expected.source_crs || boundary.geometry.type !== expected.geometry.type || JSON.stringify(boundary.geometry.coordinates) !== JSON.stringify(expected.geometry.coordinates)) return invalid()
  if (intent.action === 'create' && (summary.external_code !== intent.parcel.external_code || summary.name !== intent.parcel.name)) return invalid()
  return structuredClone(v) as Preview
}
export function operationFrom(v: unknown, workspace: string, parent: string): Operation {
  if (!record(v) || !operationID(v.operation_id) || !['create', 'revise'].includes(String(v.action)) || !['preview', 'pending', 'completed', 'rejected', 'reconciliation_required'].includes(String(v.state)) || v.farm_id !== parent || !(v.parcel_id === '' || parcelID(v.parcel_id)) || !Number.isSafeInteger(v.geometry_revision) || Number(v.geometry_revision) < 0 || typeof v.error_code !== 'string' || !/^[A-Z_]{0,64}$/.test(v.error_code)) return invalid()
  if (v.job_id !== null || v.events_url !== null || v.status_url !== `/api/agri/v1/parcel-operations/${encodeURIComponent(v.operation_id)}?workspace_id=${encodeURIComponent(workspace)}` || (v.state === 'completed' && (!parcelID(v.parcel_id) || !positive(v.geometry_revision)))) return invalid()
  return structuredClone(v) as Operation
}
