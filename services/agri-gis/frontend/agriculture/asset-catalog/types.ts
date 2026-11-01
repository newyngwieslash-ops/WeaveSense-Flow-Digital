import { record } from '../farms/transport'
import { farmID, instant, invalid, kinds, metadataFrom, reference, sourceKinds, text, timestamp, type Metadata } from './metadata'
export const capabilities = ['agri.asset.read', 'agri.asset.create', 'agri.asset_download.read'] as const
export type Capability = typeof capabilities[number]
export type Grant = { subject_user_id: string; capabilities: Capability[]; expires_at?: string }
export type Context = { workspace_id: string; can_register_without_farm: boolean; eligible_grantees: { user_id: string; display_name: string; roles: string[]; allowed_capabilities: Capability[] }[]; farms: { farm_id: string; name: string; revision: number }[]; sources: { source_id: string; kind: Metadata['source']['kind']; label: string }[] }
export type Rights = { status: 'pending' | 'approved' | 'revoked' | 'prohibited'; snapshot_hash: string; review_ref: string | null }
export type Asset = { asset_id: string; workspace_id: string; farm_id: string | null; revision: number; version: number; asset_version_id: string; kind: Metadata['kind']; data_class: Metadata['data_class']; name: string; status: 'validating' | 'ready' | 'quarantined' | 'rejected' | 'revoked'; metadata: Metadata; metadata_hash: string; rights: Rights; entity: null | { object_id: string; object_version: number }; created_at: string; allowed_actions: Capability[] }
export type Page = { items: Asset[]; next_cursor: string; has_more: boolean }
export type Intent = { action: 'create'; expected_revision: 0; metadata: Metadata; initial_grants: Grant[] } | { action: 'version'; asset_id: string; expected_revision: number; metadata: Metadata }
export type Preview = { preview_id: string; expires_at: string; request_hash: string; action: Intent['action']; asset_id: string; expected_revision: number; summary: { metadata: Metadata; rights: Rights; entity: null; status: 'validating' }; initial_grants: Grant[] }
export type Operation = { operation_id: string; action: Intent['action']; state: 'preview' | 'pending' | 'completed' | 'rejected' | 'reconciliation_required'; asset_id: string; revision: number; asset_version_id: string; error_code: string; job_id: null; events_url: null; status_url: string }
export const id = (v: unknown): v is string => typeof v === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(v)
export const assetID = (v: unknown): v is string => typeof v === 'string' && /^asset_[a-f0-9]{32}$/.test(v)
export const versionID = (v: unknown): v is string => typeof v === 'string' && /^av_[a-f0-9]{32}$/.test(v)
export const operationID = (v: unknown): v is string => typeof v === 'string' && /^aop_[A-Za-z0-9_-]{1,120}$/.test(v)
export const positive = (v: unknown): v is number => Number.isSafeInteger(v) && Number(v) >= 1
export const hash = (v: unknown): v is string => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v)
const actions = (v: unknown): v is Capability[] => Array.isArray(v) && new Set(v).size === v.length && v.every(c => capabilities.includes(c))
const canonical = (value: unknown): string => JSON.stringify(value, (_key, v) => record(v) ? Object.fromEntries(Object.keys(v).sort().map(k => [k, v[k]])) : v)
const grantIdentity = (grants: Grant[]) => canonical(grants.map(g => ({ ...g, ...(g.expires_at ? { expires_at: instant(g.expires_at).toString() } : {}) })))
export function contextFrom(v: unknown, workspace: string): Context {
  if (!record(v) || v.workspace_id !== workspace || typeof v.can_register_without_farm !== 'boolean' || !Array.isArray(v.eligible_grantees) || !Array.isArray(v.farms) || !Array.isArray(v.sources)) return invalid()
  if (v.eligible_grantees.some(x => !record(x) || !id(x.user_id) || !text(x.display_name, 256) || !Array.isArray(x.roles) || x.roles.some(r => !text(r, 128)) || !actions(x.allowed_capabilities)) || new Set(v.eligible_grantees.map(x => x.user_id)).size !== v.eligible_grantees.length) return invalid()
  if (v.farms.some(x => !record(x) || !farmID(x.farm_id) || !text(x.name, 256) || !positive(x.revision)) || new Set(v.farms.map(x => x.farm_id)).size !== v.farms.length) return invalid()
  if (v.sources.some(x => !record(x) || !reference(x.source_id) || !sourceKinds.includes(x.kind as Metadata['source']['kind']) || !text(x.label, 256)) || new Set(v.sources.map(x => x.source_id)).size !== v.sources.length) return invalid()
  return structuredClone(v) as Context
}
export function rightsFrom(v: unknown): Rights {
  if (!record(v) || !['pending', 'approved', 'revoked', 'prohibited'].includes(String(v.status)) || !hash(v.snapshot_hash) || !(v.review_ref === null || text(v.review_ref, 2048))) return invalid()
  // Only public summary fields enter Vue state; never copy registry internals.
  return { status: v.status as Rights['status'], snapshot_hash: v.snapshot_hash, review_ref: v.review_ref as string | null }
}
export function assetFrom(v: unknown, workspace: string): Asset {
  if (!record(v) || !assetID(v.asset_id) || v.workspace_id !== workspace || !(v.farm_id === null || farmID(v.farm_id)) || !positive(v.revision) || !positive(v.version) || v.version > v.revision || !versionID(v.asset_version_id) || !kinds.includes(v.kind as Metadata['kind']) || !['demo', 'operational'].includes(String(v.data_class)) || !text(v.name, 200) || !['validating', 'ready', 'quarantined', 'rejected', 'revoked'].includes(String(v.status)) || !hash(v.metadata_hash) || !timestamp(v.created_at) || !actions(v.allowed_actions)) return invalid()
  const metadata = metadataFrom(v.metadata), rights = rightsFrom(v.rights)
  if (metadata.farm_id !== v.farm_id || metadata.kind !== v.kind || metadata.data_class !== v.data_class || metadata.name !== v.name || (v.version !== v.revision && v.allowed_actions.includes('agri.asset.create'))) return invalid()
  if (v.entity !== null && (!record(v.entity) || Object.keys(v.entity).sort().join(',') !== 'object_id,object_version' || !id(v.entity.object_id) || !positive(v.entity.object_version))) return invalid()
  if (v.version === v.revision && v.status === 'ready' && v.entity === null) return invalid()
  return { ...(structuredClone(v) as Asset), metadata, rights }
}
export function pageFrom(items: unknown, next_cursor: unknown, has_more: unknown, workspace: string): Page {
  if (!Array.isArray(items) || typeof next_cursor !== 'string' || next_cursor.length > 8192 || typeof has_more !== 'boolean' || (has_more && !next_cursor)) return invalid()
  const rows = items.map(v => assetFrom(v, workspace)); if (new Set(rows.map(v => v.asset_id)).size !== rows.length || rows.some(v => v.version !== v.revision)) return invalid()
  return { items: rows, next_cursor, has_more }
}
export function grantsFrom(v: unknown): Grant[] {
  if (!Array.isArray(v) || v.length > 20 || v.some(g => !record(g) || Object.keys(g).some(k => !['subject_user_id', 'capabilities', 'expires_at'].includes(k)) || !id(g.subject_user_id) || !actions(g.capabilities) || !g.capabilities.length || (g.expires_at !== undefined && !timestamp(g.expires_at))) || new Set(v.map(g => g.subject_user_id)).size !== v.length) return invalid()
  return v.map(g => ({ ...structuredClone(g), capabilities: [...g.capabilities].sort() })).sort((a, b) => a.subject_user_id.localeCompare(b.subject_user_id))
}
export function previewFrom(v: unknown, intent: Intent): Preview {
  if (!record(v) || !operationID(v.preview_id) || !timestamp(v.expires_at) || !hash(v.request_hash) || v.action !== intent.action || v.asset_id !== (intent.action === 'create' ? '' : intent.asset_id) || v.expected_revision !== intent.expected_revision || !record(v.summary) || v.summary.entity !== null || v.summary.status !== 'validating') return invalid()
  const metadata = metadataFrom(v.summary.metadata), rights = rightsFrom(v.summary.rights), initial_grants = grantsFrom(v.initial_grants)
  if (canonical(metadata) !== canonical(metadataFrom(intent.metadata)) || grantIdentity(initial_grants) !== grantIdentity(grantsFrom(intent.action === 'create' ? intent.initial_grants : [])) || (intent.action === 'create' && !initial_grants.length)) return invalid()
  return { ...(structuredClone(v) as Preview), summary: { metadata, rights, entity: null, status: 'validating' }, initial_grants }
}
export function operationFrom(v: unknown, workspace: string): Operation {
  if (!record(v) || !operationID(v.operation_id) || !['create', 'version'].includes(String(v.action)) || !['preview', 'pending', 'completed', 'rejected', 'reconciliation_required'].includes(String(v.state)) || !(v.asset_id === '' || assetID(v.asset_id)) || !Number.isSafeInteger(v.revision) || Number(v.revision) < 0 || !(v.asset_version_id === '' || versionID(v.asset_version_id)) || typeof v.error_code !== 'string' || !/^[A-Z_]{0,64}$/.test(v.error_code) || v.job_id !== null || v.events_url !== null || v.status_url !== `/api/agri/v1/asset-operations/${encodeURIComponent(v.operation_id)}?workspace_id=${encodeURIComponent(workspace)}`) return invalid()
  if (v.state === 'completed' && (!assetID(v.asset_id) || !versionID(v.asset_version_id) || !positive(v.revision))) return invalid()
  return structuredClone(v) as Operation
}
