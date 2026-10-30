import { useCallback, useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { isQuiet, loadFailure } from './loadFailure'

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
 * progress are not overwritten by a refresh nobody needed.
 */
export function useLoadFailure() {
  const { t } = useTranslation()
  const tRef = useRef(t)
  tRef.current = t
  const stale = useRef(false)
  const [retryKey, setRetryKey] = useState(0)
  useOnReconnect(() => {
    if (!stale.current) return
    stale.current = false
    setRetryKey((n) => n + 1)
  })
  const failure = useCallback((e: unknown, fallbackKey?: string): string | null => {
    const text = loadFailure(e, tRef.current, fallbackKey)
    if (text === null && !isQuiet()) stale.current = true
    return text
  }, [])
  // `.catch(catchLoad(setLoadErr))`: report a real failure, stay silent on a connection loss.
  const catchLoad = useCallback(
    (report: (text: string) => void, fallbackKey?: string) => (e: unknown) => {
      const text = failure(e, fallbackKey)
      if (text !== null) report(text)
    },
    [failure],
  )
  return { failure, catchLoad, retryKey }
}
