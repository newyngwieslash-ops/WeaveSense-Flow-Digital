import { describe, expect, test } from 'bun:test'
import { FarmAPI } from './api'
import { FarmTransport } from './transport'
import { contextFrom, operationFrom, pageFrom, previewFrom, type Intent, type Preview } from './types'
const reply = (data: unknown, status = 200) => Response.json({ data, meta: { request_id: 'req_1', schema_version: '1.0' } }, { status })
const id = `farm_${'b'.repeat(32)}`
const intent: Intent = { action: 'archive', farm_id: id, expected_revision: 2, reason: 'Reviewed reason' }
const preview: Preview = { preview_id: 'fop_test', action: 'archive', farm_id: id, expected_revision: 2, expires_at: '2099-01-01T00:00:00Z', request_hash: 'b'.repeat(64), summary: { farm_id: id, reason: intent.reason } }
const operation = { operation_id: 'fop_test', action: 'archive', state: 'completed', farm_id: id, revision: 3, error_code: '', allowed_actions: [], job_id: null, events_url: null, status_url: '/api/agri/v1/farm-operations/fop_test?workspace_id=w1' }
describe('farm wire contract component, not real service acceptance', () => {
  test('archive request preserves explicit preview/revision/reason and no client identity fields', async () => {
    const api = new FarmAPI(new FarmTransport(async (path, init) => {
      if (path === '/api/auth/csrf') return Response.json({ code: 0, data: { token: 'b'.repeat(64) } })
      expect(path).toBe(`/api/agri/v1/farms/${id}/archive`)
      expect(JSON.parse(String(init.body))).toEqual({ workspace_id: 'w1', preview_id: 'fop_test', expected_revision: 2, reason: 'Reviewed reason' })
      expect(new Headers(init.headers).get('Idempotency-Key')).toBe('same_key')
      return reply(operation)
    }))
    expect((await api.submit('w1', intent, preview, 'same_key', new AbortController().signal)).state).toBe('completed')
  })
  test('rejects mismatched operation and 202 pretending completed', async () => {
    for (const [data, status] of [[{ ...operation, operation_id: 'wrong_id' }, 200], [operation, 202], [{ ...operation, state: 'pending' }, 200], [{ ...operation, state: 'reconciliation_required' }, 200]] as const) {
      const api = new FarmAPI(new FarmTransport(async path => path === '/api/auth/csrf' ? Response.json({ code: 0, data: { token: 'b'.repeat(64) } }) : reply(data, status)))
      await expect(api.submit('w1', intent, preview, 'same_key', new AbortController().signal)).rejects.toThrow()
    }
  })
  test('202 reconciliation retains real operation reference and null job/events without claiming completion', async () => {
    const unresolved = { ...operation, state: 'reconciliation_required', error_code: 'UNKNOWN_RESULT' }
    const api = new FarmAPI(new FarmTransport(async path => path === '/api/auth/csrf' ? Response.json({ code: 0, data: { token: 'b'.repeat(64) } }) : reply(unresolved, 202)))
    const result = await api.submit('w1', intent, preview, 'same_key', new AbortController().signal)
    expect(result).toEqual(unresolved)
    expect(result.state).not.toBe('completed')
    expect(result.status_url).toBe('/api/agri/v1/farm-operations/fop_test?workspace_id=w1')
    expect(result.job_id).toBeNull()
    expect(result.events_url).toBeNull()
  })
  test('context uses explicit region/timezone/member capabilities and trusted workspace selector', () => {
    const context = { workspace_id: 'w1', can_create: true, members: [{ user_id: 'u1', display_name: 'Member', roles: ['farm_manager'], allowed_capabilities: ['agri.farm.read'] }], regions: [{ code: 'r1', name: 'Region', timezones: ['Asia/Shanghai'] }], dictionary_version: 'v1', area_units: ['m2'] }
    expect(contextFrom(context, 'w1').members[0]?.allowed_capabilities).toEqual(['agri.farm.read'])
    expect(() => contextFrom(context, 'w2')).toThrow()
    expect(() => contextFrom({ ...context, regions: [{ code: 'r1', name: 'Region', timezones: ['Guess/Invalid'] }] }, 'w1')).toThrow()
    expect(() => contextFrom({ ...context, members: [{ ...context.members[0], allowed_capabilities: ['system.admin'] }] }, 'w1')).toThrow()
  })
  test('preview receipt must match exact object revision/action; empty list is legitimate', () => {
    expect(previewFrom(preview, intent)).toEqual(preview)
    expect(() => previewFrom({ ...preview, expected_revision: 3 }, intent)).toThrow()
    expect(() => previewFrom({ ...preview, action: 'create' }, intent)).toThrow()
    expect(pageFrom({ items: [], next_cursor: '', has_more: false }, 'w1').items).toEqual([])
    expect(() => pageFrom({ items: [], next_cursor: '', has_more: true }, 'w1')).toThrow()
    expect(() => operationFrom({ ...operation, allowed_actions: ['system.admin'] }, 'w1')).toThrow()
    expect(() => operationFrom({ ...operation, status_url: 'https://other.test/' }, 'w1')).toThrow()
  })
  test('list normalizes canonical top-level pagination and does not accept the discarded nested wire', async () => {
    const data = { data: [], meta: { request_id: 'req_1', schema_version: '1.0' }, next_cursor: '', has_more: false }
    const api = new FarmAPI(new FarmTransport(async () => Response.json(data)))
    expect(await api.list('w1', new AbortController().signal)).toEqual({ items: [], next_cursor: '', has_more: false })
    const old = new FarmAPI(new FarmTransport(async () => reply({ items: [], next_cursor: '', has_more: false })))
    await expect(old.list('w1', new AbortController().signal)).rejects.toThrow()
  })
})
