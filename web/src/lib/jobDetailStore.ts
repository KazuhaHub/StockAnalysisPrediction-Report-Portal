import { useSyncExternalStore } from 'react'

// Which run's detail is open in place over the current page. A tiny module store so that the Tasks
// drawer rows and the completion notifications can ask for it without owning the drawer; the shell
// hosts it once (JobDetailHost).
let current: number | null = null
const listeners = new Set<() => void>()

function set(id: number | null) {
  if (current === id) return
  current = id
  listeners.forEach((l) => l())
}

/**
 * Whether the page itself already shows this run in its own ?job= drawer (the queue page and the batch
 * console both embed the queue table), so opening the global detail would stack a second one.
 */
export function pageShowsJob(loc: { pathname: string; search: string }, id: number): boolean {
  if (loc.pathname !== '/queue' && loc.pathname !== '/apps/batch') return false
  const raw = new URLSearchParams(loc.search).get('job')
  return raw !== null && Number(raw) === id // numeric, like the queue page: ?job=07 is job 7
}

/** Asked of the shell to put its Tasks drawer away (it owns the drawer's open state). */
export const CLOSE_TASKS_EVENT = 'rp:close-tasks'

/** Puts away everything laid over the page for a run: the in-place detail and the Tasks drawer. */
export function dismissTaskOverlays(): void {
  closeJobDetail()
  window.dispatchEvent(new Event(CLOSE_TASKS_EVENT))
}

export const openJobDetail = (id: number): void => set(id)
export const closeJobDetail = (): void => set(null)

const subscribe = (l: () => void) => {
  listeners.add(l)
  return () => {
    listeners.delete(l)
  }
}

/** The run whose detail is open, or null. */
export const useOpenJobDetail = (): number | null => useSyncExternalStore(subscribe, () => current, () => null)
