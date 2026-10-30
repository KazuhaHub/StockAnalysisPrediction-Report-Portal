import { useTranslation } from 'react-i18next'
import { notifyExportsChanged } from './exportJobs'
import { notify, type NotifyAction } from './notify'
import { runOutcome } from './taskUi'
import { QUEUE_EVENT } from './shortcuts'
import { taskKey, useMyTasks } from './useMyTasks'

// The shell's feed of the caller's own tasks (ADR 0047): the polling of useMyTasks plus the notification
// that announces a finished run or export on any page. The Tasks drawer and the header badge read
// the returned list; nothing here draws a surface of its own.
export function useTaskCenter(enabled: boolean) {
  const { t } = useTranslation()
  // "View" opens the Tasks drawer on the tab that holds the task: runs on Runs, exports on Exports.
  const viewRuns: NotifyAction[] = [{ label: t('tasks.toast.view'), primary: true, onClick: () => window.dispatchEvent(new Event(QUEUE_EVENT)) }]
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
        else if (outcome === 'someFailed') notify({ key, kind: 'partial', title: t('tasks.toast.runPartial', { name, failed: task.counts?.failed ?? 0 }), actions: viewRuns })
        else if (outcome === 'partialOnly') notify({ key, kind: 'partial', title: t('tasks.toast.runPartialOnly', { name, partial: task.counts?.partial ?? 0 }), actions: viewRuns })
        else notify({ key, kind: 'failure', title: t('tasks.toast.runFailed', { name }), actions: viewRuns })
      } else if (task.status === 'expired') {
        notify({ key, kind: 'failure', title: t('tasks.toast.runExpired', { name }), actions: viewRuns })
      }
    },
  })
}
