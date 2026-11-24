import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import { parseOperationRows, type PlatformOperationRow } from '../../api/appOperations'
import { useAuth } from '../../auth'
import { platformRoot } from './platformLifecycle'

/** How often a feed with a running operation asks again. */
export const OPERATION_POLL_MS = 2500

export interface OperationFeed {
  /** null until the first answer (or after a failure with nothing read yet). */
  rows: PlatformOperationRow[] | null
  error: string
  loading: boolean
  reload: () => void
}

/**
 * Reads the operation history at `query` (`installation=<id>&limit=n` or `latest=1`) and keeps reading it every few
 * seconds while any row is still running. A change of `refreshKey` (the console's lifecycle changed) reads it again at
 * once. A read in flight is dropped when the feed is turned off, the account changes or the page goes. Rows already
 * read stay on screen while the next read is out, so a refresh never blanks the list.
 */
export function useOperationFeed(query: string, enabled: boolean, refreshKey?: unknown): OperationFeed {
  const { t } = useTranslation()
  // A translator that is new on every render (a test stand-in) must not restart the read.
  const text = useRef(t)
  text.current = t
  const { user } = useAuth()
  const [rows, setRows] = useState<PlatformOperationRow[] | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [attempt, setAttempt] = useState(0)
  const reload = useCallback(() => setAttempt(v => v + 1), [])

  useEffect(() => { setRows(null); setError('') }, [query, user])

  useEffect(() => {
    if (!enabled) return
    const abort = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const read = async () => {
      setLoading(true)
      try {
        const parsed = parseOperationRows(await api.get<unknown>(`${platformRoot}/operations?${query}`, { signal: abort.signal }))
        if (abort.signal.aborted) return
        if (!parsed) throw new Error(text.current('appPlatform.invalidOperation'))
        setRows(parsed); setError('')
        if (parsed.some(row => !row.terminal)) timer = setTimeout(() => { void read() }, OPERATION_POLL_MS)
      } catch (e) {
        if (!abort.signal.aborted) setError(errText(e, text.current))
      } finally {
        if (!abort.signal.aborted) setLoading(false)
      }
    }
    void read()
    return () => { abort.abort(); clearTimeout(timer) }
  }, [query, enabled, user, attempt, refreshKey])

  return useMemo(() => ({ rows, error, loading, reload }), [rows, error, loading, reload])
}

/** The newest operation of every installation, by installation id: what the app cards show. */
export function useLatestOperations(enabled: boolean, refreshKey?: unknown): Record<string, PlatformOperationRow> {
  const feed = useOperationFeed('latest=1', enabled, refreshKey)
  return useMemo(() => Object.fromEntries((feed.rows ?? []).map(row => [row.installation_id, row])), [feed.rows])
}
