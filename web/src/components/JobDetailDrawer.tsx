import { useEffect, useRef, useState, type Key, type ReactNode } from 'react'
import { Alert, App, Button, Descriptions, Drawer, Grid, Modal, Popconfirm, Space, Table, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { ArrowLeftOutlined, ArrowRightOutlined, CloseCircleOutlined, InfoCircleOutlined, SyncOutlined } from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../api/client'
import { useAuth } from '../auth'
import type { BatchItem, BatchJobDetail, BatchTarget } from '../api/types'
import {
  clampText,
  executionTags,
  fmtInputs,
  INPUT_TOTAL_MAX,
  inputPairs,
  InputsPreview,
  isTerminal,
  statusTag,
  TOOLTIP_STYLES,
} from '../lib/batchUi'
import { formatElapsed } from '../lib/taskUi'
import { loadFailure } from '../lib/loadFailure'
import { startVisiblePoll } from '../lib/visiblePoll'
import LoadGate from './LoadGate'

// itemActive reports whether a run (row) can still be cancelled: it hasn't reached a
// terminal state (succeeded / partial / failed / cancelled).
const itemActive = (s: string) => s === 'queued' || s === 'running'

const symbolOf = (it: BatchItem) => {
  try {
    const o = JSON.parse(it.inputs) as Record<string, string>
    return o.symbol || o.code || ''
  } catch {
    return ''
  }
}

export interface JobDetailDrawerProps {
  jobId: number | null
  onClose: () => void
  /** When given, the footer offers "Open in queue" and calls this on click (the host navigates). */
  onOpenInQueue?: () => void
  /** Raises the drawer above another one it opens over. */
  zIndex?: number
}

// A run's detail: one run reads as a single record (inputs, times, outcome, actions); a batch job lists its
// rows, which can be cancelled one by one (per-row close) or several at once (checkbox multi-select), by the
// job's owner or an admin. Shared by the queue page (?job=) and the in-place detail over any page.
export default function JobDetailDrawer({ jobId, onClose, onOpenInQueue, zIndex }: JobDetailDrawerProps) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { admin, user } = useAuth()
  const fullWidth = !Grid.useBreakpoint().md // phone: the 680px drawer would overflow the screen
  const [detail, setDetail] = useState<BatchJobDetail | null>(null)
  const [selected, setSelected] = useState<Key[]>([])
  const [detailItem, setDetailItem] = useState<BatchItem | null>(null)
  const [targets, setTargets] = useState<BatchTarget[]>([])
  // The server answers 404 for a job that does not exist or is not the reader's; a deep link can name either.
  // The same answer for a job that is not there and one the caller may not open (403): either way polling
  // can never succeed, so it stops.
  const [notFound, setNotFound] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  const stopPoll = useRef<(() => void) | null>(null)
  // The job this drawer is showing now; an answer for any other job (a request that was in flight when the
  // drawer moved on) is dropped before it can touch state or stop the new job's poll.
  const current = useRef<number | null>(null)
  const open = jobId != null
  const load = () =>
    jobId == null
      ? Promise.resolve()
      : api
          .get<BatchJobDetail>(`/api/admin/batch/jobs/${jobId}`)
          .then((d) => {
            if (current.current !== jobId) return
            setNotFound(false)
            setLoadErr('')
            setDetail(d)
          })
          .catch((e) => {
            if (current.current !== jobId) return
            const status = (e as { status?: number })?.status
            if (status === 404 || status === 403) {
              setNotFound(true)
              stopPoll.current?.()
              return
            }
            // A connection loss is not an answer: keep waiting, say nothing red.
            const text = loadFailure(e, t)
            if (text !== null) setLoadErr(text)
          })
  useEffect(() => {
    if (jobId == null) return
    setDetail(null)
    setNotFound(false)
    setLoadErr('')
    setSelected([])
    current.current = jobId
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r?.targets || [])).catch(() => {})
    const stop = startVisiblePoll(load, 2500)
    stopPoll.current = stop
    return () => {
      stop()
      stopPoll.current = null
      current.current = null
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId])

  const canCancel = detail != null && (admin || detail.job.created_by === user)
  const cancelRows = async (ids: Key[]) => {
    if (jobId == null || ids.length === 0) return
    try {
      await api.post(`/api/admin/batch/jobs/${jobId}/items/cancel`, { item_ids: ids.map(Number) })
      message.success(t('batch.msg.rowsCancelled', { n: ids.length }))
      setSelected([])
      await load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const cancelJob = async () => {
    if (jobId == null) return
    try {
      await api.post(`/api/admin/batch/jobs/${jobId}/cancel`)
      message.success(t('batch.msg.cancelRequested'))
      await load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }
  // A row that has a Dify handle can be reconciled: fetch its true outcome from Dify without
  // re-running it. Rescues a run wrongly marked failed by a short timeout / dropped stream.
  const hasHandle = (it: BatchItem) => !!(it.run_id || it.conversation_id)
  const canReconcile = (it: BatchItem) => admin && hasHandle(it) && !itemActive(it.status) && it.status !== 'succeeded'
  const reconcile = async (it: BatchItem) => {
    try {
      const r = await api.post<{ status: string; note?: string }>(`/api/admin/batch/items/${it.id}/reconcile`)
      message.success(r.note ? t('queue.reconcileStillRunning') : t('queue.reconcileDone', { status: t(`batch.status.${r.status}`) }))
      await load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }
  const idCell = (v: string) =>
    v ? (
      <Typography.Text copyable style={{ fontSize: 12 }}>
        {v}
      </Typography.Text>
    ) : (
      <Typography.Text type="secondary">—</Typography.Text>
    )

  // The footer's button on a single run cancels the JOB, like the Tasks row's quick cancel, so it ends as
  // cancelled; cancelling just its row would leave a "finished" job and raise a "run finished" notification.
  const cancelButton = (it: BatchItem, labelled: boolean) =>
    labelled ? (
      <Popconfirm title={t('queue.cancelConfirm')} onConfirm={cancelJob}>
        <Button danger icon={<CloseCircleOutlined />}>
          {t('tasks.cancel')}
        </Button>
      </Popconfirm>
    ) : (
      <Popconfirm title={t('queue.cancelRowConfirm')} onConfirm={() => cancelRows([it.id])}>
        <Button size="small" type="text" danger icon={<CloseCircleOutlined />} title={t('queue.cancelRow')} />
      </Popconfirm>
    )

  const cols: ColumnsType<BatchItem> = [
    { title: t('batch.col.row'), dataIndex: 'row_index', width: 48 },
    { title: t('batch.col.inputs'), render: (_: unknown, it) => <InputsPreview inputs={it.inputs} rows={3} secondary={false} /> },
    { title: t('batch.col.status'), dataIndex: 'status', width: 92, render: (s: string) => statusTag(t, s) },
    {
      title: t('batch.col.error'),
      render: (_: unknown, it) =>
        it.error ? (
          // Same bound as the inputs preview: a raw provider error can run to thousands of
          // characters, and an overlay that tall gets clipped by the viewport and flickers.
          // The full text is in this row's detail modal.
          <Typography.Paragraph
            type="danger"
            ellipsis={{ rows: 3, tooltip: { title: clampText(it.error, INPUT_TOTAL_MAX), styles: TOOLTIP_STYLES } }}
            style={{ fontSize: 12, marginBottom: 0 }}
          >
            {it.error}
          </Typography.Paragraph>
        ) : it.status === 'succeeded' && symbolOf(it) ? (
          <Link to={`/stock/${encodeURIComponent(symbolOf(it))}`}>{t('queue.viewReport')}</Link>
        ) : (
          ''
        ),
    },
    {
      title: '',
      width: 96,
      render: (_: unknown, it) => (
        <Space size={0}>
          <Button size="small" type="text" icon={<InfoCircleOutlined />} title={t('queue.itemDetails')} onClick={() => setDetailItem(it)} />
          {canReconcile(it) ? (
            <Popconfirm title={t('queue.reconcileConfirm')} onConfirm={() => reconcile(it)}>
              <Button size="small" type="text" icon={<SyncOutlined />} title={t('queue.reconcile')} />
            </Popconfirm>
          ) : null}
          {canCancel && itemActive(it.status) ? cancelButton(it, false) : null}
        </Space>
      ),
    },
  ]
  const rowSelection = canCancel
    ? {
        selectedRowKeys: selected,
        onChange: setSelected,
        getCheckboxProps: (it: BatchItem) => ({ disabled: !itemActive(it.status) }),
      }
    : undefined

  const single = detail != null && detail.items.length === 1 ? detail.items[0] : null

  // One run: a record, not a table.
  const singleRun = (it: BatchItem, d: BatchJobDetail) => {
    const j = d.job
    const dash = <Typography.Text type="secondary">—</Typography.Text>
    const target = targets.find((tg) => tg.id === j.target_id)?.name || `#${j.target_id}`
    const pairs = inputPairs(it.inputs)
    const started = it.started_at || j.started_at
    const finished = it.finished_at || j.finished_at
    // Durations are measured between instants: the strings above are zoneless, server-zone wall-clock.
    const startedMs = it.started_at_ms || j.started_at_ms || 0
    const finishedMs = it.finished_at_ms || j.finished_at_ms || 0
    let queueLine: ReactNode = null
    if (j.status === 'queued') {
      queueLine = j.scheduled
        ? t('tasks.scheduledAt', { at: j.run_at })
        : j.ahead === undefined
          ? t('tasks.runStatus.queued')
          : j.ahead === 0
            ? t('tasks.next')
            : t('tasks.queuedAhead', { n: j.ahead })
    }
    const end = isTerminal(j.status) ? finishedMs : Date.now()
    const duration = startedMs && end >= startedMs ? formatElapsed(end - startedMs, t) : ''
    return (
      <>
        <Space wrap style={{ marginBottom: 12 }}>
          {statusTag(t, it.status)}
          {executionTags(t, j)}
          {queueLine && <Typography.Text type="secondary">{queueLine}</Typography.Text>}
        </Space>
        {d.job.status === 'running' && !d.running_in_process && (
          <Alert type="info" showIcon style={{ marginBottom: 12 }} title={t('queue.notInProcess')} description={t('queue.notInProcessHint')} />
        )}
        <Descriptions bordered size="small" column={1} styles={{ label: { width: 120 } }}>
          <Descriptions.Item label={t('queue.detail.workflow')}>{target}</Descriptions.Item>
          <Descriptions.Item label={t('batch.col.inputs')}>
            {pairs.length === 0 ? (
              dash
            ) : (
              <div style={{ display: 'grid', gridTemplateColumns: 'max-content 1fr', gap: '4px 12px' }}>
                {pairs.map(([k, v], i) => (
                  <div key={`${k}-${i}`} style={{ display: 'contents' }}>
                    <Typography.Text type="secondary">{k}</Typography.Text>
                    <Typography.Paragraph
                      ellipsis={{ rows: 4, tooltip: { title: clampText(v, INPUT_TOTAL_MAX), styles: TOOLTIP_STYLES } }}
                      style={{ marginBottom: 0, wordBreak: 'break-word' }}
                    >
                      {v}
                    </Typography.Paragraph>
                  </div>
                ))}
              </div>
            )}
          </Descriptions.Item>
          <Descriptions.Item label={t('queue.detail.submitted')}>{j.created_at || dash}</Descriptions.Item>
          <Descriptions.Item label={t('queue.startedAt')}>{started || dash}</Descriptions.Item>
          <Descriptions.Item label={t('queue.finishedAt')}>{finished || dash}</Descriptions.Item>
          <Descriptions.Item label={t('queue.detail.duration')}>{duration || dash}</Descriptions.Item>
          {it.error ? (
            <Descriptions.Item label={t('batch.col.error')}>
              <Typography.Text type="danger" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
                {it.error}
              </Typography.Text>
            </Descriptions.Item>
          ) : null}
          {it.status === 'succeeded' && symbolOf(it) ? (
            <Descriptions.Item label={t('queue.viewReport')}>
              <Link to={`/stock/${encodeURIComponent(symbolOf(it))}`}>{t('queue.viewReport')}</Link>
            </Descriptions.Item>
          ) : null}
        </Descriptions>
      </>
    )
  }

  const jobAction =
    detail && !single && canCancel && !isTerminal(detail.job.status) && detail.job.status !== 'cancelling' ? (
      <Popconfirm title={t('queue.cancelConfirm')} onConfirm={cancelJob}>
        <Button danger icon={<CloseCircleOutlined />}>
          {t('queue.cancelJob')}
        </Button>
      </Popconfirm>
    ) : null
  const rowActions = single && (
    <Space wrap>
      <Button icon={<InfoCircleOutlined />} onClick={() => setDetailItem(single)}>
        {t('queue.itemDetails')}
      </Button>
      {canReconcile(single) && (
        <Popconfirm title={t('queue.reconcileConfirm')} onConfirm={() => reconcile(single)}>
          <Button icon={<SyncOutlined />}>{t('queue.reconcile')}</Button>
        </Popconfirm>
      )}
      {canCancel && itemActive(single.status) && detail?.job.status !== 'cancelling' && cancelButton(single, true)}
    </Space>
  )
  const footer =
    onOpenInQueue || rowActions || jobAction ? (
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
        {onOpenInQueue ? (
          <Button type="link" icon={<ArrowRightOutlined />} iconPlacement="end" onClick={onOpenInQueue} style={{ padding: 0 }}>
            {t('queue.openInQueue')}
          </Button>
        ) : (
          <span />
        )}
        {rowActions}
        {jobAction}
      </div>
    ) : undefined

  return (
    <Drawer
      title={detail ? t('batch.jobTitle', { id: detail.job.id }) : t('batch.jobDetail')}
      size={fullWidth ? '100%' : 680}
      open={open}
      onClose={onClose}
      destroyOnHidden
      zIndex={zIndex}
      closeIcon={fullWidth ? <ArrowLeftOutlined aria-label={t('common.back')} /> : undefined}
      footer={detail ? footer : undefined}
      extra={
        !single && selected.length > 0 ? (
          <Popconfirm title={t('queue.cancelRowsConfirm', { n: selected.length })} onConfirm={() => cancelRows(selected)}>
            <Button danger size="small" icon={<CloseCircleOutlined />}>
              {t('queue.cancelSelected', { n: selected.length })}
            </Button>
          </Popconfirm>
        ) : null
      }
    >
      {notFound && <Alert type="warning" showIcon title={t('batch.jobNotFound')} />}
      {!detail && !notFound && (
        <LoadGate loading={!loadErr} error={loadErr} onRetry={() => { setLoadErr(''); void load() }} minHeight={200} title={t('common.loadFailedContent')}>
          {null}
        </LoadGate>
      )}
      {detail && single && singleRun(single, detail)}
      {detail && !single && (
        <>
          <Space wrap style={{ marginBottom: 12 }}>{executionTags(t, detail.job)}</Space>
          {/* A job the queue calls "running" is not necessarily running HERE: after a restart the row
              survives while the goroutine driving it does not, and the portal reconciles it by polling
              rather than by executing. The one signal that tells an admin whether a stuck job is stuck
              in THIS process or merely being watched. Only said when it is worth saying. */}
          {detail.job.status === 'running' && !detail.running_in_process && (
            <Alert type="info" showIcon style={{ marginBottom: 12 }} title={t('queue.notInProcess')} description={t('queue.notInProcessHint')} />
          )}
          <Table
            rowKey="id"
            size="small"
            rowSelection={rowSelection}
            dataSource={detail.items}
            columns={cols}
            pagination={{ pageSize: 20, size: 'small' }}
            scroll={{ x: 560 }}
          />
        </>
      )}
      <Modal open={detailItem != null} title={t('queue.itemDetails')} footer={null} width={560} onCancel={() => setDetailItem(null)}>
        {detailItem && (
          <Descriptions bordered size="small" column={1}>
            <Descriptions.Item label={t('batch.col.row')}>{detailItem.row_index}</Descriptions.Item>
            <Descriptions.Item label={t('batch.col.status')}>{statusTag(t, detailItem.status)}</Descriptions.Item>
            <Descriptions.Item label={t('queue.attempts')}>{detailItem.attempts}</Descriptions.Item>
            <Descriptions.Item label="run_id">{idCell(detailItem.run_id)}</Descriptions.Item>
            <Descriptions.Item label="conversation_id">{idCell(detailItem.conversation_id)}</Descriptions.Item>
            <Descriptions.Item label="task_id">{idCell(detailItem.task_id)}</Descriptions.Item>
            <Descriptions.Item label={t('queue.startedAt')}>{detailItem.started_at || '—'}</Descriptions.Item>
            <Descriptions.Item label={t('queue.finishedAt')}>{detailItem.finished_at || '—'}</Descriptions.Item>
            <Descriptions.Item label={t('batch.col.inputs')}>{fmtInputs(detailItem.inputs)}</Descriptions.Item>
            {detailItem.error ? (
              <Descriptions.Item label={t('batch.col.error')}>
                <Typography.Text type="danger" style={{ fontSize: 12 }}>
                  {detailItem.error}
                </Typography.Text>
              </Descriptions.Item>
            ) : null}
          </Descriptions>
        )}
      </Modal>
    </Drawer>
  )
}
