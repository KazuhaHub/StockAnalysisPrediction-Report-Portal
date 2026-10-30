import { useSyncExternalStore } from 'react'

/** A device with no hover, or with any coarse pointer: there the hover affordances never appear. */
export const COARSE_POINTER_QUERY = '(hover: none), (any-pointer: coarse)'

function query(): MediaQueryList | null {
  try {
    return typeof window !== 'undefined' && typeof window.matchMedia === 'function' ? window.matchMedia(COARSE_POINTER_QUERY) : null
  } catch {
    return null
  }
}

function subscribe(onChange: () => void): () => void {
  const mql = query()
  mql?.addEventListener?.('change', onChange)
  return () => mql?.removeEventListener?.('change', onChange)
}

const read = () => query()?.matches ?? false

/** True on a touch / coarse-pointer device; follows the media query live. */
export function useCoarsePointer(): boolean {
  return useSyncExternalStore(subscribe, read, () => false)
}
