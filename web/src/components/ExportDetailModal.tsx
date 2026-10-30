import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Descriptions, Modal, Popconfirm, Space, Spin, Typography } from 'antd'
import { CloseCircleOutlined, DownloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api } from '../api/client'
import type { ExportJob, TaskItem } from '../api/types'
import { countConditions } from '../lib/exportFilter'
import { exportBase } from '../lib/exportMode'
import { fmtBytes } from '../lib/exportJobs'
import { fmtMs } from '../lib/exportTime'
import { loadFailure } from '../lib/loadFailure'

export interface ExportDetailModalProps {
  /** The export's row from /api/tasks; null keeps the modal closed. */
  task: TaskItem | null
  onClose: () => void
  /** Cancels the export (the caller owns the request and the refresh). */
  onCancel: (task: TaskItem) => void | Promise<void>
}

// A compact detail for one export, opened from its Tasks drawer row. The row already carries the name, format,
// progress, size, error and the download / cancel URLs; the source, filter and the expiry come from the
// export record, read from the family the row's own link names (an administrator's console or the reader page).
export default function ExportDetailModal({ task, onClose, onCancel }: ExportDetailModalProps) {
  const { t } = useTranslation()
  const [job, setJob] = useState<ExportJob | null>(null)
  const id = task?.id
  const status = task?.status
  const link = task?.link
  const [loadErr, setLoadErr] = useState('')
  const [attempt, setAttempt] = useState(0)
  useEffect(() => {
    setJob(null)
    setLoadErr('')
    if (!id) return
    let live = true
    const base = exportBase(link?.startsWith('/manage') ? 'admin' : 'reader')
    api
      .get<ExportJob>(`${base}/${encodeURIComponent(id)}`)
      .then((j) => live && setJob(j))
      .catch((e) => {
        // A connection loss is not an answer: keep waiting, say nothing red.
        const text = loadFailure(e, t)
        if (live && text !== null) setLoadErr(text)
      })
    return () => {
      live = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, status, link, attempt])
  const retry = useCallback(() => setAttempt((n) => n + 1), [])

  const dash = <Typography.Text type="secondary">—</Typography.Text>
  const time = (ms?: number) => (ms ? fmtMs(ms, '') : dash)
  const error = task?.error || job?.error
  const conditions = job ? countConditions(job.filter) : 0
  const bytes = task?.bytes || job?.bytes || 0
  const active = !!task?.cancel
  return (
    <Modal
      open={task != null}
      onCancel={onClose}
      title={t('tasks.detail.title')}
      width={520}
      zIndex={1010}
      destroyOnHidden
      footer={
        task && (task.download || active) ? (
          <Space>
            {task.download && (
              <Button type="primary" icon={<DownloadOutlined />} href={task.download}>
                {t('tasks.download')}
              </Button>
            )}
            {active && (
              <Popconfirm
                title={t('export.history.cancelConfirm')}
                onConfirm={async () => {
                  await onCancel(task)
                  onClose()
                }}
              >
                <Button danger icon={<CloseCircleOutlined />}>
                  {t('tasks.cancel')}
                </Button>
              </Popconfirm>
            )}
          </Space>
        ) : null
      }
    >
      {task && !job && (
        <div style={{ marginBottom: 12 }}>
          {loadErr ? (
            <Alert
              type="warning"
              showIcon
              title={t('common.loadFailedContent')}
              description={loadErr}
              action={<Button size="small" onClick={retry}>{t('common.retry')}</Button>}
            />
          ) : (
            <Space>
              <Spin size="small" />
              <Typography.Text type="secondary">{t('common.loading')}</Typography.Text>
            </Space>
          )}
        </div>
      )}
      {task && (
        <Descriptions bordered size="small" column={1} styles={{ label: { width: 120 } }}>
          <Descriptions.Item label={t('export.history.name')}>{task.name}</Descriptions.Item>
          <Descriptions.Item label={t('export.history.source')}>{job ? t(`export.source.${job.source}`, job.source) : dash}</Descriptions.Item>
          <Descriptions.Item label={t('export.history.status')}>{t(`export.status.${task.status}`, task.status)}</Descriptions.Item>
          <Descriptions.Item label={t('export.dialog.format')}>{task.format ? t(`export.format.${task.format}`, task.format) : dash}</Descriptions.Item>
          <Descriptions.Item label={t('tasks.detail.filter')}>
            {job ? (conditions > 0 ? t('tasks.detail.conditions', { n: conditions }) : t('tasks.detail.noFilter')) : dash}
          </Descriptions.Item>
          <Descriptions.Item label={t('export.history.rows')}>
            {task.status === 'running' || task.status === 'cancelling'
              ? t('tasks.rows', { done: task.done, total: task.total })
              : task.done.toLocaleString()}
          </Descriptions.Item>
          <Descriptions.Item label={t('export.history.size')}>{bytes ? fmtBytes(bytes) : dash}</Descriptions.Item>
          <Descriptions.Item label={t('export.history.created')}>{time(task.created_at)}</Descriptions.Item>
          <Descriptions.Item label={t('queue.finishedAt')}>{time(task.finished_at)}</Descriptions.Item>
          <Descriptions.Item label={t('export.history.expires')}>{time(job?.status === 'done' ? job.expires_at : 0)}</Descriptions.Item>
          {error ? (
            <Descriptions.Item label={t('batch.col.error')}>
              <Typography.Text type="danger" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                {error}
              </Typography.Text>
            </Descriptions.Item>
          ) : null}
        </Descriptions>
      )}
    </Modal>
  )
}
