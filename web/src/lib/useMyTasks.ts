import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../api/client'
import type { TaskItem } from '../api/types'
import { EXPORTS_CHANGED_EVENT, type ExportsChangedDetail } from './exportJobs'
import { startVisiblePoll } from './visiblePoll'

export const ACTIVE_POLL_MS = 3000
export const IDLE_POLL_MS = 30000
const LIMIT = 10

export const taskKey = (t: Pick<TaskItem, 'kind' | 'id'>): string => `${t.kind}:${t.id}`

/** Queued, running and cancelling tasks are in flight; everything else is settled. */
export const isActiveTask = (t: Pick<TaskItem, 'status'>): boolean => t.status === 'queued' || t.status === 'running' || t.status === 'cancelling'

// The My tasks tray's data (ADR 0047): the caller's own runs and exports from one endpoint. It polls
// fast while anything is in flight and slowly otherwise. A task this browser saw in flight that now
// shows up settled is handed to onFinished once, which is how a completion toast reaches any page.
export function useMyTasks(opts: { enabled: boolean; onFinished?: (task: TaskItem) => void }) {
  const { enabled } = opts
  const [tasks, setTasks] = useState<TaskItem[]>([])
  const watched = useRef(new Set<string>())
  const onFinished = useRef(opts.onFinished)
  onFinished.current = opts.onFinished
  const alive = useRef(true)
  // Polls can overlap (a focus event during a slow reply). Replies are applied in request order: one
  // that arrives after a newer one was applied describes an older state and is dropped.
  const issued = useRef(0)
  const applied = useRef(0)

  const refresh = useCallback(async () => {
    const seq = ++issued.current
    try {
      const r = await api.get<{ tasks: TaskItem[] }>(`/api/tasks?limit=${LIMIT}`)
      if (!alive.current || seq < applied.current) return
      applied.current = seq
      const list = r?.tasks ?? []
      setTasks(list)
      const byKey = new Map(list.map((t) => [taskKey(t), t]))
      for (const t of list) if (isActiveTask(t)) watched.current.add(taskKey(t))
      for (const key of [...watched.current]) {
        const t = byKey.get(key)
        if (t && isActiveTask(t)) continue
        watched.current.delete(key)
        if (t) onFinished.current?.(t)
      }
    } catch {
      // Keep the last good state; the next tick tries again.
    }
  }, [])

  const active = tasks.filter(isActiveTask)
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
    const onEvent = (e: Event) => {
      const job = (e as CustomEvent<ExportsChangedDetail>).detail?.job
      if (job) watched.current.add(taskKey({ kind: 'export', id: job.id }))
      void refresh()
    }
    const onFocus = () => void refresh()
    window.addEventListener(EXPORTS_CHANGED_EVENT, onEvent)
    window.addEventListener('focus', onFocus)
    return () => {
      window.removeEventListener(EXPORTS_CHANGED_EVENT, onEvent)
      window.removeEventListener('focus', onFocus)
    }
  }, [enabled, refresh])

  useEffect(() => {
    if (!enabled) return
    return startVisiblePoll(refresh, hasActive ? ACTIVE_POLL_MS : IDLE_POLL_MS, { skipLeading: true })
  }, [enabled, hasActive, refresh])

  return { tasks, active, refresh }
}
