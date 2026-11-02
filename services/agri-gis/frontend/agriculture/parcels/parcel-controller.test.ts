import { expect, test } from 'bun:test'
import { ParcelController } from './parcel-controller'
import { FarmError } from '../farms/transport'
import type { Intent, Operation, Parcel, ParcelContext, ParcelPage, Preview, HistoryPage } from './types'
const farm = `farm_${'a'.repeat(32)}`, id = `parcel_${'b'.repeat(32)}`
const parcel: Parcel = { id, workspace_id: 'w1', farm_id: farm, name: 'Parcel', external_code: 'P1', status: 'active', data_class: 'demo', geometry_revision: 1, current_geometry_revision: 1, geometry: { type: 'Polygon', coordinates: [[[110, 25], [110.01, 25], [110.01, 25.01], [110, 25]]] }, source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude', area_m2: '100', area_ha: '0.01', area_mu: '0.15', geometry_hash: 'a'.repeat(64), valid_from: '2026-11-01T00:00:00Z', valid_to: null, created_at: '2026-11-01T00:00:00Z', reason: 'Original', actor_user_id: 'actor1', provenance: {}, affected_dependencies: { availability: 'not_yet_integrated', count: null }, allowed_actions: ['agri.parcel.read', 'agri.parcel.update'] }
const context: ParcelContext = { workspace_id: 'w1', farm_id: farm, expected_farm_revision: 5, allowed_actions: ['agri.parcel.create', 'agri.parcel.read', 'agri.parcel.update'] }
const intent: Intent = { action: 'revise', parcel_id: id, expected_revision: 1, geometry: parcel.geometry, source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude', reason: 'Boundary correction' }
const preview: Preview = { preview_id: 'pop_1', expires_at: '2099-01-01T00:00:00Z', request_hash: 'b'.repeat(64), action: 'revise', farm_id: farm, parcel_id: id, expected_revision: 1, summary: { geometry: parcel.geometry, reason: intent.reason } }
const operation: Operation = { operation_id: 'pop_1', action: 'revise', state: 'pending', farm_id: farm, parcel_id: id, geometry_revision: 1, error_code: '', job_id: null, events_url: null, status_url: '/api/agri/v1/parcel-operations/pop_1?workspace_id=w1' }
const api = () => ({ context: async (): Promise<ParcelContext> => context, list: async (): Promise<ParcelPage> => ({ items: [parcel], next_cursor: '', has_more: false }), detail: async (): Promise<Parcel> => parcel, history: async (): Promise<HistoryPage> => ({ items: [parcel], next_cursor: '', has_more: false }), preview: async () => preview, submit: async (): Promise<Operation> => operation, operation: async (): Promise<Operation> => operation })
test('parcel create-only context never silently reads content or grants creator access', async () => {
  const a = api(); let reads = 0
  a.context = async () => ({ ...context, allowed_actions: ['agri.parcel.create'] }); a.list = async () => { reads++; throw Error('must not read') }
  const c = new ParcelController('w1', farm, a); await c.initialize(); await c.list()
  expect(reads).toBe(0); expect(c.state.context?.allowed_actions).toEqual(['agri.parcel.create']); expect(c.state.page.items).toEqual([]); c.dispose()
})
test('parcel conflict preserves original draft intent/current geometry until explicit comparison adoption', async () => {
  const a = api(), c = new ParcelController('w1', farm, a); await c.initialize(); await c.select(id); await c.prepare(intent)
  a.submit = async () => { throw new FarmError(409, 'REVISION_CONFLICT', 'conflict') }; await c.confirm()
  expect(c.state.conflict).toBe(true); expect(c.state.intent).toEqual(intent); expect(c.state.selected?.geometry_revision).toBe(1)
  a.detail = async () => ({ ...parcel, geometry_revision: 2, current_geometry_revision: 2 }); await c.compare()
  expect(c.state.selected?.geometry_revision).toBe(1); expect(c.state.comparison?.geometry_revision).toBe(2)
  const epoch = c.state.editorEpoch; c.adoptComparison(); expect(c.state.selected?.geometry_revision).toBe(2); expect(c.state.editorEpoch).toBe(epoch + 1); expect(c.state.preview).toBeNull(); c.dispose()
})
test('historical selection and stale mismatched expected version cannot prepare a revision', async () => {
  const a = api(); let calls = 0; a.preview = async () => { calls++; return preview }
  a.detail = async () => ({ ...parcel, current_geometry_revision: 2, allowed_actions: ['agri.parcel.read'] })
  const c = new ParcelController('w1', farm, a); await c.select(id, { revision: 1 }); await c.prepare(intent); expect(calls).toBe(0)
  a.detail = async () => ({ ...parcel, geometry_revision: 2, current_geometry_revision: 2 }); await c.select(id); await c.prepare(intent); expect(calls).toBe(0); c.dispose()
})
test('parcel unknown outcome and preview polling keep original immutable body and idempotency key', async () => {
  const a = api(); const keys: unknown[] = []; const intents: unknown[] = []; let n = 0
  a.submit = async (_w?: unknown, _f?: unknown, body?: unknown, _p?: unknown, key?: unknown) => { intents.push(body); keys.push(key); if (++n === 1) throw TypeError('unknown'); return operation }
  a.operation = async () => ({ ...operation, state: 'preview' })
  const c = new ParcelController('w1', farm, a, undefined, () => 'same_key'); await c.select(id); const draft = structuredClone(intent); await c.prepare(draft); draft.reason = 'external edit'; await c.confirm(); await c.poll()
  expect(c.unresolved).toBe(true); c.draftChanged(); expect(c.state.intent).toEqual(intent); await c.confirm()
  expect(keys).toEqual(['same_key', 'same_key']); expect(intents).toEqual([intent, intent]); c.dispose()
})
test('double confirmation and routine authority checking do not issue duplicate parcel writes', async () => {
  const a = api(); let calls = 0; let resolve!: (value: Operation) => void
  a.submit = async () => { calls++; return new Promise(done => { resolve = done }) }
  const c = new ParcelController('w1', farm, a); await c.select(id); await c.prepare(intent); c.setChecking(true); await c.confirm(); expect(calls).toBe(0); expect(c.state.preview).toEqual(preview)
  c.setChecking(false); const pending = c.confirm(); await c.confirm(); expect(calls).toBe(1); resolve({ ...operation, state: 'completed', geometry_revision: 2 }); await pending; await c.confirm(); expect(calls).toBe(1); c.dispose()
})
test('revocation clears list/detail/history/preview while late disposed responses cannot restore content', async () => {
  for (const status of [401, 403, 404]) {
    const a = api(); let rechecks = 0; const c = new ParcelController('w1', farm, a, () => { rechecks++ }); await c.initialize(); await c.select(id); await c.history()
    a.preview = async () => { throw new FarmError(status, 'DENIED', 'denied') }; await c.prepare(intent)
    expect(c.state.page.items).toEqual([]); expect(c.state.history.items).toEqual([]); expect(c.state.selected).toBeNull(); expect(c.state.context).toBeNull(); expect(rechecks).toBe(status === 404 ? 0 : 1); c.dispose()
  }
  const a = api(); let resolve!: (value: ParcelContext) => void; let reads = 0; a.context = async () => new Promise(done => { resolve = done }); a.list = async () => { reads++; return { items: [parcel], next_cursor: '', has_more: false } }
  const c = new ParcelController('w1', farm, a); const pending = c.initialize(); c.dispose(); resolve(context); await pending; expect(c.state.context).toBeNull(); expect(c.state.page.items).toEqual([]); expect(reads).toBe(0)
})
test('reconciliation and lookup never imply completion or synthesize draft authority', async () => {
  const a = api(); a.operation = async () => ({ ...operation, state: 'reconciliation_required' })
  const c = new ParcelController('w1', farm, a); await c.lookup('pop_1'); expect(c.unresolved).toBe(true); expect(c.state.intent).toBeNull(); expect(c.state.selected).toBeNull(); expect(c.state.operation?.status_url).toBe(operation.status_url); c.dispose()
})
test('create preview requires current explicit capability and exact parent revision; expired preview cannot write', async () => {
  const a = api(); let previews = 0, writes = 0
  a.preview = async () => { previews++; return { ...preview, expires_at: '2000-01-01T00:00:00Z' } }; a.submit = async () => { writes++; return operation }
  const c = new ParcelController('w1', farm, a)
  const create: Intent = { action: 'create', farm_id: farm, expected_revision: 0, expected_farm_revision: 5, parcel: { name: 'P', external_code: 'P', geometry: parcel.geometry, source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude' }, reason: 'Creation' }
  await c.prepare(create); expect(previews).toBe(0); await c.initialize(); await c.prepare({ ...create, expected_farm_revision: 4 }); expect(previews).toBe(0)
  await c.prepare(create); expect(previews).toBe(1); await c.confirm(); expect(writes).toBe(0); expect(c.state.error?.code).toBe('PREVIEW_EXPIRED'); c.dispose()
})
test('create parent conflict refreshes only trusted context without remounting draft or retrying; unknown writes block it', async () => {
  const a = api(); let previews = 0, writes = 0, contexts = 0
  a.context = async () => { contexts++; return { ...context, expected_farm_revision: contexts === 1 ? 5 : 6 } }
  a.preview = async () => { previews++; throw new FarmError(409, 'REVISION_CONFLICT', 'parent revision conflict') }
  a.submit = async () => { writes++; throw TypeError('unknown outcome') }
  const c = new ParcelController('w1', farm, a); await c.initialize(); c.create(); const epoch = c.state.editorEpoch
  const create: Intent = { action: 'create', farm_id: farm, expected_revision: 0, expected_farm_revision: 5, parcel: { name: 'Draft retained', external_code: 'P', geometry: parcel.geometry, source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude' }, reason: 'Creation' }
  await c.prepare(create); expect(c.state.conflict).toBe(true); await c.refreshContext()
  expect(c.state.editorEpoch).toBe(epoch); expect(c.state.context?.expected_farm_revision).toBe(6); expect(c.state.conflict).toBe(false); expect(previews).toBe(1); expect(writes).toBe(0); expect(create.parcel.name).toBe('Draft retained')
  a.preview = async () => ({ ...preview, action: 'create', parcel_id: '', expected_revision: 0 })
  await c.prepare({ ...create, expected_farm_revision: 6 }); await c.confirm(); expect(c.unresolved).toBe(true)
  const count = contexts; await c.refreshContext(); expect(contexts).toBe(count); expect(c.state.preview?.preview_id).toBe('pop_1'); expect(writes).toBe(1); c.dispose()
})
