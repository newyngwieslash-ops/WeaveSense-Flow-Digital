import { expect, test } from 'bun:test'
import { ParcelAPI } from './api'
import { parcelFrom, revisionFrom, timeValue, type Intent, type Operation, type Parcel, type Preview } from './types'
const farm = `farm_${'a'.repeat(32)}`, id = `parcel_${'b'.repeat(32)}`
const geometry = { type: 'Polygon' as const, coordinates: [[[110, 25], [110.01, 25], [110.01, 25.01], [110, 25]]] as [number, number][][] }
export const parcelFixture: Parcel = { id, farm_id: farm, workspace_id: 'w1', geometry_revision: 1, current_geometry_revision: 1, external_code: 'P1', name: 'Parcel', status: 'active', data_class: 'demo', geometry, source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude', area_m2: '100', area_ha: '0.01', area_mu: '0.15', geometry_hash: 'c'.repeat(64), valid_from: '2026-11-01T10:00:00.000001Z', valid_to: null, reason: 'Explicit reason', actor_user_id: 'actor1', created_at: '2026-11-01T10:00:00.000001Z', provenance: { algorithm_version: 'fixture-only' }, affected_dependencies: { availability: 'not_yet_integrated', count: null }, allowed_actions: ['agri.parcel.read', 'agri.parcel.update'] }
const intent: Intent = { action: 'revise', parcel_id: id, expected_revision: 1, geometry, source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude', reason: 'Boundary correction' }
const preview: Preview = { preview_id: 'pop_one', expires_at: '2099-01-01T00:00:00Z', request_hash: 'd'.repeat(64), action: 'revise', farm_id: farm, parcel_id: id, expected_revision: 1, summary: { geometry, reason: intent.reason } }
const operation: Operation = { operation_id: 'pop_one', action: 'revise', state: 'completed', farm_id: farm, parcel_id: id, geometry_revision: 2, error_code: '', job_id: null, events_url: null, status_url: '/api/agri/v1/parcel-operations/pop_one?workspace_id=w1' }
const reply = (data: unknown, status = 200) => ({ status, envelope: { data, meta: { request_id: 'r1', schema_version: '1.0' as const } } })
test('parcel revision writes carry original expected version/preview/reason/key but no client identity', async () => {
  const abort = new AbortController()
  const api = new ParcelAPI({ request: async (path, signal, method, body, key) => {
    expect(path).toBe(`/api/agri/v1/parcels/${id}/revisions`); expect(signal).toBe(abort.signal); expect(method).toBe('POST'); expect(key).toBe('same_key')
    expect(body).toEqual({ workspace_id: 'w1', preview_id: 'pop_one', expected_revision: 1, geometry, source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude', reason: 'Boundary correction' })
    return reply(operation)
  } })
  expect((await api.submit('w1', farm, intent, preview, 'same_key', abort.signal)).geometry_revision).toBe(2)
})
test('parcel preview includes concrete matching geometry/areas/reason, not a bare hash or invented impact zero', async () => {
  const summary = { geometry, source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude', farm_id: farm, parcel_id: id, expected_revision: 1, reason: intent.reason, geometry_hash: 'f'.repeat(64), area_m2: '100', area_ha: '0.01', area_mu: '0.15', affected_dependencies: { availability: 'not_yet_integrated', count: null } }
  const full = { ...preview, summary }
  expect((await new ParcelAPI({ request: async () => reply(full) }).preview('w1', farm, intent, new AbortController().signal)).summary.reason).toBe(intent.reason)
  for (const changes of [{ summary: {} }, { summary: { ...summary, reason: 'different' } }, { summary: { ...summary, affected_dependencies: { availability: 'not_yet_integrated', count: 0 } } }, { summary: { ...summary, source_crs: 'OGC:CRS84' } }]) await expect(new ParcelAPI({ request: async () => reply({ ...full, ...changes }) }).preview('w1', farm, intent, new AbortController().signal)).rejects.toThrow()
})
test('parcel create explicitly distinguishes zero new revision and positive parent revision', async () => {
  const input: Intent = { action: 'create', farm_id: farm, expected_revision: 0, expected_farm_revision: 5, parcel: { external_code: 'P1', name: 'Parcel', geometry, source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude' }, reason: 'Initial boundary' }
  const api = new ParcelAPI({ request: async (path, _s, method, body) => {
    expect(path).toBe(`/api/agri/v1/farms/${farm}/parcels`); expect(method).toBe('POST')
    expect(body).toEqual({ workspace_id: 'w1', preview_id: 'pop_one', expected_revision: 0, expected_farm_revision: 5, parcel: input.parcel, reason: input.reason })
    return reply({ ...operation, action: 'create', geometry_revision: 1 }, 201)
  } })
  await api.submit('w1', farm, input, { ...preview, action: 'create', parcel_id: '', expected_revision: 0 }, 'same_key', new AbortController().signal)
})
test('unknown/reconciliation receipts remain pending and status/null job references are strict', async () => {
  const pending = { ...operation, state: 'reconciliation_required' }
  expect((await new ParcelAPI({ request: async () => reply(pending, 202) }).submit('w1', farm, intent, preview, 'key', new AbortController().signal)).state).toBe('reconciliation_required')
  for (const [value, status] of [[pending, 200], [operation, 202], [{ ...operation, state: 'preview' }, 202], [{ ...operation, state: 'rejected' }, 202], [{ ...operation, status_url: 'https://external.invalid' }, 200], [{ ...operation, job_id: 'fake' }, 200], [{ ...operation, parcel_id: `parcel_${'e'.repeat(32)}` }, 200]] as const) await expect(new ParcelAPI({ request: async () => reply(value, status) }).submit('w1', farm, intent, preview, 'key', new AbortController().signal)).rejects.toThrow()
})
test('historical selectors bind returned ID/version/time and preserve submillisecond validity', async () => {
  const old = { ...parcelFixture, current_geometry_revision: 2, valid_to: '2026-11-01T10:00:00.000003Z', allowed_actions: ['agri.parcel.read'] }
  const at = '2026-11-01T10:00:00.000002Z'
  const api = new ParcelAPI({ request: async path => { expect(path).toContain('at=2026-11-01T10%3A00%3A00.000002Z'); return reply(old) } })
  expect((await api.detail('w1', farm, id, new AbortController().signal, { at })).geometry_revision).toBe(1)
  expect(timeValue(old.valid_to) - timeValue(old.valid_from)).toBe(2000n)
  expect(() => revisionFrom({ ...old, valid_to: old.valid_from }, 'w1', farm)).toThrow()
  expect(() => parcelFrom({ ...old, allowed_actions: ['agri.parcel.update'] }, 'w1', farm)).toThrow()
  await expect(new ParcelAPI({ request: async () => reply(old) }).detail('w1', farm, id, new AbortController().signal)).rejects.toThrow()
  for (const selector of [{ revision: 2 }, { at: old.valid_to }, { at, revision: 1 }, { at: '2026-02-30T00:00:00Z' }]) await expect(new ParcelAPI({ request: async () => reply(old) }).detail('w1', farm, id, new AbortController().signal, selector)).rejects.toThrow()
})
test('parcel context and list bind parent/workspace, preserve canonical cursor and reject unknown capabilities', async () => {
  const api = new ParcelAPI({ request: async path => {
    if (path.includes('/parcel-context?')) return reply({ workspace_id: 'w1', farm_id: farm, expected_farm_revision: 5, allowed_actions: ['agri.parcel.create'] })
    expect(path).toContain('limit=50'); expect(path).toContain('cursor=opaque_cursor')
    return { status: 200, envelope: { ...reply([]).envelope, data: [parcelFixture], next_cursor: '', has_more: false } }
  } })
  expect((await api.context('w1', farm, new AbortController().signal)).expected_farm_revision).toBe(5)
  expect((await api.list('w1', farm, new AbortController().signal, 'opaque_cursor')).items[0]?.id).toBe(id)
  for (const changes of [{ farm_id: `farm_${'c'.repeat(32)}` }, { expected_farm_revision: -1 }, { expected_farm_revision: 0, allowed_actions: ['agri.parcel.create'] }, { allowed_actions: ['agri.parcel.delete'] }]) await expect(new ParcelAPI({ request: async () => reply({ workspace_id: 'w1', farm_id: farm, expected_farm_revision: 5, allowed_actions: ['agri.parcel.read'], ...changes }) }).context('w1', farm, new AbortController().signal)).rejects.toThrow()
  expect((await new ParcelAPI({ request: async () => reply({ workspace_id: 'w1', farm_id: farm, expected_farm_revision: 0, allowed_actions: ['agri.parcel.read'] }) }).context('w1', farm, new AbortController().signal)).expected_farm_revision).toBe(0)
})
test('direct parcel-grant parent discovery returns only explicit scoped contexts and permits zero parent disclosure', async () => {
  const context = { workspace_id: 'w1', farm_id: farm, expected_farm_revision: 0, allowed_actions: ['agri.parcel.read'] }
  const value = { ...reply([context]), envelope: { ...reply([context]).envelope, next_cursor: '', has_more: false } }
  const api = new ParcelAPI({ request: async (path, signal, method, body) => { expect(path).toBe('/api/agri/v1/parcel-farms?workspace_id=w1&limit=50'); expect(signal).toBeInstanceOf(AbortSignal); expect(method).toBeUndefined(); expect(body).toBeUndefined(); return value } })
  expect((await api.farms('w1', new AbortController().signal)).items).toEqual([context])
  for (const items of [[{ ...context, workspace_id: 'other' }], [context, context], [{ ...context, allowed_actions: [] }]]) await expect(new ParcelAPI({ request: async () => ({ ...value, envelope: { ...value.envelope, data: items } }) }).farms('w1', new AbortController().signal)).rejects.toThrow()
})
test('same tenant foreign parent/workspace, malformed geometry, guessed dependencies and wrong history IDs rejected', async () => {
  for (const changes of [{ workspace_id: 'other' }, { farm_id: `farm_${'e'.repeat(32)}` }, { geometry: { type: 'Polygon', coordinates: [] } }, { affected_dependencies: { availability: 'not_yet_integrated', count: 0 } }]) expect(() => parcelFrom({ ...parcelFixture, ...changes }, 'w1', farm)).toThrow()
  const api = new ParcelAPI({ request: async () => ({ ...reply([{ ...parcelFixture, id: `parcel_${'e'.repeat(32)}` }]), envelope: { ...reply([]).envelope, data: [{ ...parcelFixture, id: `parcel_${'e'.repeat(32)}` }], next_cursor: '', has_more: false } }) })
  await expect(api.history('w1', farm, id, new AbortController().signal)).rejects.toThrow()
})
