import { useTranslation } from 'react-i18next'
import { notify } from './notify'
import { runOutcome } from './taskUi'
import { taskKey, useMyTasks } from './useMyTasks'

// The shell's feed of the caller's own tasks (ADR 0047): the polling of useMyTasks plus the notification
// that announces a finished run or export on any page. The Tasks drawer and the header badge read
// the returned list; nothing here draws a surface of its own.
export function useTaskCenter(enabled: boolean) {
  const { t } = useTranslation()
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
          notify({ key: `task-failed-${taskKey(task)}`, kind: 'failure', title: t('tasks.toast.exportFailed', { name }), description: task.error || undefined })
        }
        return
      }
      const key = `task-run-${taskKey(task)}`
      if (task.status === 'finished') {
        const outcome = runOutcome(task.counts)
        if (outcome === 'ok') notify({ key, kind: 'success', title: t('tasks.toast.runFinished', { name }) })
        else if (outcome === 'someFailed') notify({ key, kind: 'partial', title: t('tasks.toast.runPartial', { name, failed: task.counts?.failed ?? 0 }) })
        else if (outcome === 'partialOnly') notify({ key, kind: 'partial', title: t('tasks.toast.runPartialOnly', { name, partial: task.counts?.partial ?? 0 }) })
        else notify({ key, kind: 'failure', title: t('tasks.toast.runFailed', { name }) })
      } else if (task.status === 'expired') {
        notify({ key, kind: 'failure', title: t('tasks.toast.runExpired', { name }) })
      }
    },
  })
}
