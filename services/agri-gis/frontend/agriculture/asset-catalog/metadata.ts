import { FarmError, record } from '../farms/transport'
export const kinds = ['raster', 'vector', 'table', 'photo', 'audio', 'model'] as const
export const sourceKinds = ['customer_data', 'public_imagery', 'third_party'] as const
export const purposes = ['ingest', 'analysis', 'report_publish', 'export', 'external_share', 'model_training'] as const
export const sensitivities = ['public', 'internal', 'confidential', 'restricted'] as const
export type Metadata = {
  name: string; kind: typeof kinds[number]; data_class: 'demo' | 'operational'; farm_id: string | null
  source: { kind: typeof sourceKinds[number]; source_id: string | null; reference: string }
  rights_declaration: { owner: string | null; license_ref: string | null; permitted_uses: typeof purposes[number][]; attribution: { required: boolean; text: string | null }; valid_from: string | null; expiry: string | null }
  sensitivity: typeof sensitivities[number]; retention: { retain_until: string | null; legal_hold: boolean }
}
export const invalid = (): never => { throw new FarmError(0, 'INVALID_RESPONSE', '资产目录响应不符合严格合同，已拒绝使用。') }
const exact = (v: unknown, fields: string[]): v is Record<string, unknown> => record(v) && Object.keys(v).sort().join(',') === fields.sort().join(',')
export const text = (v: unknown, max: number): v is string => typeof v === 'string' && v.length > 0 && v.length <= max && v.trim() === v && !/[\x00-\x1f\x7f]/.test(v)
export const reference = (v: unknown): v is string => typeof v === 'string' && /^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$/.test(v)
export const farmID = (v: unknown): v is string => typeof v === 'string' && /^farm_[a-f0-9]{32}$/.test(v)
export function timestamp(v: unknown): v is string {
  // GIS metadata is persisted at microsecond precision; service receipt clocks
  // are separate and may use Go's RFC3339Nano representation.
  return serviceTimestamp(v) && !/\.\d{7,}/.test(v)
}
export function serviceTimestamp(v: unknown): v is string {
  if (typeof v !== 'string') return false
  const m = /^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/.exec(v)
  if (!m || !Number.isFinite(Date.parse(v))) return false
  const year = Number(m[1]), month = Number(m[2]), day = Number(m[3]), days = [31, year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0) ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  return year >= 1 && month >= 1 && month <= 12 && day >= 1 && day <= days[month - 1]! && Number(m[4]) <= 23 && Number(m[5]) <= 59 && Number(m[6]) <= 59 && (m[8] === 'Z' || (Number(m[8]!.slice(1, 3)) <= 23 && Number(m[8]!.slice(4)) <= 59))
}
export function instant(value: string): bigint {
  if (!serviceTimestamp(value)) return invalid()
  const base = value.replace(/\.\d+(?=Z|[+-]\d\d:\d\d$)/, '')
  return BigInt(Date.parse(base)) * 1000000n + BigInt((value.match(/\.(\d+)(?:Z|[+-]\d\d:\d\d)$/)?.[1] ?? '').padEnd(9, '0'))
}
export function metadataFrom(value: unknown): Metadata {
  if (!exact(value, ['name', 'kind', 'data_class', 'farm_id', 'source', 'rights_declaration', 'sensitivity', 'retention']) || !text(value.name, 200) || !kinds.includes(value.kind as Metadata['kind']) || !['demo', 'operational'].includes(String(value.data_class)) || !(value.farm_id === null || farmID(value.farm_id)) || !sensitivities.includes(value.sensitivity as Metadata['sensitivity'])) return invalid()
  const source = value.source, rights = value.rights_declaration, retention = value.retention
  if (!exact(source, ['kind', 'source_id', 'reference']) || !sourceKinds.includes(source.kind as Metadata['source']['kind']) || !(source.source_id === null || reference(source.source_id)) || !reference(source.reference)) return invalid()
  if (!exact(rights, ['owner', 'license_ref', 'permitted_uses', 'attribution', 'valid_from', 'expiry']) || !(rights.owner === null || text(rights.owner, 200)) || !(rights.license_ref === null || reference(rights.license_ref)) || !Array.isArray(rights.permitted_uses) || new Set(rights.permitted_uses).size !== rights.permitted_uses.length || rights.permitted_uses.some(v => !purposes.includes(v)) || !exact(rights.attribution, ['required', 'text']) || typeof rights.attribution.required !== 'boolean' || !(rights.attribution.text === null || text(rights.attribution.text, 2048)) || !(rights.valid_from === null || timestamp(rights.valid_from)) || !(rights.expiry === null || timestamp(rights.expiry))) return invalid()
  if (rights.valid_from !== null && rights.expiry !== null && instant(rights.expiry as string) <= instant(rights.valid_from as string)) return invalid()
  if (!exact(retention, ['retain_until', 'legal_hold']) || !(retention.retain_until === null || timestamp(retention.retain_until)) || typeof retention.legal_hold !== 'boolean') return invalid()
  const copy = structuredClone(value) as Metadata; copy.rights_declaration.permitted_uses.sort(); return copy
}
export function successorFrom(previous: Metadata, value: unknown): Metadata {
  const next = metadataFrom(value)
  if (['kind', 'farm_id', 'data_class'].some(k => previous[k as keyof Metadata] !== next[k as keyof Metadata])) throw new Error('后继版本不能改变资产类别、所属农场或演示/正式等级。')
  if (previous.retention.legal_hold && !next.retention.legal_hold) throw new Error('此入口不能解除法律保留。')
  if (next.retention.retain_until !== null && (previous.retention.retain_until === null || instant(next.retention.retain_until) < instant(previous.retention.retain_until))) throw new Error('后继版本不能缩短已确定的保留期限。无截止日期代表持续保留。')
  if (sensitivities.indexOf(next.sensitivity) < sensitivities.indexOf(previous.sensitivity)) throw new Error('此入口不能降低敏感等级。')
  return next
}
