import { useTranslation } from 'react-i18next'
import { notifyExportsChanged } from './exportJobs'
import { notify, type NotifyAction } from './notify'
import { useRef } from 'react'
import { useLocation } from 'react-router'
import { openJobDetail, pageShowsJob } from './jobDetailStore'
import { runOutcome } from './taskUi'
import { taskKey, useMyTasks } from './useMyTasks'
import type { TaskItem } from '../api/types'

// The shell's feed of the caller's own tasks (ADR 0047): the polling of useMyTasks plus the notification
// that announces a finished run or export on any page. The Tasks drawer and the header badge read
// the returned list; nothing here draws a surface of its own.
export function useTaskCenter(enabled: boolean) {
  const { t } = useTranslation()
  // The callbacks below outlive the render that made them (a toast's button), so read the page from a ref.
  const loc = useLocation()
  const locRef = useRef(loc)
  locRef.current = loc
  // "View" opens the thing itself: a run's detail in place over the current page, the Tasks drawer's Exports tab for an export.
  // A caller without run_batch gets no link from the server, and the detail would only 403: offer nothing.
  const viewRun = (task: TaskItem): NotifyAction[] => !task.link ? [] : [{ label: t('tasks.toast.view'), primary: true, onClick: () => { if (!pageShowsJob(locRef.current, Number(task.id))) openJobDetail(Number(task.id)) } }]
  const viewExports: NotifyAction[] = [{ label: t('tasks.toast.view'), primary: true, onClick: () => notifyExportsChanged({ open: true }) }]
  return useMyTasks({
    enabled,
    onFinished: (task) => {
      const name = task.name
      if (task.kind === 'export') {
        if (task.status === 'done') {
          notify({
            key: `task-done-${taskKey(task)}`,
            kind: task.download ? 'actionable' : 'success',
            title: t('tasks.toast.exportDone', { name }),
            actions: task.download ? [{ label: t('tasks.download'), primary: true, href: task.download }] : undefined,
          })
        } else if (task.status === 'failed') {
          notify({ key: `task-failed-${taskKey(task)}`, kind: 'failure', title: t('tasks.toast.exportFailed', { name }), description: task.error || undefined, actions: viewExports })
        }
        return
      }
      const key = `task-run-${taskKey(task)}`
      if (task.status === 'finished') {
        const outcome = runOutcome(task.counts)
        if (outcome === 'ok') notify({ key, kind: 'success', title: t('tasks.toast.runFinished', { name }) })
        else if (outcome === 'someFailed') notify({ key, kind: 'partial', title: t('tasks.toast.runPartial', { name, failed: task.counts?.failed ?? 0 }), actions: viewRun(task) })
        else if (outcome === 'partialOnly') notify({ key, kind: 'partial', title: t('tasks.toast.runPartialOnly', { name, partial: task.counts?.partial ?? 0 }), actions: viewRun(task) })
        else notify({ key, kind: 'failure', title: t('tasks.toast.runFailed', { name }), actions: viewRun(task) })
      } else if (task.status === 'expired') {
        notify({ key, kind: 'failure', title: t('tasks.toast.runExpired', { name }), actions: viewRun(task) })
      }
    },
  })
}
