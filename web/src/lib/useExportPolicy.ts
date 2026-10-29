import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import type { ExportPolicy } from '../api/types'
import { EXPORTS_CHANGED_EVENT } from './exportJobs'

// The signed-in reader's export policy and today's usage (ADR 0046). It reloads when a task is
// submitted or cancelled, because both change what is left of today's quota. `null` until the first
// answer; a failed load leaves it null, which the callers treat as "not allowed".
export function useExportPolicy(enabled = true) {
  const [policy, setPolicy] = useState<ExportPolicy | null>(null)
  const [loaded, setLoaded] = useState(false)
  const alive = useRef(true)
  const load = useCallback(async () => {
    try {
      const p = await api.get<ExportPolicy>('/api/exports/policy')
      if (alive.current) setPolicy(p)
    } catch {
      if (alive.current) setPolicy(null)
    } finally {
      if (alive.current) setLoaded(true)
    }
  }, [])
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])
  useEffect(() => {
    if (!enabled) return
    void load()
    window.addEventListener(EXPORTS_CHANGED_EVENT, load)
    return () => window.removeEventListener(EXPORTS_CHANGED_EVENT, load)
  }, [enabled, load])
  return { policy, loaded, reload: load }
}
