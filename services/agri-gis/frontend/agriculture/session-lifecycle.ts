import type { SessionController } from './session'

// pageshow also fires when the browser restores this document from BFCache;
// a restored Vue tree is not evidence that its old account still owns the cookie.
export function bindSessionLifecycle(
  controller: SessionController,
  windowEvents: EventTarget,
  documentEvents: EventTarget,
  visibility: () => DocumentVisibilityState,
  search: () => string,
): () => void {
  const refresh = () => { void controller.refresh(search()) }
  const visible = () => { if (visibility() === 'visible') refresh() }
  windowEvents.addEventListener('focus', refresh)
  windowEvents.addEventListener('pageshow', refresh)
  documentEvents.addEventListener('visibilitychange', visible)
  refresh()
  return () => {
    windowEvents.removeEventListener('focus', refresh)
    windowEvents.removeEventListener('pageshow', refresh)
    documentEvents.removeEventListener('visibilitychange', visible)
    controller.dispose()
  }
}
