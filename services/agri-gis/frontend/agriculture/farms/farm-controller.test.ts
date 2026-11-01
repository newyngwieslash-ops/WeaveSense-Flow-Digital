import { describe, expect, test } from 'bun:test'
import { FarmController } from './farm-controller'
import { FarmError } from './transport'
import type { Farm, FarmContext, FarmPage, Intent, Operation, Preview } from './types'
const id = `farm_${'a'.repeat(32)}`
const context: FarmContext = { workspace_id: 'w1', can_create: true, members: [], regions: [{ code: 'r1', name: 'Region', timezones: ['Asia/Shanghai'] }], dictionary_version: 'v1', area_units: ['m2', 'ha', 'mu'] }
const farm: Farm = { id, workspace_id: 'w1', external_code: 'F001', name: 'Original', operator_name: 'Cooperative', region_code: 'r1', dictionary_version: 'v1', timezone: 'Asia/Shanghai', area_unit: 'ha', geometry: { type: 'Polygon', coordinates: [[[112, 25], [112.01, 25], [112.01, 25.01], [112, 25]]] }, source_crs: 'EPSG:4326', coordinate_order: 'longitude_latitude', data_class: 'demo', status: 'active', revision: 1, area_m2: '100', area_ha: '0.01', area_mu: '0.15', created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z', allowed_actions: ['agri.farm.read', 'agri.farm.update'] }
const intent: Intent = { action: 'archive', farm_id: id, expected_revision: 1, reason: 'Explicit reason' }
const preview: Preview = { preview_id: 'fop_1', expires_at: '2099-01-01T00:00:00Z', request_hash: 'a'.repeat(64), action: 'archive', farm_id: id, expected_revision: 1, summary: { farm_id: id, reason: intent.reason } }
const operation: Operation = { operation_id: 'fop_1', action: 'archive', state: 'pending', farm_id: id, revision: 1, error_code: '', allowed_actions: [], job_id: null, events_url: null, status_url: '/api/agri/v1/farm-operations/fop_1?workspace_id=w1' }
const api = () => ({ context: async () => context, list: async (): Promise<FarmPage> => ({ items: [farm], next_cursor: '', has_more: false }), detail: async () => farm, preview: async () => preview, submit: async () => operation, operation: async () => operation })
describe('farm controller component (not live service acceptance)', () => {
  test('known pending and unknown network outcomes retry identical key/body, never claim farm creation', async () => {
    const a = api(); const keys: string[] = []; const bodies: Intent[] = []; let attempts = 0
    a.submit = async (_w?: unknown, submitted?: unknown, _p?: unknown, key?: unknown) => { keys.push(key as string); bodies.push(submitted as Intent); if (++attempts === 1) throw new TypeError('network'); return operation }
    const c = new FarmController('w1', a, undefined, () => 'same_key')
    await c.initialize(); await c.prepare(intent); await c.confirm()
    expect(c.state.uncertain).toBe(true); expect(c.state.operation).toBeNull(); expect(c.state.selected).toBeNull()
    c.draftChanged(); expect(c.state.preview?.preview_id).toBe('fop_1')
    await c.confirm(); expect(c.state.operation?.state).toBe('pending'); expect(c.state.uncertain).toBe(false)
    expect(keys).toEqual(['same_key', 'same_key']); expect(bodies).toEqual([intent, intent]); c.dispose()
  })
  test('double confirmation is not concurrent and late disposed response never restores protected state', async () => {
    const a = api(); let resolve!: (value: Operation) => void; let calls = 0; let signal!: AbortSignal
    a.submit = async (_w?: unknown, _i?: unknown, _p?: unknown, _k?: unknown, s?: unknown) => { calls++; signal = s as AbortSignal; return new Promise(done => { resolve = done }) }
    const c = new FarmController('w1', a); await c.prepare(intent)
    const pending = c.confirm(); await c.confirm(); expect(calls).toBe(1)
    c.dispose(); expect(signal.aborted).toBe(true); resolve({ ...operation, state: 'completed' }); await pending
    expect(c.state.preview).toBeNull(); expect(c.state.operation).toBeNull(); expect(c.state.context).toBeNull()
  })
  test('409 preserves selected version and intent; compare never overwrites draft until explicit adopt', async () => {
    const a = api(); const c = new FarmController('w1', a)
    await c.select(id); await c.prepare(intent)
    a.submit = async () => { throw new FarmError(409, 'REVISION_CONFLICT', 'Conflict') }
    await c.confirm(); expect(c.state.conflict).toBe(true); expect(c.state.intent).toEqual(intent); expect(c.state.selected?.revision).toBe(1)
    a.detail = async () => ({ ...farm, revision: 2, name: 'Current' }); await c.compare()
    expect(c.state.comparison?.revision).toBe(2); expect(c.state.selected?.revision).toBe(1); const epoch = c.state.editorEpoch
    c.adoptComparison(); expect(c.state.selected?.revision).toBe(2); expect(c.state.editorEpoch).toBe(epoch + 1); expect(c.state.preview).toBeNull(); c.dispose()
  })
  test('authority failure clears protected projections and requests shared session revalidation', async () => {
    for (const status of [401, 403, 404]) {
      const a = api(); let rechecks = 0; const c = new FarmController('w1', a, () => { rechecks++ })
      await c.initialize(); await c.select(id); a.preview = async () => { throw new FarmError(status, 'DENIED', 'Denied') }; await c.prepare(intent)
      expect(c.state.context).toBeNull(); expect(c.state.selected).toBeNull(); expect(c.state.page.items).toEqual([]); expect(rechecks).toBe(status === 404 ? 0 : 1); c.dispose()
    }
  })
  test('routine session checks preserve draft preview but block writes', async () => {
    const a = api(); let submitted = false; a.submit = async () => { submitted = true; return operation }
    const c = new FarmController('w1', a); await c.prepare(intent); c.setChecking(true); await c.confirm()
    expect(submitted).toBe(false); expect(c.state.preview).toEqual(preview)
    c.setChecking(false); await c.confirm(); expect(submitted).toBe(true); c.dispose()
  })
  test('explicit draft edit invalidates preview and creates fresh confirmation rather than changing frozen payload', async () => {
    const c = new FarmController('w1', api()); const draft = structuredClone(intent); await c.prepare(draft); draft.reason = 'Changed outside'
    expect((c.state.intent as typeof intent).reason).toBe('Explicit reason'); c.draftChanged(); expect(c.state.intent).toBeNull(); expect(c.state.preview).toBeNull(); c.dispose()
  })
  test('expired unsubmitted preview cannot write; completed operation cannot submit twice', async () => {
    const a = api(); let calls = 0; a.preview = async () => ({ ...preview, expires_at: '2000-01-01T00:00:00Z' }); a.submit = async () => { calls++; return { ...operation, state: 'completed' } }
    const c = new FarmController('w1', a); await c.prepare(intent); await c.confirm(); expect(c.state.error?.code).toBe('PREVIEW_EXPIRED'); expect(calls).toBe(0)
    a.preview = async () => preview; await c.prepare(intent); await c.confirm(); await c.confirm(); expect(calls).toBe(1); c.dispose()
  })
  test('late context initialization after authority unmount does not repopulate state', async () => {
    const a = api(); let resolve!: (v: FarmContext) => void; a.context = async () => new Promise(done => { resolve = done })
    const c = new FarmController('w1', a); const pending = c.initialize(); c.dispose(); resolve(context); await pending
    expect(c.state.context).toBeNull(); expect(c.state.page.items).toEqual([])
  })
  test('polling preview after unknown submission preserves original intent until definitive outcome', async () => {
    const a = api(); a.submit = async () => { throw new TypeError('unknown') }; a.operation = async () => ({ ...operation, state: 'preview' })
    const c = new FarmController('w1', a); await c.prepare(intent); await c.confirm(); await c.poll()
    expect(c.unresolved).toBe(true); c.draftChanged(); expect(c.state.intent).toEqual(intent); c.dispose()
  })
  test('operation recovery queries server identity and does not fabricate retry payload or content grants', async () => {
    const c = new FarmController('w1', api()); await c.lookup('fop_1')
    expect(c.state.operation).toEqual(operation); expect(c.state.selected).toBeNull(); expect(c.state.preview).toBeNull(); expect(c.state.intent).toBeNull(); expect(c.unresolved).toBe(true); c.dispose()
  })
  test('reconciliation state keeps operation query reference and prevents new drafts or implicit completion', async () => {
    const a = api(); a.submit = async () => ({ ...operation, state: 'reconciliation_required' })
    const c = new FarmController('w1', a); await c.prepare(intent); await c.confirm()
    expect(c.state.operation?.state).toBe('reconciliation_required'); expect(c.unresolved).toBe(true)
    expect(c.state.operation?.status_url).toBe(operation.status_url); expect(c.state.operation?.job_id).toBeNull(); expect(c.state.operation?.events_url).toBeNull()
    c.draftChanged(); expect(c.state.intent).toEqual(intent); expect(c.state.preview).toEqual(preview); expect(c.state.selected).toBeNull(); c.dispose()
  })
})
