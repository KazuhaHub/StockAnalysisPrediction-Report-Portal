import { useEffect, useState } from 'react'
import { api, ApiError } from '../api/client'
import type { ForecastGroupsResponse } from '../api/types'

type RecordState =
  | { kind: 'loading' | 'unavailable' }
  | { kind: 'ready'; data: ForecastGroupsResponse }
  | { kind: 'error'; error: unknown }

// Reader requests never use management aliases or warm admin data. Identity/population changes
// hide old results synchronously, and effect cleanup discards late responses and retry timers.
export function useForecastRecord(query: string, identity: string, allowed: boolean) {
  const [revision, setRevision] = useState(0)
  const key = JSON.stringify([query, identity, allowed, revision])
  const [stored, setStored] = useState<{ key: string; state: RecordState }>()

  useEffect(() => {
    if (!allowed) {
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
          publish({ kind: 'unavailable' })
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
    : stored?.key === key ? stored.state : { kind: 'loading' }
  return { ...state, retry: () => {
    if (state.kind === 'error') setRevision((n) => n + 1)
  } }
}
