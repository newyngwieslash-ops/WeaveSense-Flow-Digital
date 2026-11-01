import { expect, test } from 'bun:test'
import { instant, metadataFrom, successorFrom, timestamp, type Metadata } from './metadata'
const metadata = (): Metadata => ({ name: 'Unverified asset', kind: 'raster', data_class: 'demo', farm_id: null, source: { kind: 'customer_data', source_id: null, reference: 'customer-record-1' }, rights_declaration: { owner: null, license_ref: null, permitted_uses: [], attribution: { required: false, text: null }, valid_from: null, expiry: null }, sensitivity: 'internal', retention: { retain_until: null, legal_hold: false } })
test('metadata claims keep null-farm and unknown rights without inventing ready/approved/entity', () => {
  const input = metadata(); expect(metadataFrom(input)).toEqual(input)
  const value = metadataFrom({ ...input, rights_declaration: { ...input.rights_declaration, permitted_uses: ['model_training', 'ingest'] } })
  expect(value.rights_declaration.permitted_uses).toEqual(['ingest', 'model_training']); expect(input.rights_declaration.permitted_uses).toEqual([])
  for (const extra of [{ approved: true }, { status: 'ready' }, { entity: { key: 'forged' } }, { commercial_allowed: true }]) expect(() => metadataFrom({ ...input, ...extra })).toThrow()
})
test('metadata rejects arbitrary URLs, credentials/identity extras, foreign fields and unknown purposes', () => {
  const input = metadata()
  for (const reference of ['https://example.invalid/file', '../private', 'opaque\nsecret', 'user:pass@host']) expect(() => metadataFrom({ ...input, source: { ...input.source, reference } })).toThrow()
  for (const extra of [{ endpoint: 'https://example.invalid' }, { token: 'secret' }, { tenant_id: 'other' }]) expect(() => metadataFrom({ ...input, source: { ...input.source, ...extra } })).toThrow()
  for (const permitted_uses of [['execute'], ['ingest', 'ingest']]) expect(() => metadataFrom({ ...input, rights_declaration: { ...input.rights_declaration, permitted_uses } })).toThrow()
})
test('successor preserves farm/type/class, legal holds, non-shortening retention and sensitivity', () => {
  const previous = { ...metadata(), sensitivity: 'confidential' as const, retention: { retain_until: '2027-01-01T00:00:00Z', legal_hold: true } }
  for (const patch of [{ farm_id: `farm_${'a'.repeat(32)}` }, { kind: 'model' }, { data_class: 'operational' }, { sensitivity: 'internal' }, { retention: { ...previous.retention, legal_hold: false } }, { retention: { ...previous.retention, retain_until: '2026-12-31T23:59:59Z' } }]) expect(() => successorFrom(previous, { ...previous, ...patch })).toThrow()
  expect(successorFrom(previous, { ...previous, name: 'Renamed but same identity', retention: { retain_until: null, legal_hold: true } }).retention.retain_until).toBeNull()
  expect(() => successorFrom(metadata(), { ...metadata(), retention: { retain_until: '2027-01-01T00:00:00Z', legal_hold: false } })).toThrow()
})
test('rights timestamps reject invalid dates and keep microsecond/order semantics across timezone offsets', () => {
  expect(timestamp('2026-02-30T00:00:00Z')).toBe(false); expect(timestamp('2028-02-29T00:00:00Z')).toBe(true); expect(timestamp('2026-01-01')).toBe(false)
  expect(instant('2026-11-01T10:00:00.000001Z')).toBe(instant('2026-11-01T18:00:00.000001+08:00'))
  const value = metadata(); value.rights_declaration.valid_from = '2026-11-01T10:00:00.000001Z'; value.rights_declaration.expiry = '2026-11-01T10:00:00.000002Z'
  expect(metadataFrom(value).rights_declaration.expiry).toBe(value.rights_declaration.expiry)
  expect(() => metadataFrom({ ...value, rights_declaration: { ...value.rights_declaration, expiry: value.rights_declaration.valid_from } })).toThrow()
})
