import { expect, test } from 'bun:test'
import { contextKey, selectedContext } from './directory'
import type { ParcelContext } from './types'
const current: ParcelContext = { workspace_id: 'w1', farm_id: `farm_${'a'.repeat(32)}`, expected_farm_revision: 5, allowed_actions: ['agri.parcel.read', 'agri.parcel.create'] }
test('directory refresh never interprets a first-page omission as revoked selected context', async () => {
  let calls = 0; const controller = new AbortController()
  const api = { context: async (workspace: string, farm: string, signal: AbortSignal) => { calls++; expect(workspace).toBe('w1'); expect(farm).toBe(current.farm_id); expect(signal).toBe(controller.signal); return current } }
  expect(await selectedContext(api, 'w1', current, [], false, controller.signal)).toEqual(current); expect(calls).toBe(1)
  await selectedContext(api, 'w1', current, [], true, controller.signal); expect(calls).toBe(1)
  await selectedContext(api, 'w1', current, [current], false, controller.signal); expect(calls).toBe(1)
})
test('directory key preserves drafts on parent revision advancement but remounts on real scope/capability changes', () => {
  expect(contextKey({ ...current, expected_farm_revision: 6, allowed_actions: [...current.allowed_actions].reverse() })).toBe(contextKey(current))
  expect(contextKey({ ...current, allowed_actions: ['agri.parcel.read'] })).not.toBe(contextKey(current))
  expect(contextKey({ ...current, workspace_id: 'other' })).not.toBe(contextKey(current))
})
test('directory late selected-context result and actual authorization denial are never treated as valid selection', async () => {
  const controller = new AbortController(); let release!: (value: ParcelContext) => void
  const pending = selectedContext({ context: async () => new Promise(done => { release = done }) }, 'w1', current, [], false, controller.signal)
  controller.abort(); release(current); await expect(pending).rejects.toThrow()
  await expect(selectedContext({ context: async () => { throw Error('denied') } }, 'w1', current, [], false, new AbortController().signal)).rejects.toThrow('denied')
})
