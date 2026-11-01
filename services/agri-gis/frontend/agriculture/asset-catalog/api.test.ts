import { expect, test } from 'bun:test'
import { AssetAPI } from './api'
import { assetFrom, contextFrom, previewFrom, rightsFrom } from './types'
import { asset, context, intent, operation, preview } from './tests/fixtures'
const reply = (data: unknown, status = 200) => ({ status, envelope: { data, meta: { request_id: 'r1', schema_version: '1.0' as const } } })
test('asset metadata registration uses canonical same-origin request with explicit zero version and original grants/key', async () => {
  const signal = new AbortController().signal
  const api = new AssetAPI({ request: async (path, actualSignal, method, body, key) => { expect(path).toBe('/api/agri/v1/assets'); expect(actualSignal).toBe(signal); expect(method).toBe('POST'); expect(body).toEqual({ workspace_id: 'w1', preview_id: 'aop_1', expected_revision: 0, metadata: intent.metadata, initial_grants: intent.initial_grants }); expect(key).toBe('same_key'); return reply(operation, 201) } })
  expect((await api.submit('w1', intent, preview, 'same_key', signal)).state).toBe('completed')
  expect(previewFrom(preview, intent).summary.entity).toBeNull(); expect(previewFrom(preview, intent).summary.status).toBe('validating')
})
test('asset preview rejects fabricated ready bytes, altered grant plans and modified metadata', () => {
  for (const changes of [{ summary: { ...preview.summary, status: 'ready' } }, { summary: { ...preview.summary, entity: { object_id: 'client_claim' } } }, { initial_grants: [] }, { summary: { ...preview.summary, metadata: { ...asset.metadata, name: 'Different' } } }]) expect(() => previewFrom({ ...preview, ...changes }, intent)).toThrow()
})
test('grant preview compares exact instants despite server RFC3339 canonicalization, never changes expiry', () => {
  const original = { ...intent, initial_grants: intent.initial_grants.map(g => ({ ...g, expires_at: '2027-01-01T08:00:00.000000+08:00' })) }
  const same = { ...preview, initial_grants: preview.initial_grants.map(g => ({ ...g, expires_at: '2027-01-01T00:00:00Z' })) }
  expect(previewFrom(same, original).initial_grants[0]?.expires_at).toBe('2027-01-01T00:00:00Z')
  expect(() => previewFrom({ ...same, initial_grants: same.initial_grants.map(g => ({ ...g, expires_at: '2027-01-01T00:00:00.000001Z' })) }, original)).toThrow()
})
test('current rights summaries discard registry internals; wrong scopes, historical write and ready-without-entity rejected', () => {
  expect(rightsFrom({ ...asset.rights, reviewed_sources: [{ endpoint: 'not-public' }], private_key: 'never-copy' })).toEqual(asset.rights)
  expect(contextFrom(context, 'w1')).toEqual(context)
  for (const changes of [{ workspace_id: 'other' }, { farm_id: `farm_${'f'.repeat(32)}` }, { revision: 2 }, { status: 'ready' }, { entity: { object_id: 'obj_1', object_version: 1, storage_key: 'secret' } }]) expect(() => assetFrom({ ...asset, ...changes }, 'w1')).toThrow()
  expect(assetFrom({ ...asset, revision: 2, allowed_actions: ['agri.asset.read'] }, 'w1').version).toBe(1)
})
test('asset list is canonical paginated full DTO and version reads bind exact version/ID/current permission', async () => {
  const api = new AssetAPI({ request: async path => { expect(path).toContain('kind=raster'); expect(path).toContain('data_class=demo'); return { ...reply([asset]), envelope: { ...reply([asset]).envelope, next_cursor: '', has_more: false } } } })
  expect((await api.list('w1', new AbortController().signal, 'raster', 'demo')).items[0]?.asset_id).toBe(asset.asset_id)
  await expect(new AssetAPI({ request: async () => reply(asset) }).detail('w1', asset.asset_id, 2, new AbortController().signal)).rejects.toThrow()
  const calls: string[] = []; const current = new AssetAPI({ request: async path => { calls.push(path); return reply(path.includes('/versions/1?') ? { ...asset, revision: 2, allowed_actions: ['agri.asset.read'] } : { ...asset, revision: 2, version: 2 }) } })
  expect((await current.current('w1', asset.asset_id, 1, new AbortController().signal)).version).toBe(2); expect(calls).toHaveLength(2)
})
test('asset operation state/status/null references never turn unresolved receipts into completed', async () => {
  expect((await new AssetAPI({ request: async () => reply({ ...operation, state: 'reconciliation_required' }, 202) }).submit('w1', intent, preview, 'key', new AbortController().signal)).state).toBe('reconciliation_required')
  for (const [value, status] of [[operation, 200], [operation, 202], [{ ...operation, state: 'pending' }, 201], [{ ...operation, state: 'preview' }, 202], [{ ...operation, job_id: 'fake' }, 201], [{ ...operation, status_url: 'https://external.invalid' }, 201]] as const) await expect(new AssetAPI({ request: async () => reply(value, status) }).submit('w1', intent, preview, 'key', new AbortController().signal)).rejects.toThrow()
})
