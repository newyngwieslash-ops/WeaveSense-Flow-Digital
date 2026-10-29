import { describe, expect, test, mock } from 'bun:test'
import { createPinia, setActivePinia } from 'pinia'
import { readFileSync } from 'node:fs'
import { isIsolatedLegacyDemo, selectedDemoIdentity, explicitDemoContext } from '../src/security/legacyIdentity'
import type { ProjectUser } from '../src/types/workbench'

let requests = 0
mock.module('../src/api/index', () => ({ getProjectUsers: async () => { requests++; return { users: [] } } }))
const { useUserStore } = await import('../src/store/userStore')
const user = { user_code: 'forged-admin', is_default: true, capabilities: ['upload_field_data', 'operate_uav_missions'] } as ProjectUser

describe('legacy identity is not production authority', () => {
  test('production/demo spoofing never enables selected identities', () => {
    for (const [dev, mode, host] of [[false,'demo','127.0.0.1'], [true,'production','127.0.0.1'], [true,'demo','localhost'], [true,'demo','production.example']] as const) {
      expect(isIsolatedLegacyDemo(dev, mode, host)).toBe(false)
    }
    expect(isIsolatedLegacyDemo(true, 'demo', '127.0.0.1')).toBe(true)
    expect(selectedDemoIdentity(false, [user], user.user_code)).toBeNull()
    expect(selectedDemoIdentity(true, [user], '')).toBeNull()
    expect(selectedDemoIdentity(true, [user], 'unknown')).toBeNull()
    expect(selectedDemoIdentity(true, [user], user.user_code)).toBe(user)
  })
  test('real Pinia store ignores storage and cannot promote default/operator role', async () => {
    const previous = Object.getOwnPropertyDescriptor(globalThis, 'window')
    Object.defineProperty(globalThis, 'window', { configurable: true, value: {
      location: { hostname: '127.0.0.1' },
      get localStorage() { throw new Error('storage must not be read') },
    } })
    try {
      setActivePinia(createPinia())
      const store = useUserStore()
      store.usersRef = [user]
      store.currentUserCodeRef = user.user_code
      expect(store.currentUserComputed).toBeNull()
      expect(store.hasCapability('upload_field_data')).toBe(false)
      expect(store.hasCapability('operate_uav_missions')).toBe(false)
      await store.initialize('RS-2026')
      store.setCurrentUser(user.user_code)
      expect(store.currentUserCodeRef).toBe('')
      expect(store.usersRef).toEqual([])
      expect(requests).toBe(0)
    } finally {
      if (previous) Object.defineProperty(globalThis, 'window', previous)
      else Reflect.deleteProperty(globalThis, 'window')
    }
  })
  test('mobile context has no default project or task', () => {
    expect(explicitDemoContext(false, 'RS-2026')).toBe('')
    expect(explicitDemoContext(true, undefined)).toBe('')
    expect(explicitDemoContext(true, ['forged'])).toBe('')
    for (const name of ['FieldCaptureView', 'UavCaptureView']) {
      const source = readFileSync(new URL(`../src/views/${name}.vue`, import.meta.url), 'utf8')
      expect(source).not.toContain('setCurrentUser(')
      expect(source).not.toContain("'RS-2026")
      expect(source).toContain('v-if="legacyDemoEnabled()"')
    }
  })
  test('legacy network is fail closed, not an identity-switcher visual hide', () => {
    const source = readFileSync(new URL('../src/api/request.ts', import.meta.url), 'utf8')
    expect(source).toContain("throw new Error('旧GIS接口未接入可信平台身份，当前不可用')")
    expect(source).not.toContain('return config')
  })
})
