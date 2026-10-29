import { useEffect, useState } from 'react'
import { App, Badge, Button, Empty, Popover, Progress, Space, Tag, Typography, theme } from 'antd'
import { DownloadOutlined, HourglassOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import type { ExportJob } from '../api/types'
import {
  EXPORTS_CHANGED_EVENT,
  exportDownloadUrl,
  fmtBytes,
  isActiveJob,
  jobActions,
  jobProgress,
  notifyExportsChanged,
  sortJobsForTray,
  type ExportsChangedDetail,
} from '../lib/exportJobs'
import { useExportTasks } from '../lib/useExportTasks'
import { STATUS_COLOR } from '../pages/manage/exports/ExportHistory'
import { useExportLabels } from '../pages/manage/exports/labels'

// The top-bar task tray (administrators only), the portal's version of Workspace's hourglass:
// in-progress tasks with progress, finished ones with a download, failed ones with the reason.
// A toast announces a finished task on any page.
export default function ExportTaskTray() {
  const { t } = useTranslation()
  const { token } = theme.useToken()
  const { notification, message } = App.useApp()
  const labels = useExportLabels()
  const [open, setOpen] = useState(false)

  const { active, recent, loadRecent } = useExportTasks({
    enabled: true,
    onFinished: (job) => {
      const name = job.name || labels.source(job.source)
      if (job.status === 'done') {
        const key = `export-done-${job.id}`
        notification.success({
          key,
          title: t('export.toast.done', { name }),
          actions: (
            <Button size="small" type="primary" icon={<DownloadOutlined />} href={exportDownloadUrl(job.id)} onClick={() => notification.destroy(key)}>
              {t('export.tray.download')}
            </Button>
          ),
        })
      } else {
        notification.error({ key: `export-failed-${job.id}`, title: t('export.toast.failed', { name }), description: job.error || undefined })
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
    if (open) void loadRecent()
  }, [open, loadRecent])

  const activeIds = new Set(active.map((j) => j.id))
  const jobs = sortJobsForTray([...active, ...recent.filter((j) => !activeIds.has(j.id))]).slice(0, 10)

  const cancel = async (j: ExportJob) => {
    try {
      await api.post(`/api/admin/exports/${encodeURIComponent(j.id)}/cancel`)
      notifyExportsChanged()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const content = (
    <div style={{ width: 380, maxWidth: '86vw' }}>
      <Typography.Text strong style={{ display: 'block', marginBottom: 4 }}>
        {t('export.tray.title')}
      </Typography.Text>
      {jobs.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('export.tray.empty')} />
      ) : (
        <ul style={{ listStyle: 'none', margin: 0, padding: 0, maxHeight: 360, overflowY: 'auto' }} aria-label={t('export.tray.title')}>
          {jobs.map((j) => {
            const pct = jobProgress(j)
            const a = jobActions(j)
            return (
              <li key={j.id} style={{ padding: '10px 0', borderBottom: `1px solid ${token.colorBorderSecondary}` }}>
                <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                  <Typography.Text ellipsis style={{ flex: 1, minWidth: 0 }} title={j.name}>
                    {j.name || labels.source(j.source)}
                  </Typography.Text>
                  <Tag color={STATUS_COLOR[j.status]} style={{ marginInlineEnd: 0 }}>{labels.status(j.status)}</Tag>
                </div>
                {isActiveJob(j) && (
                  <Progress
                    percent={pct ?? 0}
                    size="small"
                    status="active"
                    format={() => (j.rows_estimate > 0 ? `${j.rows_done.toLocaleString()} / ${j.rows_estimate.toLocaleString()}` : t('export.tray.waiting'))}
                  />
                )}
                {j.status === 'failed' && j.error && (
                  <Typography.Text type="danger" style={{ fontSize: 12, display: 'block' }}>
                    {j.error}
                  </Typography.Text>
                )}
                <Space size={8} style={{ marginTop: 4 }} wrap>
                  {j.status === 'done' && (
                    <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                      {fmtBytes(j.bytes)}
                    </Typography.Text>
                  )}
                  {a.download && (
                    <Button size="small" type="link" icon={<DownloadOutlined />} href={exportDownloadUrl(j.id)} style={{ padding: 0 }}>
                      {t('export.tray.download')}
                    </Button>
                  )}
                  {a.cancel && (
                    <Button size="small" type="link" danger style={{ padding: 0 }} onClick={() => cancel(j)}>
                      {t('export.history.cancel')}
                    </Button>
                  )}
                </Space>
              </li>
            )
          })}
        </ul>
      )}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginTop: 10 }}>
        <Link to="/manage/exports" onClick={() => setOpen(false)}>
          {t('export.tray.viewAll')}
        </Link>
        <Button onClick={() => setOpen(false)}>{t('export.tray.close')}</Button>
      </div>
    </div>
  )

  return (
    <Popover trigger="click" placement="bottomRight" destroyOnHidden open={open} onOpenChange={setOpen} content={content} styles={{ container: { padding: 14 } }}>
      <Badge count={active.length} size="small" overflowCount={99} offset={[-4, 4]}>
        <Button type="text" shape="circle" icon={<HourglassOutlined />} aria-label={t('export.tray.title')} aria-haspopup="dialog" aria-expanded={open} title={t('export.tray.title')} />
      </Badge>
    </Popover>
  )
}
