import { parseGeometry, type FarmGeometry } from './geometry'
import { FarmError, record } from './transport'
export const capabilities = ['agri.farm.read', 'agri.farm.update', 'agri.farm.archive'] as const
export type Capability = typeof capabilities[number]
export type AreaUnit = 'm2' | 'ha' | 'mu'
export type FarmInput = {
  external_code: string; name: string; operator_name: string
  contact?: { name?: string; email?: string; phone?: string }
  region_code: string; dictionary_version: string; timezone: string; area_unit: AreaUnit
  geometry: FarmGeometry; source_crs: 'EPSG:4326' | 'OGC:CRS84'; coordinate_order: 'longitude_latitude'; data_class: 'demo' | 'operational'
}
export type FarmChanges = Omit<FarmInput, 'external_code' | 'data_class'>
export type Farm = FarmInput & { id: string; workspace_id: string; status: 'active' | 'archived'; revision: number; area_m2: string; area_ha: string; area_mu: string; created_at: string; updated_at: string; allowed_actions: Capability[] }
export type InitialGrant = { subject_user_id: string; capabilities: Capability[]; expires_at?: string }
export type FarmContext = { workspace_id: string; can_create: boolean; members: { user_id: string; display_name: string; roles: string[]; allowed_capabilities: Capability[] }[]; regions: { code: string; name: string; timezones: string[] }[]; dictionary_version: string; area_units: AreaUnit[] }
export type Intent = { action: 'create'; farm: FarmInput; initial_grants: InitialGrant[] } | { action: 'update'; farm_id: string; expected_revision: number; farm: FarmChanges } | { action: 'archive'; farm_id: string; expected_revision: number; reason: string }
export type Preview = { preview_id: string; expires_at: string; request_hash: string; action: Intent['action']; farm_id: string; expected_revision: number; summary: Record<string, unknown> }
export type Operation = { operation_id: string; action: Intent['action']; state: 'preview' | 'pending' | 'completed' | 'rejected' | 'reconciliation_required'; farm_id: string; revision: number; error_code: string; allowed_actions: Capability[]; job_id: null; events_url: null; status_url: string }
export type FarmSummary = Pick<Farm, 'id' | 'name' | 'external_code' | 'status' | 'revision' | 'region_code' | 'timezone' | 'area_m2' | 'area_unit' | 'data_class' | 'allowed_actions'>
export type FarmPage = { items: FarmSummary[]; next_cursor: string; has_more: boolean }
export const invalid = (): never => { throw new FarmError(0, 'INVALID_RESPONSE', '农业数据格式异常，已拒绝使用。') }
const text = (v: unknown, max = 256): v is string => typeof v === 'string' && v.length > 0 && v.length <= max
const id = (v: unknown): v is string => typeof v === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(v)
const farmID = (v: unknown): v is string => typeof v === 'string' && /^farm_[0-9a-f]{32}$/.test(v)
const date = (v: unknown): v is string => typeof v === 'string' && Number.isFinite(Date.parse(v))
const actions = (v: unknown): v is Capability[] => Array.isArray(v) && new Set(v).size === v.length && v.every(x => capabilities.includes(x))
export function contextFrom(v: unknown, workspace: string): FarmContext {
  if (!record(v) || v.workspace_id !== workspace || typeof v.can_create !== 'boolean' || !Array.isArray(v.members) || !Array.isArray(v.regions) || !text(v.dictionary_version) || !Array.isArray(v.area_units) || !v.area_units.length || v.area_units.some(x => !['m2', 'ha', 'mu'].includes(x))) return invalid()
  if (v.members.some(m => !record(m) || !id(m.user_id) || !text(m.display_name) || !Array.isArray(m.roles) || m.roles.some(r => !text(r)) || !actions(m.allowed_capabilities)) || new Set(v.members.map(m => m.user_id)).size !== v.members.length) return invalid()
  if (!v.regions.length || v.regions.some(r => !record(r) || !text(r.code) || !text(r.name) || !Array.isArray(r.timezones) || !r.timezones.length || r.timezones.some(z => { try { if (!text(z)) return true; new Intl.DateTimeFormat('en', { timeZone: z }); return false } catch { return true } })) || new Set(v.regions.map(r => r.code)).size !== v.regions.length) return invalid()
  return structuredClone(v) as FarmContext
}
export function farmFrom(v: unknown, workspace: string): Farm {
  if (!record(v) || !farmID(v.id) || v.workspace_id !== workspace || !['active', 'archived'].includes(String(v.status)) || !Number.isSafeInteger(v.revision) || Number(v.revision) < 1 || !actions(v.allowed_actions) || !date(v.created_at) || !date(v.updated_at)) return invalid()
  for (const key of ['external_code', 'name', 'operator_name', 'region_code', 'dictionary_version', 'timezone']) if (!text(v[key])) return invalid()
  if (!['demo', 'operational'].includes(String(v.data_class)) || !['m2', 'ha', 'mu'].includes(String(v.area_unit)) || !['EPSG:4326', 'OGC:CRS84'].includes(String(v.source_crs)) || v.coordinate_order !== 'longitude_latitude') return invalid()
  for (const key of ['area_m2', 'area_ha', 'area_mu']) if (typeof v[key] !== 'string' || !/^\d+(?:\.\d+)?$/.test(v[key] as string)) return invalid()
  if (v.contact !== undefined && (!record(v.contact) || Object.entries(v.contact).some(([k, value]) => !['name', 'email', 'phone'].includes(k) || typeof value !== 'string'))) return invalid()
  try { parseGeometry(v.geometry) } catch { return invalid() }
  return structuredClone(v) as Farm
}
export function pageFrom(v: unknown, _workspace: string): FarmPage {
  if (!record(v) || !Array.isArray(v.items) || typeof v.next_cursor !== 'string' || v.next_cursor.length > 8192 || typeof v.has_more !== 'boolean' || (v.has_more && !v.next_cursor)) return invalid()
  const items = v.items.map(f => {
    if (!record(f) || !farmID(f.id) || !text(f.name) || !text(f.external_code) || !text(f.region_code) || !text(f.timezone) || !['active', 'archived'].includes(String(f.status)) || !Number.isSafeInteger(f.revision) || Number(f.revision) < 1 || !['demo', 'operational'].includes(String(f.data_class)) || !['m2', 'ha', 'mu'].includes(String(f.area_unit)) || typeof f.area_m2 !== 'string' || !/^\d+(?:\.\d+)?$/.test(f.area_m2) || !actions(f.allowed_actions)) return invalid()
    return structuredClone(f) as FarmSummary
  })
  if (new Set(items.map(f => f.id)).size !== items.length) return invalid()
  return { items, next_cursor: v.next_cursor, has_more: v.has_more }
}
export function previewFrom(v: unknown, intent: Intent): Preview {
  if (!record(v) || !id(v.preview_id) || !date(v.expires_at) || typeof v.request_hash !== 'string' || !/^[a-f0-9]{64}$/.test(v.request_hash) || v.action !== intent.action || v.farm_id !== (intent.action === 'create' ? '' : intent.farm_id) || v.expected_revision !== (intent.action === 'create' ? 0 : intent.expected_revision) || !record(v.summary)) return invalid()
  return structuredClone(v) as Preview
}
export function operationFrom(v: unknown, workspace: string): Operation {
  if (!record(v) || !id(v.operation_id) || !['create', 'update', 'archive'].includes(String(v.action)) || !['preview', 'pending', 'completed', 'rejected', 'reconciliation_required'].includes(String(v.state)) || !(v.farm_id === '' || farmID(v.farm_id)) || !Number.isSafeInteger(v.revision) || Number(v.revision) < 0 || typeof v.error_code !== 'string' || !/^[A-Z_]{0,64}$/.test(v.error_code) || !actions(v.allowed_actions)) return invalid()
  if (v.job_id !== null || v.events_url !== null || v.status_url !== `/api/agri/v1/farm-operations/${encodeURIComponent(v.operation_id)}?workspace_id=${encodeURIComponent(workspace)}`) return invalid()
  return structuredClone(v) as Operation
}
