import { useEffect, useMemo, useRef, useState, type Key } from 'react'
import {
  App,
  Button,
  Card,
  DatePicker,
  Empty,
  Grid,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Progress,
  Segmented,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  ClockCircleOutlined,
  DeleteOutlined,
  ExportOutlined,
  EyeOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  StopOutlined,
} from '@ant-design/icons'
import { useNavigate, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { Dayjs } from 'dayjs'
import { api, errText } from '../api/client'
import { exportLink, runsFilterToTree } from '../lib/exportFilter'
import { useAuth } from '../auth'
import type { BatchJob, BatchQueueSummary, BatchTarget } from '../api/types'
import {
  BASE_MAX,
  executionTags,
  queueStatusTag,
  InputsPreview,
  isTerminal,
  isUrgent,
  jobProgressPresentation,
  priorityNum,
  priorityTag,
} from '../lib/batchUi'
import { UNCHANGED, forgetTags, getIfChanged, type TagHolder } from '../lib/conditionalGet'
import { watchQueue } from '../lib/queueWatch'
import { useQueueScope, withQueueScope, type QueueScope } from '../lib/queueScope'
import { startVisiblePoll } from '../lib/visiblePoll'
import LoadGate from './LoadGate'
import JobDetailDrawer from './JobDetailDrawer'
import { loadFailure } from '../lib/loadFailure'
import { useTableView } from './TableView'

const todayStr = () => new Date().toISOString().slice(0, 10)

// The full run queue: filters + jobs table + all queue actions + detail drawer +
// reschedule modal. Shared by the Run/queue page (with stats) and the batch console
// (embedded under the run form) so both show the same complete view. Queue mutations
// are gated to match the server: a non-admin sees only "cancel" and only on their own
// runs; admins see cancel/terminate/retry/reschedule/reprioritize/delete on any job.
// One scope's last answer: null until its first jobs / summary call has landed.
type ScopeView = { jobs: BatchJob[] | null; total: number; summary: BatchQueueSummary | null; err: string }

export default function QueueTable({ showStats = false }: { showStats?: boolean }) {
  const { t } = useTranslation()
  const { message } = App.useApp()
  const { admin, user } = useAuth()
  // Whose runs this view covers; only an administrator gets to choose (ADR 0047).
  const [scope, setScope] = useQueueScope(admin)
  const mobile = !Grid.useBreakpoint().md
  const navigate = useNavigate()
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [auto, setAuto] = useState(true)
  const [fStatus, setFStatus] = useState<string>('')
  const [fUser, setFUser] = useState<string>('')
  const [fTarget, setFTarget] = useState<number | undefined>()
  const [search, setSearch] = useState('')
  // The open job lives in the url (?job=id) so a link, a reload and the back button all land on it.
  const [params, setParams] = useSearchParams()
  const jobParam = Number(params.get('job'))
  const detailId = Number.isInteger(jobParam) && jobParam > 0 ? jobParam : null
  const setDetailId = (id: number | null) =>
    setParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (id === null) next.delete('job')
        else next.set('job', String(id))
        return next
      },
      { replace: id === null },
    )
  const [reschedId, setReschedId] = useState<number | null>(null)
  const [reschedAt, setReschedAt] = useState<Dayjs | null>(null)
  const [selectedJobs, setSelectedJobs] = useState<Key[]>([])
  // Each scope keeps its own last answer, so switching back to one shows it at once and switching
  // to one not yet asked shows the spinner — never the other scope's runs under this one's label.
  // A scope with no entry has not answered: `[]` is where state starts, not an empty queue, and
  // "Queue is empty" is a claim about the server.
  const [views, setViews] = useState<Partial<Record<QueueScope, ScopeView>>>({})
  const view = views[scope]
  const jobs = useMemo(() => view?.jobs ?? [], [view])
  const total = view?.total ?? 0
  const summary = view?.summary ?? null
  const loaded = !!view?.jobs
  const loadErr = view?.err ?? ''
  const [targetsLoaded, setTargetsLoaded] = useState(false) // the workflow filter's option list
  // This view's own revalidation tags: the header badge and the Tasks drawer poll the same summary
  // URL, and a 304 means "unchanged since the tag YOU sent", so shared tags would let them answer
  // "nothing changed" about a summary this view never had.
  const [tagHolder] = useState<TagHolder>(() => ({}))
  // Also when the viewer's admin role changes: the plain URL then means another scope (everyone's
  // for an administrator, their own otherwise), so a tag kept across that would answer for the wrong one.
  useEffect(() => () => forgetTags(undefined, tagHolder), [tagHolder, admin])
  // The jobs URL whose answer each scope's view holds. The search box changes that URL within a
  // scope, so clearing it back to a URL tagged before the current rows would otherwise be answered
  // "nothing changed" about rows this view no longer has. A ref, not state: `load` is captured by
  // a poll started in an effect.
  const heldJobsUrl = useRef<Partial<Record<QueueScope, string>>>({})

  const load = async () => {
    // Bounded poll: the server returns every active job + the most recent terminal jobs (not the whole
    // history) plus the true total, so the 3s poll stays cheap on a large finished-job backlog.
    // The search goes with it: since the list carries only a PREVIEW of each job's inputs, filtering
    // here would stop matching anything past the first line of a prompt. The server still sees them
    // whole.
    // Conditional: a queue in which nothing happened answers 304, and keeping the objects we
    // already have is what stops a 3-second poll from re-rendering the table for no reason.
    const sc = scope
    const patch = (p: Partial<ScopeView>) => setViews((v) => ({ ...v, [sc]: { jobs: null, total: 0, summary: null, err: '', ...v[sc], ...p } }))
    const jobsUrl = withQueueScope(`/api/admin/batch/jobs?limit=300${search.trim() ? `&q=${encodeURIComponent(search.trim())}` : ''}`, sc, admin)
    const summaryUrl = withQueueScope('/api/admin/batch/queue', sc, admin)
    // Drop the tag for rows we are not holding, so this request has to come back as an answer.
    if (heldJobsUrl.current[sc] !== jobsUrl) forgetTags(jobsUrl, tagHolder)
    const jobsRequest = getIfChanged<{ jobs: BatchJob[]; total?: number }>(jobsUrl, tagHolder).then((r) => {
      if (r === UNCHANGED) return
      patch({ jobs: r.jobs || [], total: r.total ?? (r.jobs || []).length, err: '' })
      heldJobsUrl.current[sc] = jobsUrl
    })
    const summaryRequest = getIfChanged<BatchQueueSummary>(summaryUrl, tagHolder)
      .then((r) => {
        if (r !== UNCHANGED) patch({ summary: r })
      })
      .catch(() => {})
    try {
      await jobsRequest
    } catch (e) {
      // Only a scope's first load has anything to report. Once rows are on screen a failed poll is
      // not news — they are still the last thing the server said, and swapping them for an
      // error page would be the worse answer. The render below reads this only while !loaded.
      // A lost connection is not news either: the table keeps its spinner and the next poll (which
      // also runs when the connection returns) asks again.
      const text = loadFailure(e, t)
      if (text !== null) patch({ err: text })
    }
    await summaryRequest
  }
  useEffect(() => {
    api
      .get<{ targets: BatchTarget[] }>('/api/admin/batch/targets')
      .then((r) => setTargets(r.targets || []))
      .catch(() => {})
      .finally(() => setTargetsLoaded(true))
    // While this view is mounted the header badge stands down: it would be asking, four times
    // slower, for what is on screen in full.
    return watchQueue()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  useEffect(() => {
    if (!auto) return
    return startVisiblePoll(load, 3000)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auto, search, scope])

  // Typing must not fire a request per keystroke, and with auto-refresh off nothing else would
  // refetch at all, so a paused queue still answers the search box.
  useEffect(() => {
    const timer = window.setTimeout(() => void load(), 300)
    return () => window.clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search, scope])

  const targetName = (id: number) => targets.find((tg) => tg.id === id)?.name || `#${id}`
  const submitters = useMemo(() => [...new Set(jobs.map((j) => j.created_by).filter(Boolean))], [jobs])
  // Prefer the server-side count (exact even though the job list is paginated); fall back to counting
  // the loaded page for an older server that doesn't send done_today — and only once that page is
  // actually loaded, since counting [] would answer "0 today" before anyone had asked the server.
  const doneToday = useMemo(
    () => summary?.done_today ?? (loaded ? jobs.filter((j) => isTerminal(j.status) && (j.finished_at || '').startsWith(todayStr())).length : null),
    [summary, jobs, loaded],
  )
  const canCancel = (j: BatchJob) => admin || j.created_by === user // matches the server ownership check

  // Status / submitter / workflow filter the fetched set — they read fields the list still carries
  // in full. The free-text search does not appear here: it is applied by the server (see load).
  const rows = useMemo(() => {
    return jobs.filter((j) => {
      if (fStatus === 'scheduled' ? !j.scheduled : fStatus && j.status !== fStatus) return false
      if (fUser && j.created_by !== fUser) return false
      if (fTarget && j.target_id !== fTarget) return false
      return true
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobs, fStatus, fUser, fTarget, search, targets])

  const act = async (fn: () => Promise<unknown>, okMsg?: string) => {
    try {
      await fn()
      if (okMsg) message.success(okMsg)
      load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }
  const cancel = (id: number) => act(() => api.post(`/api/admin/batch/jobs/${id}/cancel`), t('batch.msg.cancelRequested'))
  // Cancel several whole jobs at once (multi-select). Each goes through the same per-job
  // cancel endpoint (which the server authorizes per job), so partial permission is fine.
  const cancelJobs = (ids: Key[]) =>
    act(async () => {
      await Promise.allSettled(ids.map((id) => api.post(`/api/admin/batch/jobs/${Number(id)}/cancel`)))
      setSelectedJobs([])
    }, t('batch.msg.cancelRequested'))
  const del = (id: number) => act(() => api.del(`/api/admin/batch/jobs/${id}`), t('queue.deleted'))
  const retry = (id: number) => act(() => api.post(`/api/admin/batch/jobs/${id}/retry`, { statuses: ['failed'] }), t('queue.retried'))
  const runNow = (id: number) => act(() => api.post(`/api/admin/batch/jobs/${id}/schedule`, { run_at: '' }), t('queue.runningNow'))
  const reprioritize = (id: number, p: string) => act(() => api.post(`/api/admin/batch/jobs/${id}/priority`, { priority: p }))
  const saveReschedule = () =>
    act(
      () => api.post(`/api/admin/batch/jobs/${reschedId}/schedule`, { run_at: reschedAt ? reschedAt.format('YYYY-MM-DD HH:mm:ss') : '' }),
      t('queue.rescheduled'),
    ).then(() => {
      setReschedId(null)
      setReschedAt(null)
    })
  const clearFinished = async () => {
    try {
      const r = await api.post<{ n: number }>('/api/admin/batch/jobs/clear-finished')
      message.success(t('queue.clearedN', { n: r.n }))
      load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const cols: ColumnsType<BatchJob> = [
    {
      key: 'workflow', minWidth: 150,
      title: t('queue.colWorkflow'),
      render: (_: unknown, j) => (
        <div>
          <div style={{ fontSize: 13 }}>{targetName(j.target_id)}</div>
          <InputsPreview inputs={j.inputs} />
          <Space size={[6, 6]} wrap>{executionTags(t, j)}</Space>
        </div>
      ),
    },
    { key: 'owner', title: t('batch.col.createdBy'), dataIndex: 'created_by', width: 110, minWidth: 92, responsive: ['md'] },
    { key: 'submitted', title: t('queue.colSubmitted'), dataIndex: 'created_at', width: 160, minWidth: 116, responsive: ['lg'], render: (s: string) => <span style={{ fontSize: 12 }}>{s}</span> },
    {
      key: 'status', minWidth: 88,
      title: t('batch.col.status'),
      width: 108,
      render: (_: unknown, j) => queueStatusTag(t, j),
    },
    {
      // A queued non-urgent job gets an inline base-priority editor (admin only); urgent
      // and non-queued jobs, and non-admins, show a static tag (ADR 0008).
      title: t('batch.priorityLabel'),
      key: 'priority', minWidth: 88,
      width: 116,
      responsive: ['md'],
      render: (_: unknown, j) =>
        admin && j.status === 'queued' && !j.scheduled && !isUrgent(j.priority) ? (
          <InputNumber
            size="small"
            min={0}
            max={BASE_MAX}
            style={{ width: 76 }}
            value={priorityNum(j.priority)}
            onChange={(v) => reprioritize(j.id, String(v ?? 50))}
          />
        ) : (
          priorityTag(t, j.priority)
        ),
    },
    {
      title: t('queue.colProgress'),
      key: 'progress', minWidth: 130,
      width: 220,
      render: (_: unknown, j) => {
        if (j.window_blocked) return <Typography.Text type="secondary">{t('queue.windowWaitHint')}</Typography.Text>
        if (j.scheduled)
          return (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              <ClockCircleOutlined /> {j.run_at}
            </Typography.Text>
          )
        if (j.status === 'queued') return <Tag>{j.ahead ? t('batch.aheadN', { n: j.ahead }) : t('batch.aheadNext')}</Tag>
        const cancelled = j.cancelled || 0
        const progress = jobProgressPresentation(j)
        return (
          <div style={{ maxWidth: 200 }}>
            <Progress
              percent={progress.loading ? 100 : progress.percent}
              size="small"
              status={progress.status}
              strokeColor={progress.strokeColor}
              // Indeterminate "loading" bar: animate a full bar but hide the "100%" —
              // the run is at 0/1, not done, so the number would be a lie.
              showInfo={progress.showInfo}
            />
            <Typography.Text type="secondary" style={{ fontSize: 12, display: 'block', marginTop: -2 }}>
              {t('batch.progressText', { done: progress.done, total: j.total, ok: j.succeeded, fail: j.failed, partial: j.partial })}
              {cancelled > 0 ? ` · ${t('batch.cancelledN', { n: cancelled })}` : ''}
            </Typography.Text>
          </div>
        )
      },
    },
    {
      title: t('batch.col.actions'),
      key: 'actions', minWidth: 100,
      width: 150,
      render: (_: unknown, j) => (
        <Space size={[2, 2]} wrap>
          <Button size="small" type="text" icon={<EyeOutlined />} onClick={() => setDetailId(j.id)} title={t('batch.view')} />
          {admin && j.scheduled && <Button size="small" type="text" icon={<PlayCircleOutlined />} onClick={() => runNow(j.id)} title={t('queue.runNow')} />}
          {admin && j.status === 'queued' && (
            <Button size="small" type="text" icon={<ClockCircleOutlined />} onClick={() => { setReschedId(j.id); setReschedAt(null) }} title={t('queue.reschedule')} />
          )}
          {admin && isTerminal(j.status) && j.failed > 0 && (
            <Button size="small" type="text" icon={<ReloadOutlined />} onClick={() => retry(j.id)} title={t('batch.retryFailed', { n: j.failed })} />
          )}
          {!isTerminal(j.status)
            ? canCancel(j) && (
                <Popconfirm title={t('queue.cancelConfirm')} onConfirm={() => cancel(j.id)}>
                  <Button size="small" type="text" danger icon={<StopOutlined />} title={t('common.cancel')} />
                </Popconfirm>
              )
            : admin && (
                <Popconfirm title={t('queue.deleteConfirm')} onConfirm={() => del(j.id)}>
                  <Button size="small" type="text" danger icon={<DeleteOutlined />} title={t('common.delete')} />
                </Popconfirm>
              )}
        </Space>
      ),
    },
  ]
  const tableView = useTableView('queue', cols, ['workflow', 'status', 'actions'])

  // null = the summary has not landed yet. A dash, not a 0: five tiles reading zero are a
  // statement about the queue, and rendering it before the server has answered makes the page
  // look like it has finished loading on an idle queue.
  const stat = (label: string, value: number | null) => (
    <Card size="small" styles={{ body: { padding: mobile ? '8px 12px' : undefined } }}>
      <div style={{ fontSize: mobile ? 20 : 24, fontWeight: 500, lineHeight: 1.2 }}>{value ?? <Typography.Text type="secondary">—</Typography.Text>}</div>
      <Typography.Text type="secondary" style={{ fontSize: mobile ? 12 : 13 }}>
        {label}
      </Typography.Text>
    </Card>
  )

  // Budget / my-priority / clear-finished / auto-refresh. In the card header (extra) on
  // desktop; on a phone that corner is too cramped, so they move to a full-width body row.
  const queueControls = (
    <Space wrap>
      {admin && (
        <Segmented
          size="small"
          value={scope}
          onChange={(v) => setScope(v as QueueScope)}
          options={[
            { label: t('queue.scope.mine'), value: 'mine' },
            { label: t('queue.scope.all'), value: 'all' },
          ]}
        />
      )}
      {summary?.budget != null && (
        <span style={{ fontSize: 12 }}>
          <Typography.Text type="secondary">{t('queue.budget')}</Typography.Text>{' '}
          <Tag>
            {summary.queue_running_rows ?? summary.running_rows ?? summary.running ?? 0} / {summary.budget}
          </Tag>
        </span>
      )}
      {summary?.my_priority != null && (
        <span style={{ fontSize: 12 }}>
          <Typography.Text type="secondary">{t('queue.myPriority')}</Typography.Text> {priorityTag(t, String(summary.my_priority))}
        </span>
      )}
      {selectedJobs.length > 0 && (
        <Popconfirm title={t('queue.cancelJobsConfirm', { n: selectedJobs.length })} onConfirm={() => cancelJobs(selectedJobs)}>
          <Button size="small" danger icon={<StopOutlined />}>
            {t('queue.cancelSelected', { n: selectedJobs.length })}
          </Button>
        </Popconfirm>
      )}
      {admin && (
        // Hands the current filters to the Export page. Only status, submitter and workflow have a
        // field in the runs source; the free-text search and the derived "scheduled" view do not.
        <Button
          size="small"
          icon={<ExportOutlined />}
          onClick={() =>
            navigate(
              exportLink(
                'runs',
                runsFilterToTree({ status: fStatus === 'scheduled' ? '' : fStatus, createdBy: fUser, target: fTarget != null ? targets.find((tg) => tg.id === fTarget)?.name : '' }),
              ),
            )
          }
        >
          {t('queue.export')}
        </Button>
      )}
      {admin && (
        <Popconfirm title={t('queue.clearFinishedConfirm')} onConfirm={clearFinished}>
          <Button size="small" icon={<DeleteOutlined />}>
            {t('queue.clearFinished')}
          </Button>
        </Popconfirm>
      )}
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {t('queue.autoRefresh')}
      </Typography.Text>
      <Switch size="small" checked={auto} onChange={setAuto} />
    </Space>
  )

  return (
    <Space orientation="vertical" size={16} style={{ width: '100%' }}>
      {showStats && (
        // A responsive grid (not Space wrap): auto-fit + 1fr keeps every tile the same size
        // and stretched to fill its row, so the stats never leave a lone half-empty card.
        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(96px, 1fr))', gap: 10, width: '100%' }}>
          {stat(t('queue.running'), summary?.running ?? null)}
          {stat(t('queue.waiting'), summary?.waiting ?? null)}
          {stat(t('queue.scheduled'), summary?.scheduled ?? null)}
          {stat(t('queue.doneToday'), doneToday)}
          {stat(t('queue.budget'), summary?.budget ?? null)}
        </div>
      )}

      <Card title={t('queue.title')} extra={mobile ? undefined : queueControls}>
        {mobile && <div style={{ marginBottom: 12 }}>{queueControls}</div>}
        <Space wrap style={{ marginBottom: 12 }}>
          <Input.Search allowClear placeholder={t('queue.search')} value={search} onChange={(e) => setSearch(e.target.value)} style={{ width: 200 }} />
          <Select
            allowClear
            placeholder={t('batch.col.status')}
            style={{ width: 130 }}
            value={fStatus || undefined}
            onChange={(v) => setFStatus(v || '')}
            options={[
              { value: 'scheduled', label: t('queue.scheduled') },
              { value: 'queued', label: t('batch.status.queued') },
              { value: 'running', label: t('batch.status.running') },
              { value: 'finished', label: t('batch.status.finished') },
              { value: 'cancelled', label: t('batch.status.cancelled') },
            ]}
          />
          {admin && <Select showSearch optionFilterProp="label" allowClear placeholder={t('batch.col.createdBy')} style={{ width: 140 }} value={fUser || undefined} onChange={(v) => setFUser(v || '')} options={submitters.map((u) => ({ value: u, label: u }))} />}
          {/* loading, not an empty dropdown: "no workflows" is not something this filter knows. */}
          <Select showSearch optionFilterProp="label" allowClear loading={!targetsLoaded} placeholder={t('run.workflow')} style={{ width: 200 }} value={fTarget} onChange={setFTarget} options={targets.map((tg) => ({ value: tg.id, label: tg.name }))} />
          {tableView.controls}
        </Space>
        {total > jobs.length && (
          <Typography.Text type="secondary" style={{ display: 'block', marginBottom: 8 }}>
            {t('queue.truncatedHint', { shown: jobs.length, total })}
          </Typography.Text>
        )}
        {/* Until the first answer lands, "Queue is empty" is a claim about the server that the
            server has not made — and with auto-refresh off nothing would ever correct it. */}
        <LoadGate
          loading={!loaded && !loadErr}
          error={loaded ? undefined : loadErr}
          onRetry={load}
          minHeight={220}
          title={t('common.loadFailedContent')}
        >
        {rows.length === 0 ? (
          <Empty description={t('queue.empty')} />
        ) : (
          <Table
            rowKey="id"
            size="small"
            rowSelection={{
              selectedRowKeys: selectedJobs,
              onChange: setSelectedJobs,
              // Only in-flight jobs (their owner/admin) can be cancelled; terminal or
              // others' jobs aren't selectable.
              getCheckboxProps: (j) => ({ disabled: isTerminal(j.status) || !canCancel(j) }),
            }}
            dataSource={rows}
            columns={tableView.columns}
            className={tableView.className}
            tableLayout="fixed"
            pagination={{ pageSize: 15 }}
            scroll={{ x: tableView.scroll.x + 32 }}
          />
        )}
        </LoadGate>
      </Card>

      <JobDetailDrawer jobId={detailId} onClose={() => setDetailId(null)} />

      <Modal
        title={t('queue.reschedule')}
        open={reschedId != null}
        onOk={saveReschedule}
        okText={t('common.save')}
        cancelText={t('common.cancel')}
        onCancel={() => setReschedId(null)}
        destroyOnHidden
      >
        <Space orientation="vertical" style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('queue.rescheduleHint')}</Typography.Text>
          <DatePicker showTime={{ format: 'HH:mm' }} value={reschedAt} onChange={setReschedAt} format="YYYY-MM-DD HH:mm" classNames={{ popup: { root: 'rp-picker-popup' } }} style={{ width: '100%' }} placeholder={t('run.pickTime')} />
        </Space>
      </Modal>
    </Space>
  )
}
