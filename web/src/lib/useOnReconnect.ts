import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { loadFailure, type LoadOpts } from './loadFailure'
import { hasPendingDrafts, subscribeDrafts } from './pendingDrafts'

/** Runs `fn` whenever the API client reports the connection is back ('rp:connection-restored'). */
export function useOnReconnect(fn: () => void) {
  const latest = useRef(fn)
  latest.current = fn
  useEffect(() => {
    const onRestored = () => latest.current()
    window.addEventListener('rp:connection-restored', onRestored)
    return () => window.removeEventListener('rp:connection-restored', onRestored)
  }, [])
}

/**
 * For a view that loads once. `failure(e)` goes in the load's catch: it returns null for a
 * connection loss (leave state untouched) and the error text otherwise. After a connection loss,
 * `retryKey` changes when the connection returns, so adding it to the load effect's dependencies
 * re-runs the load. A view whose load never failed on the connection is not reloaded, so edits in
 * progress are not overwritten by a refresh nobody needed. Pass `{ guardDrafts: true }` for a load
 * that writes into a form.
 */
export function useLoadFailure(options: { guardDrafts?: boolean } = {}) {
  const { t } = useTranslation()
  const tRef = useRef(t)
  const guardDrafts = !!options.guardDrafts
  tRef.current = t
  const stale = useRef(false)
  const [retryKey, setRetryKey] = useState(0)
  // A load that writes into a form (`guardDrafts`) must not run while the form holds unsaved edits:
  // it would overwrite them. It is held until the form is clean (saved or discarded) instead.
  const held = useRef(false)
  useOnReconnect(() => {
    if (!stale.current) return
    stale.current = false
    if (guardDrafts && hasPendingDrafts()) { held.current = true; return }
    setRetryKey((n) => n + 1)
  })
  useEffect(() => {
    if (!guardDrafts) return
    return subscribeDrafts(() => {
      if (!held.current || hasPendingDrafts()) return
      held.current = false
      setRetryKey((n) => n + 1)
    })
  }, [guardDrafts])
  const failure = useCallback((e: unknown, opts?: LoadOpts): string | null => {
    const text = loadFailure(e, tRef.current)
    if (text === null && !opts?.quiet) stale.current = true
    return text
  }, [])
  // `.catch(catchLoad(setLoadErr))`: report a real failure, stay silent on a connection loss.
  const catchLoad = useCallback(
    (report: (text: string) => void, opts?: LoadOpts) => (e: unknown) => {
      const text = failure(e, opts)
      if (text !== null) report(text)
    },
    [failure],
  )
  return { failure, catchLoad, retryKey }
}
