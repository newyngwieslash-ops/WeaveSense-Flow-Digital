import type { ProjectUser } from '../types/workbench'

// Legacy identities are demo fixtures, never platform principals. A production
// build cannot enable this via storage, query parameters or a runtime flag.
export function isIsolatedLegacyDemo(dev: boolean, mode: string, hostname: string): boolean {
  return dev === true && mode === 'demo' && ['127.0.0.1', '[::1]'].includes(hostname)
}

export const legacyDemoEnabled = (): boolean => isIsolatedLegacyDemo(
  import.meta.env?.DEV === true, import.meta.env?.MODE ?? '',
  typeof window === 'undefined' ? '' : window.location.hostname,
)

export function selectedDemoIdentity(enabled: boolean, users: ProjectUser[], code: string): ProjectUser | null {
  if (!enabled || !code) return null
  return users.find((user) => user.user_code === code) ?? null
}

export function explicitDemoContext(enabled: boolean, value: unknown): string {
  return enabled && typeof value === 'string' && /^[A-Za-z0-9_-]{1,128}$/.test(value) ? value : ''
}
