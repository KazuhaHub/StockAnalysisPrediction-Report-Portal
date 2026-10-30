import { App, Button } from 'antd'
import { DownloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { runOutcome } from './taskUi'
import { taskKey, useMyTasks } from './useMyTasks'

// The shell's feed of the caller's own tasks (ADR 0047): the polling of useMyTasks plus the toast
// that announces a finished run or export on any page. The Tasks drawer and the header badge read
// the returned list; nothing here draws a surface of its own.
export function useTaskCenter(enabled: boolean) {
  const { t } = useTranslation()
  const { notification } = App.useApp()
  return useMyTasks({
    enabled,
    onFinished: (task) => {
      const name = task.name
      if (task.kind === 'export') {
        if (task.status === 'done') {
          const key = `task-done-${taskKey(task)}`
          notification.success({
            key,
            title: t('tasks.toast.exportDone', { name }),
            actions: task.download ? (
              <Button size="small" type="primary" icon={<DownloadOutlined />} href={task.download} onClick={() => notification.destroy(key)}>
                {t('tasks.download')}
              </Button>
            ) : undefined,
          })
        } else if (task.status === 'failed') {
          notification.error({ key: `task-failed-${taskKey(task)}`, title: t('tasks.toast.exportFailed', { name }), description: task.error || undefined })
        }
        return
      }
      const key = `task-run-${taskKey(task)}`
      if (task.status === 'finished') {
        const outcome = runOutcome(task.counts)
        if (outcome === 'ok') notification.success({ key, title: t('tasks.toast.runFinished', { name }) })
        else if (outcome === 'someFailed') notification.warning({ key, title: t('tasks.toast.runPartial', { name, failed: task.counts?.failed ?? 0 }) })
        else if (outcome === 'partialOnly') notification.warning({ key, title: t('tasks.toast.runPartialOnly', { name, partial: task.counts?.partial ?? 0 }) })
        else notification.error({ key, title: t('tasks.toast.runFailed', { name }) })
      } else if (task.status === 'expired') {
        notification.warning({ key, title: t('tasks.toast.runExpired', { name }) })
      }
    },
  })
}
