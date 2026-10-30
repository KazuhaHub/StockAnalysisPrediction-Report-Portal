import { useSyncExternalStore } from 'react'

/** A device whose primary input cannot hover: there the hover affordances never appear. */
export const HOVER_NONE_QUERY = '(hover: none)'

function query(): MediaQueryList | null {
  try {
    return typeof window !== 'undefined' && typeof window.matchMedia === 'function' ? window.matchMedia(HOVER_NONE_QUERY) : null
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

/** True while the primary input cannot hover; follows the media query live. */
export function useHoverNone(): boolean {
  return useSyncExternalStore(subscribe, read, () => false)
}
