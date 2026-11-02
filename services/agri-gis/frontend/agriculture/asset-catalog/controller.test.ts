import { expect, test } from 'bun:test'
import { AssetController } from './controller'
import { FarmError } from '../farms/transport'
import { asset, context, intent, operation, preview } from './tests/fixtures'
import type { Asset, Context, Intent, Operation, Preview } from './types'
const api = () => ({ context: async (): Promise<Context> => context, list: async () => ({ items: [asset], next_cursor: '', has_more: false }), detail: async (): Promise<Asset> => asset, current: async (): Promise<Asset> => asset, preview: async (): Promise<Preview> => preview, submit: async (): Promise<Operation> => operation, operation: async (): Promise<Operation> => operation })
test('asset create requires current explicit governance/farm scope and eligible grant ceiling', async () => {
  const a = api(); let calls = 0; a.preview = async () => { calls++; return preview }
  const c = new AssetController('w1', a); await c.prepare(intent); expect(calls).toBe(0); await c.initialize()
  await c.prepare({ ...intent, initial_grants: [] }); expect(calls).toBe(0)
  await c.prepare({ ...intent, initial_grants: [{ subject_user_id: 'foreign', capabilities: ['agri.asset.read'] }] }); expect(calls).toBe(0)
  await c.prepare({ ...intent, metadata: { ...intent.metadata, farm_id: `farm_${'f'.repeat(32)}` } }); expect(calls).toBe(0)
  await c.prepare(intent); expect(calls).toBe(1); c.dispose()
})
test('asset unknown outcomes preserve original metadata/grants/key and block replacement after preview polling', async () => {
  const a = api(); let writes = 0; const keys: unknown[] = []; const bodies: unknown[] = []
  a.submit = async (_w?: unknown, body?: unknown, _p?: unknown, key?: unknown) => { writes++; keys.push(key); bodies.push(body); if (writes === 1) throw TypeError('unknown'); return operation }; a.operation = async () => ({ ...operation, state: 'preview' })
  const c = new AssetController('w1', a, undefined, () => 'same_key'); await c.initialize(); const draft = structuredClone(intent); await c.prepare(draft); draft.metadata.name = 'later unrelated change'; await c.confirm(); await c.poll()
  expect(c.unresolved).toBe(true); c.draftChanged(); expect(c.state.intent).toEqual(intent); c.create(); expect(c.state.preview?.preview_id).toBe('aop_1'); await c.confirm()
  expect(keys).toEqual(['same_key', 'same_key']); expect(bodies).toEqual([intent, intent]); c.dispose()
})
test('asset CAS conflict retains version draft until explicit current comparison/adoption', async () => {
  const a = api(); const update: Intent = { action: 'version', asset_id: asset.asset_id, expected_revision: 1, metadata: { ...asset.metadata, name: 'new version draft' } }
  a.preview = async () => ({ ...preview, action: 'version', asset_id: asset.asset_id, expected_revision: 1, initial_grants: [], summary: { ...preview.summary, metadata: update.metadata } }); a.submit = async () => { throw new FarmError(409, 'REVISION_CONFLICT', 'conflict') }
  const c = new AssetController('w1', a); await c.initialize(); await c.select(asset.asset_id, 1); await c.prepare(update); await c.confirm()
  expect(c.state.conflict).toBe(true); expect(c.state.intent).toEqual(update); expect(c.state.selected?.version).toBe(1)
  a.current = async () => ({ ...asset, version: 2, revision: 2 }); await c.compare(); expect(c.state.selected?.version).toBe(1)
  const epoch = c.state.editorEpoch; c.adoptComparison(); expect(c.state.editorEpoch).toBe(epoch + 1); expect(c.state.selected?.version).toBe(2); expect(c.state.preview).toBeNull(); c.dispose()
})
test('historical asset version cannot create successor and routine checking blocks new writes without discarding draft', async () => {
  const a = api(); let calls = 0; a.preview = async () => { calls++; return preview }; a.detail = async () => ({ ...asset, revision: 2, allowed_actions: ['agri.asset.read'] })
  const c = new AssetController('w1', a); await c.initialize(); await c.select(asset.asset_id, 1); await c.prepare({ action: 'version', asset_id: asset.asset_id, expected_revision: 2, metadata: asset.metadata }); expect(calls).toBe(0)
  c.create(); await c.prepare(intent); c.setChecking(true); await c.confirm(); expect(c.state.operation).toBeNull(); expect(c.state.intent).toEqual(intent)
  c.setChecking(false); await c.confirm(); expect(c.state.operation?.state).toBe('completed'); c.dispose()
})
test('asset revocation clears every protected projection; disposal suppresses late HTTP results', async () => {
  for (const status of [401, 403, 404]) {
    const a = api(); let rechecks = 0; const c = new AssetController('w1', a, () => { rechecks++ }); await c.initialize(); await c.select(asset.asset_id, 1); a.preview = async () => { throw new FarmError(status, 'DENIED', 'denied') }; await c.prepare(intent)
    expect(c.state.context).toBeNull(); expect(c.state.selected).toBeNull(); expect(c.state.page.items).toEqual([]); expect(rechecks).toBe(status === 404 ? 0 : 1); c.dispose()
  }
  const a = api(); let done!: (v: Context) => void; a.context = async () => new Promise(resolve => { done = resolve }); const c = new AssetController('w1', a); const pending = c.initialize(); c.dispose(); done(context); await pending; expect(c.state.context).toBeNull(); expect(c.state.page.items).toEqual([])
})
test('asset double submit, expired preview and reconciliation recovery never imply entity readiness', async () => {
  const a = api(); let calls = 0; let finish!: (value: Operation) => void; a.submit = async () => { calls++; return new Promise(resolve => { finish = resolve }) }
  const c = new AssetController('w1', a); await c.initialize(); await c.prepare(intent); const pending = c.confirm(); await c.confirm(); expect(calls).toBe(1); finish(operation); await pending; await c.confirm(); expect(calls).toBe(1); expect(c.state.selected).toBeNull(); c.dispose()
  const b = api(); b.preview = async () => ({ ...preview, expires_at: '2000-01-01T00:00:00Z' }); const expired = new AssetController('w1', b); await expired.initialize(); await expired.prepare(intent); await expired.confirm(); expect(expired.state.error?.code).toBe('PREVIEW_EXPIRED'); expect(expired.state.operation).toBeNull(); expired.dispose()
  const d = api(); d.operation = async () => ({ ...operation, state: 'reconciliation_required' }); const recovered = new AssetController('w1', d); await recovered.lookup('aop_1'); expect(recovered.unresolved).toBe(true); expect(recovered.state.intent).toBeNull(); expect(recovered.state.selected).toBeNull(); recovered.dispose()
})
test('post-completion content denial clears all metadata and retains only a freshly reauthorized owner receipt', async () => {
  for (const status of [403, 404]) {
    const a = api(); let rechecks = 0, receipts = 0; a.operation = async () => { receipts++; return operation }
    const c = new AssetController('w1', a, () => { rechecks++ }); await c.initialize(); await c.prepare(intent); await c.confirm(); a.list = async () => { throw new FarmError(status, 'AUTHORITY_UNAVAILABLE', 'denied') }; await c.list()
    expect(receipts).toBe(1); expect(rechecks).toBe(0); expect(c.state.context).toBeNull(); expect(c.state.selected).toBeNull(); expect(c.state.page.items).toEqual([]); expect(c.state.intent).toBeNull(); expect(c.state.preview).toBeNull(); expect(c.state.operation).toBeNull()
    expect(c.state.safeReceipt).toEqual({ operation_id: operation.operation_id, state: 'completed', status_url: operation.status_url }); c.dispose()
  }
})
test('401 never tries receipt fallback; failed owner check and late receipt cannot restore old scope', async () => {
  const a = api(); let receipts = 0, rechecks = 0; a.operation = async () => { receipts++; return operation }
  const c = new AssetController('w1', a, () => { rechecks++ }); await c.initialize(); await c.prepare(intent); await c.confirm(); a.list = async () => { throw new FarmError(401, 'AUTHORITY_UNAVAILABLE', 'denied') }; await c.list(); expect(receipts).toBe(0); expect(rechecks).toBe(1); expect(c.state.safeReceipt).toBeNull(); c.dispose()
  const b = api(); let failures = 0; b.operation = async () => { throw new FarmError(403, 'DENIED', 'owner/membership denied') }; const failed = new AssetController('w1', b, () => { failures++ }); await failed.initialize(); await failed.prepare(intent); await failed.confirm(); b.list = async () => { throw new FarmError(403, 'DENIED', 'content denied') }; await failed.list(); expect(failures).toBe(1); expect(failed.state.safeReceipt).toBeNull(); failed.dispose()
  const d = api(); let release!: (value: Operation) => void; let signal!: AbortSignal; let started!: () => void; const entered = new Promise<void>(resolve => { started = resolve }); d.operation = async (_w?: unknown, _id?: unknown, s?: unknown) => { signal = s as AbortSignal; return new Promise(resolve => { release = resolve; started() }) }
  const stale = new AssetController('w1', d); await stale.initialize(); await stale.prepare(intent); await stale.confirm(); d.list = async () => { throw new FarmError(403, 'DENIED', 'content denied') }; const pending = stale.list(); await entered
  stale.dispose(); expect(signal.aborted).toBe(true); release(operation); await pending; expect(stale.state.safeReceipt).toBeNull(); expect(stale.state.context).toBeNull(); expect(stale.state.page.items).toEqual([])
})
