import type { SessionController } from './session'
import { SESSION_RECHECK_MS, subscribeSessionHints } from '../../../../contracts/agriculture/session-hints'

// pageshow also fires when the browser restores this document from BFCache;
// a restored Vue tree is not evidence that its old account still owns the cookie.
export function bindSessionLifecycle(
  controller: SessionController,
  windowEvents: EventTarget,
  documentEvents: EventTarget,
  visibility: () => DocumentVisibilityState,
  search: () => string,
  pathname: () => string = () => '/agri/',
  hash: () => string = () => '',
  listenHints: (invalidate: () => void) => () => void = invalidate => typeof window === 'undefined' ? () => {} : subscribeSessionHints(invalidate),
): () => void {
  const refresh = () => {
    if (visibility() !== 'visible') { controller.invalidate(); return }
    void controller.refresh(search(), pathname(), hash())
  }
  const visible = () => { refresh() }
  const unlisten = listenHints(() => { controller.invalidate(); refresh() })
  const timer = setInterval(() => { if (visibility() === 'visible') refresh() }, SESSION_RECHECK_MS)
  windowEvents.addEventListener('focus', refresh)
  windowEvents.addEventListener('pageshow', refresh)
  windowEvents.addEventListener('popstate', refresh)
  windowEvents.addEventListener('hashchange', refresh)
  documentEvents.addEventListener('visibilitychange', visible)
  refresh()
  return () => {
    clearInterval(timer)
    unlisten()
    windowEvents.removeEventListener('focus', refresh)
    windowEvents.removeEventListener('pageshow', refresh)
    windowEvents.removeEventListener('popstate', refresh)
    windowEvents.removeEventListener('hashchange', refresh)
    documentEvents.removeEventListener('visibilitychange', visible)
    controller.dispose()
  }
}
