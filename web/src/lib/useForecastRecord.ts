import { useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import type { ForecastGroupsResponse } from '../api/types'

export type RecordState =
  // idle: the caller has no query yet (a custom range with a missing date). Nothing is requested.
  | { kind: 'idle' | 'loading' }
  // unavailable: no access, or the server refused (403). Terminal; `code` says which refusal.
  | { kind: 'unavailable'; code?: string }
  | { kind: 'ready'; data: ForecastGroupsResponse }
  | { kind: 'error'; error: unknown }

// Reader requests never use management aliases or warm admin data. Identity/population changes
// hide old results synchronously, and effect cleanup discards late responses and retry timers.
//
// A null query means "nothing to ask yet": the hook is idle and makes no request. A denied reader
// (allowed === false) is unavailable without a request. A 403 is terminal: it is the server's
// decision, so asking again cannot change it and the hook never does.
export function useForecastRecord(query: string | null, identity: string, allowed: boolean) {
  const [revision, setRevision] = useState(0)
  const key = JSON.stringify([query, identity, allowed, revision])
  const [stored, setStored] = useState<{ key: string; state: RecordState }>()

  useEffect(() => {
    if (!allowed || query === null) {
      setStored(undefined)
      return
    }
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const publish = (state: RecordState) => {
      if (!cancelled) setStored({ key, state })
    }
    publish({ kind: 'loading' })
    const load = async (attempt: number) => {
      try {
        const data = await api.get<ForecastGroupsResponse>(`/api/forecast/groups?${query}`)
        publish({ kind: 'ready', data })
      } catch (error) {
        if (cancelled) return
        if (error instanceof ApiError && error.status === 403) {
          publish({ kind: 'unavailable', code: error.code })
        } else if (error instanceof ApiError && error.status === 503 && error.code === 'forecast_busy' && attempt < 3) {
          const base = 250 * 2 ** attempt
          timer = setTimeout(() => { void load(attempt + 1) }, base + Math.floor(Math.random() * base))
        } else {
          publish({ kind: 'error', error })
        }
      }
    }
    void load(0)
    return () => {
      cancelled = true
      clearTimeout(timer)
    }
  }, [allowed, identity, key, query])

  const state: RecordState = !allowed ? { kind: 'unavailable' }
    : query === null ? { kind: 'idle' }
      : stored?.key === key ? stored.state : { kind: 'loading' }
  return { ...state, retry: () => {
    if (state.kind === 'error') setRevision((n) => n + 1)
  } }
}
