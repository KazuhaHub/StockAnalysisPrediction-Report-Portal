import { useCallback, useSyncExternalStore } from 'react'

// Whose runs the queue views show (ADR 0047): an administrator picks their own or everyone's;
// anyone else is always scoped to their own by the server, so the choice is not offered or sent.
// The pick is a per-browser convenience kept in localStorage and shared by every queue view (the
// page, the drawer, the header badge) so they never disagree.
export type QueueScope = 'all' | 'mine'

const KEY = 'rp.queueScope'
const EVENT = 'rp:queue-scope'

export function readQueueScope(): QueueScope {
  try {
    return localStorage.getItem(KEY) === 'mine' ? 'mine' : 'all'
  } catch {
    return 'all'
  }
}

export function writeQueueScope(scope: QueueScope) {
  try {
    localStorage.setItem(KEY, scope)
  } catch {
    // Storage blocked: the pick still applies to this page through the event below.
    memo = scope
  }
  window.dispatchEvent(new Event(EVENT))
}

// Fallback when storage throws, so the toggle still works within the tab.
let memo: QueueScope | null = null

function subscribe(listener: () => void) {
  window.addEventListener(EVENT, listener)
  window.addEventListener('storage', listener)
  return () => {
    window.removeEventListener(EVENT, listener)
    window.removeEventListener('storage', listener)
  }
}

const snapshot = () => memo ?? readQueueScope()

/** The effective scope for this viewer and a setter; non-administrators are always 'mine'. */
export function useQueueScope(admin: boolean): [QueueScope, (scope: QueueScope) => void] {
  const stored = useSyncExternalStore(subscribe, snapshot, () => 'all' as QueueScope)
  const set = useCallback((scope: QueueScope) => writeQueueScope(scope), [])
  return [admin ? stored : 'mine', set]
}

/** Adds scope=mine to a queue URL when an administrator asked for their own runs. */
export function withQueueScope(url: string, scope: QueueScope, admin: boolean): string {
  if (!admin || scope !== 'mine') return url
  return url + (url.includes('?') ? '&' : '?') + 'scope=mine'
}
