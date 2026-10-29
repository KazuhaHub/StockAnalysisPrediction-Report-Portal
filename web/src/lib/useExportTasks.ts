import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import type { ExportJob } from '../api/types'
import { EXPORTS_CHANGED_EVENT, isActiveJob, type ExportsChangedDetail } from './exportJobs'
import { exportBase, type ExportMode } from './exportMode'
import { startVisiblePoll } from './visiblePoll'

export const ACTIVE_POLL_MS = 3000
export const IDLE_POLL_MS = 30000
const RECENT_LIMIT = 10

// The task tray's data. It polls the cheap `active=1` listing — fast while something is running,
// slowly otherwise — and the moment a task this browser was watching leaves that listing it asks
// for the task itself, so the completion toast carries the real final state and fires exactly once.
//
// The tray lists every administrator's tasks, but a completion or failure toast is addressed to the
// person who asked for the export: with `currentUser` given, onFinished fires only for their own jobs
// (null, an identity not yet known, fires for none). Left undefined, every finished job is announced.
export function useExportTasks(opts: { enabled: boolean; mode?: ExportMode; onFinished?: (job: ExportJob) => void; currentUser?: string | null }) {
  const { enabled } = opts
  const base = exportBase(opts.mode)
  const [active, setActive] = useState<ExportJob[]>([])
  const [recent, setRecent] = useState<ExportJob[]>([])
  const watched = useRef(new Set<string>())
  const onFinished = useRef(opts.onFinished)
  onFinished.current = opts.onFinished
  const currentUser = useRef(opts.currentUser)
  currentUser.current = opts.currentUser
  const alive = useRef(true)

  const loadRecent = useCallback(async () => {
    try {
      const r = await api.get<{ jobs: ExportJob[] }>(`${base}?limit=${RECENT_LIMIT}`)
      if (alive.current) setRecent(r?.jobs ?? [])
    } catch {
      // The tray is a convenience; the history table reports errors.
    }
  }, [base])

  const settle = useCallback(
    async (ids: string[]) => {
      for (const id of ids) {
        try {
          const job = await api.get<ExportJob>(`${base}/${encodeURIComponent(id)}`)
          if (isActiveJob(job)) continue // still going; the active listing was just a moment behind
          watched.current.delete(id)
          const me = currentUser.current
          const mine = me === undefined || (me !== null && job.requested_by === me)
          if (mine && (job.status === 'done' || job.status === 'failed')) onFinished.current?.(job)
        } catch (e) {
          if ((e as { status?: number })?.status === 404) watched.current.delete(id)
        }
      }
      if (ids.length > 0) await loadRecent()
    },
    [base, loadRecent],
  )

  const refresh = useCallback(async () => {
    try {
      const r = await api.get<{ jobs: ExportJob[] }>(`${base}?active=1`)
      const jobs = r?.jobs ?? []
      if (!alive.current) return
      setActive(jobs)
      const now = new Set(jobs.map((j) => j.id))
      for (const j of jobs) watched.current.add(j.id)
      await settle([...watched.current].filter((id) => !now.has(id)))
    } catch {
      // Keep the last good state; the next tick tries again.
    }
  }, [base, settle])

  const hasActive = active.length > 0

  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
    }
  }, [])

  useEffect(() => {
    if (!enabled) return
    void refresh()
    void loadRecent()
    const onEvent = (e: Event) => {
      const detail = (e as CustomEvent<ExportsChangedDetail>).detail
      if (detail?.job) watched.current.add(detail.job.id)
      void refresh()
    }
    const onFocus = () => void refresh()
    window.addEventListener(EXPORTS_CHANGED_EVENT, onEvent)
    window.addEventListener('focus', onFocus)
    return () => {
      window.removeEventListener(EXPORTS_CHANGED_EVENT, onEvent)
      window.removeEventListener('focus', onFocus)
    }
  }, [enabled, refresh, loadRecent])

  useEffect(() => {
    if (!enabled) return
    return startVisiblePoll(refresh, hasActive ? ACTIVE_POLL_MS : IDLE_POLL_MS, { skipLeading: true })
  }, [enabled, hasActive, refresh])

  return { active, recent, refresh, loadRecent }
}
