import { useCallback, useEffect, useState } from 'react'
import { App, Alert, Button, Card, Popconfirm, Progress, Space, Table, Tag, Tooltip, Typography } from 'antd'
import { DownloadOutlined, ReloadOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../../api/client'
import { useLoadFailure } from '../../../lib/useOnReconnect'
import type { ExportJob, ExportStatus } from '../../../api/types'
import { EXPORTS_CHANGED_EVENT, exportDownloadUrl, fmtBytes, isActiveJob, jobActions, jobProgress, notifyExportsChanged } from '../../../lib/exportJobs'
import { exportBase, type ExportMode } from '../../../lib/exportMode'
import { fmtMs } from '../../../lib/exportTime'
import { useTableView } from '../../../components/TableView'
import { startVisiblePoll } from '../../../lib/visiblePoll'
import type { ExportLabels } from './labels'

export const STATUS_COLOR: Record<ExportStatus, string> = {
  queued: 'default',
  running: 'processing',
  done: 'success',
  failed: 'error',
  canceled: 'warning',
  expired: 'default',
}

// Recent tasks of every administrator (a reader's page lists their own). It refreshes quickly while any task is active, and when the
// tray announces a change.
export default function ExportHistory({ tz, labels, onReuse, mode = 'admin' }: { tz: string; labels: ExportLabels; onReuse: (j: ExportJob) => void; mode?: ExportMode }) {
  const base = exportBase(mode)
  const { t } = useTranslation()
  const { failure, catchLoad, retryKey } = useLoadFailure()
  const { message } = App.useApp()
  const [jobs, setJobs] = useState<ExportJob[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const anyActive = jobs.some(isActiveJob)

  const load = useCallback(async () => {
    let settled = true
    try {
      const r = await api.get<{ jobs: ExportJob[] }>(`${base}?limit=50`)
      setJobs(r?.jobs ?? [])
      setError('')
    } catch (e) {
      // A lost connection keeps the list as it was (or loading, before the first answer).
      const text = failure(e)
      if (text === null) settled = false
      else setError(text)
    } finally {
      if (settled) setLoading(false)
    }
  }, [base, t, failure])

  useEffect(() => {
    void load()
    window.addEventListener(EXPORTS_CHANGED_EVENT, load)
    return () => window.removeEventListener(EXPORTS_CHANGED_EVENT, load)
  }, [load, retryKey])

  useEffect(() => {
    if (!anyActive) return
    return startVisiblePoll(load, 4000, { skipLeading: true })
  }, [anyActive, load])

  const cancel = async (j: ExportJob) => {
    try {
      await api.post(`${base}/${encodeURIComponent(j.id)}/cancel`)
      notifyExportsChanged()
    } catch (e) {
      message.error(errText(e, t))
      void load()
    }
  }

  const columns: ColumnsType<ExportJob> = [
    {
      title: t('export.history.name'),
      key: 'name',
      minWidth: 170,
      ellipsis: true,
      render: (_, j) => <span title={j.name || j.file_name}>{j.name || j.file_name || j.id.slice(0, 8)}</span>,
    },
    { title: t('export.history.source'), key: 'source', width: 140, minWidth: 90, render: (_, j) => labels.source(j.source) },
    ...(mode === 'admin' ? [{ title: t('export.history.requester'), key: 'requested_by', dataIndex: 'requested_by', width: 130, minWidth: 90, ellipsis: true }] : []),
    { title: t('export.history.created'), key: 'created', width: 170, minWidth: 110, render: (_, j) => fmtMs(j.created_at, tz) },
    {
      title: t('export.history.status'),
      key: 'status',
      width: 170,
      minWidth: 100,
      render: (_, j) => {
        const pct = jobProgress(j)
        return (
          <Space orientation="vertical" size={2} style={{ width: '100%' }}>
            <Tooltip title={j.status === 'failed' && j.error ? j.error : undefined}>
              <Tag color={STATUS_COLOR[j.status]} style={{ marginInlineEnd: 0 }}>{labels.status(j.status)}</Tag>
            </Tooltip>
            {j.status === 'running' && pct !== null && <Progress percent={pct} size="small" showInfo={false} />}
            {j.status === 'failed' && j.error && (
              <Typography.Text type="danger" style={{ fontSize: 12 }}>{j.error}</Typography.Text>
            )}
          </Space>
        )
      },
    },
    {
      title: t('export.history.rows'),
      key: 'rows',
      width: 110,
      minWidth: 80,
      align: 'end',
      render: (_, j) => (j.status === 'running' ? `${j.rows_done.toLocaleString()} / ${j.rows_estimate.toLocaleString()}` : (j.status === 'queued' ? j.rows_estimate : j.rows_done).toLocaleString()),
    },
    { title: t('export.history.size'), key: 'size', width: 90, minWidth: 70, align: 'end', render: (_, j) => (j.bytes ? fmtBytes(j.bytes) : '—') },
    { title: t('export.history.expires'), key: 'expires', width: 170, minWidth: 110, render: (_, j) => (j.status === 'done' && j.expires_at ? fmtMs(j.expires_at, tz) : '—') },
    {
      title: '',
      key: 'actions',
      width: 150,
      minWidth: 120,
      fixed: 'right',
      render: (_, j) => {
        const a = jobActions(j)
        return (
          <Space size={0}>
            {a.download && (
              <Button type="link" size="small" icon={<DownloadOutlined />} href={exportDownloadUrl(j.id, mode)}>
                {t('export.history.download')}
              </Button>
            )}
            {a.cancel && (
              <Popconfirm title={t('export.history.cancelConfirm')} onConfirm={() => cancel(j)}>
                <Button type="link" size="small" danger>
                  {t('export.history.cancel')}
                </Button>
              </Popconfirm>
            )}
            <Button type="link" size="small" onClick={() => onReuse(j)}>
              {t('export.history.reuse')}
            </Button>
          </Space>
        )
      },
    },
  ]
  const tableView = useTableView(mode === 'admin' ? 'exports' : 'my-exports', columns, ['name', 'actions'])

  return (
    <Card
      title={t('export.history.title')}
      extra={
        <Space size={4}>
          {tableView.controls}
          <Button type="text" icon={<ReloadOutlined />} aria-label={t('common.refresh')} title={t('common.refresh')} onClick={() => void load()} />
        </Space>
      }
    >
      {error && <Alert type="error" showIcon title={error} style={{ marginBottom: 12 }} />}
      <Table<ExportJob>
        size="small"
        rowKey="id"
        loading={loading}
        columns={tableView.columns}
        className={tableView.className}
        tableLayout="fixed"
        dataSource={jobs}
        scroll={tableView.scroll}
        pagination={{ pageSize: 10, size: 'small', hideOnSinglePage: true }}
        locale={{ emptyText: t('export.history.empty') }}
      />
    </Card>
  )
}
