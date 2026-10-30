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
  return (loc.pathname === '/queue' || loc.pathname === '/apps/batch') && new URLSearchParams(loc.search).get('job') === String(id)
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
