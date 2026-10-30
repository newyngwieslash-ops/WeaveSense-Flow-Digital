// Untrusted, secret-free invalidation hints. Never carry or apply identity/roles.
export const SESSION_HINT_KEY = 'weavesense.agriculture.session-invalidated.v1'
export const SESSION_RECHECK_MS = 30000
type Hint = { version: 1; type: 'invalidate'; event: string }
type Channel = Pick<BroadcastChannel, 'postMessage' | 'addEventListener' | 'removeEventListener' | 'close'>
type HintWindow = Pick<Window, 'addEventListener' | 'removeEventListener' | 'localStorage'>
export function validSessionHint(value: unknown): value is Hint {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const item = value as Record<string, unknown>
  return Object.keys(item).sort().join(',') === 'event,type,version' && item.version === 1 && item.type === 'invalidate'
    && typeof item.event === 'string' && /^[a-f0-9]{32}$/.test(item.event)
}
let documentEventPrefix = ''
const randomHex = () => Array.from(crypto.getRandomValues(new Uint8Array(8)), b => b.toString(16).padStart(2, '0')).join('')
export function publishSessionHint(target: Pick<HintWindow, 'localStorage'> | undefined = typeof window === 'undefined' ? undefined : window,
  createChannel: () => Channel = () => new BroadcastChannel(SESSION_HINT_KEY)) {
  if (!target) return
  // A different channel object in this same document also receives broadcasts.
  // Suppress only our own document's events; other documents still revalidate.
  documentEventPrefix ||= randomHex()
  const hint: Hint = { version: 1, type: 'invalidate', event: documentEventPrefix + randomHex() }
  try { const channel = createChannel(); channel.postMessage(hint); channel.close() } catch { /* storage fallback below */ }
  try { target.localStorage.setItem(SESSION_HINT_KEY, JSON.stringify(hint)); target.localStorage.removeItem(SESSION_HINT_KEY) } catch { /* focus and bounded recheck remain mandatory */ }
}
export function subscribeSessionHints(invalidate: () => void, target: HintWindow = window,
  createChannel: () => Channel = () => new BroadcastChannel(SESSION_HINT_KEY)): () => void {
  let channel: Channel | undefined
  // Only deduplicate the same broadcast/storage hint; no identity or permission cache.
  let lastEvent = ''
  const accept = (value: unknown) => {
    if (!validSessionHint(value) || value.event === lastEvent || (documentEventPrefix && value.event.startsWith(documentEventPrefix))) return
    lastEvent = value.event
    invalidate()
  }
  const message = (event: Event) => accept((event as MessageEvent).data)
  const storage = (event: Event) => {
    const value = event as StorageEvent
    if (value.key !== SESSION_HINT_KEY || !value.newValue || value.newValue.length > 160) return
    try { if (value.storageArea !== target.localStorage) return; accept(JSON.parse(value.newValue)) } catch { /* malformed untrusted hint */ }
  }
  try { channel = createChannel(); channel.addEventListener('message', message) } catch { /* storage fallback */ }
  target.addEventListener('storage', storage)
  return () => { channel?.removeEventListener('message', message); channel?.close(); target.removeEventListener('storage', storage) }
}
