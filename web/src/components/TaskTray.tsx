import { useEffect, useState } from 'react'
import { App, Badge, Button, Empty, Popover, Progress, Space, Tag, Typography, theme } from 'antd'
import { DownloadOutlined, HourglassOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import type { TaskItem } from '../api/types'
import { EXPORTS_CHANGED_EVENT, notifyExportsChanged, type ExportsChangedDetail } from '../lib/exportJobs'
import { isActiveTask, taskKey, useMyTasks } from '../lib/useMyTasks'

const STATUS_COLOR: Record<string, string> = {
  queued: 'default',
  running: 'processing',
  cancelling: 'warning',
  done: 'success',
  finished: 'success',
  failed: 'error',
  canceled: 'warning',
  cancelled: 'warning',
  expired: 'default',
}

// A finished run is only as good as its rows: some failures make it a partial result, all of them a failure.
function runOutcome(t: TaskItem): 'ok' | 'someFailed' | 'partialOnly' | 'failed' {
  const c = t.counts
  if (!c || c.failed + c.partial === 0) return 'ok'
  if (c.failed === 0) return 'partialOnly'
  return c.succeeded === 0 && c.partial === 0 ? 'failed' : 'someFailed'
}

export interface TaskTrayProps {
  /** Holds run_batch: runs are theirs to submit, and the queue page is theirs to open. */
  canRun: boolean
  /** Reader export is allowed for them. */
  canExport: boolean
  /** An administrator: their exports live in the console. */
  admin: boolean
}

// The top-bar "My tasks" tray (ADR 0047): the caller's own runs and exports in one place, the
// portal's version of Workspace's hourglass. Each queued task says how many tasks are ahead of it,
// never whose. A toast announces a finished task on any page.
export default function TaskTray({ canRun, canExport, admin }: TaskTrayProps) {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const { notification, message } = App.useApp()
  const [open, setOpen] = useState(false)
  const enabled = canRun || canExport || admin

  const { tasks, active, refresh } = useMyTasks({
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
        const outcome = runOutcome(task)
        if (outcome === 'ok') notification.success({ key, title: t('tasks.toast.runFinished', { name }) })
        else if (outcome === 'someFailed') notification.warning({ key, title: t('tasks.toast.runPartial', { name, failed: task.counts?.failed ?? 0 }) })
        else if (outcome === 'partialOnly') notification.warning({ key, title: t('tasks.toast.runPartialOnly', { name, partial: task.counts?.partial ?? 0 }) })
        else notification.error({ key, title: t('tasks.toast.runFailed', { name }) })
      } else if (task.status === 'expired') {
        notification.warning({ key, title: t('tasks.toast.runExpired', { name }) })
      }
    },
  })

  useEffect(() => {
    const onEvent = (e: Event) => {
      if ((e as CustomEvent<ExportsChangedDetail>).detail?.open) setOpen(true)
    }
    window.addEventListener(EXPORTS_CHANGED_EVENT, onEvent)
    return () => window.removeEventListener(EXPORTS_CHANGED_EVENT, onEvent)
  }, [])

  useEffect(() => {
    if (open) void refresh()
  }, [open, refresh])

  if (!enabled) return null

  const cancel = async (task: TaskItem) => {
    if (!task.cancel) return
    try {
      await api.post(task.cancel)
      void refresh()
      if (task.kind === 'export') notifyExportsChanged()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const statusLabel = (task: TaskItem) => t(task.kind === 'run' ? `tasks.runStatus.${task.status}` : `export.status.${task.status}`, task.status)
  const exportsHref = admin ? '/manage/exports' : '/exports'

  const content = (
    <div style={{ width: 380, maxWidth: '86vw' }}>
      <Typography.Text strong style={{ display: 'block', marginBottom: 4 }}>
        {t('tasks.title')}
      </Typography.Text>
      {tasks.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('tasks.empty')} />
      ) : (
        <ul style={{ listStyle: 'none', margin: 0, padding: 0, maxHeight: 360, overflowY: 'auto' }} aria-label={t('tasks.title')}>
          {tasks.map((task) => {
            const live = isActiveTask(task)
            const pct = task.total > 0 ? Math.max(0, Math.min(100, Math.floor((task.done / task.total) * 100))) : 0
            return (
              <li key={taskKey(task)} style={{ padding: '10px 0', borderBottom: `1px solid ${token.colorBorderSecondary}` }}>
                <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                  <Tag style={{ marginInlineEnd: 0 }}>{t(`tasks.kind.${task.kind}`)}</Tag>
                  <Typography.Text ellipsis style={{ flex: 1, minWidth: 0 }} title={task.name}>
                    {task.name}
                  </Typography.Text>
                  <Tag color={STATUS_COLOR[task.status]} style={{ marginInlineEnd: 0 }}>{statusLabel(task)}</Tag>
                </div>
                {task.detail && (
                  <Typography.Text type="secondary" ellipsis style={{ fontSize: 12, display: 'block' }} title={task.detail}>
                    {task.detail}
                  </Typography.Text>
                )}
                {task.status === 'queued' && (task.scheduled || task.ahead !== undefined) && (
                  <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block' }}>
                    {task.scheduled ? t('tasks.scheduled') : task.ahead === 0 ? t('tasks.next') : task.ahead === 1 ? t('tasks.aheadOne') : t('tasks.ahead', { n: task.ahead })}
                  </Typography.Text>
                )}
                {live && task.status !== 'queued' && (
                  <Progress
                    percent={pct}
                    size="small"
                    status="active"
                    format={() => (task.total > 0 ? `${task.done.toLocaleString()} / ${task.total.toLocaleString()}` : t('tasks.waiting'))}
                  />
                )}
                {task.kind === 'run' && task.status === 'finished' && task.counts && (
                  <Typography.Text type={runOutcome(task) === 'ok' ? 'secondary' : 'warning'} style={{ fontSize: 12, display: 'block' }}>
                    {t('tasks.runCounts', { ok: task.counts.succeeded + task.counts.partial, failed: task.counts.failed, total: task.total })}
                  </Typography.Text>
                )}
                {task.status === 'failed' && task.error && (
                  <Typography.Text type="danger" style={{ fontSize: 12, display: 'block' }}>
                    {task.error}
                  </Typography.Text>
                )}
                <Space size={8} style={{ marginTop: 4 }} wrap>
                  {task.download && (
                    <Button size="small" type="link" icon={<DownloadOutlined />} href={task.download} style={{ padding: 0 }}>
                      {t('tasks.download')}
                    </Button>
                  )}
                  {task.cancel && (
                    <Button size="small" type="link" danger style={{ padding: 0 }} onClick={() => cancel(task)}>
                      {t('tasks.cancel')}
                    </Button>
                  )}
                  {task.link && (
                    <Link to={task.link} onClick={() => setOpen(false)} style={{ fontSize: 12 }}>
                      {t('tasks.open')}
                    </Link>
                  )}
                </Space>
              </li>
            )
          })}
        </ul>
      )}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 10, gap: 8 }}>
        <Space size={12} wrap>
          {canRun && (
            <Link to="/queue" onClick={() => setOpen(false)}>
              {t('tasks.viewQueue')}
            </Link>
          )}
          {(canExport || admin) && (
            <Link to={exportsHref} onClick={() => setOpen(false)}>
              {t('tasks.viewExports')}
            </Link>
          )}
        </Space>
        <Button onClick={() => setOpen(false)}>{t('tasks.close')}</Button>
      </div>
    </div>
  )

  return (
    <Popover trigger="click" placement="bottomRight" destroyOnHidden open={open} onOpenChange={setOpen} content={content} styles={{ container: { padding: 14 } }}>
      <Badge count={active.length} size="small" overflowCount={99} offset={[-4, 4]}>
        <Button type="text" shape="circle" icon={<HourglassOutlined />} aria-label={t('tasks.title')} aria-haspopup="dialog" aria-expanded={open} title={t('tasks.title')} />
      </Badge>
    </Popover>
  )
}
