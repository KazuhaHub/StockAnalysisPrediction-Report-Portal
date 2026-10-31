import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { App, Button, Drawer, Empty, Popconfirm, Segmented, Tooltip, Typography, theme } from 'antd'
import { ArrowRightOutlined, CloseCircleOutlined, DownloadOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { useLocation, useNavigate } from 'react-router'
import { api, errText } from '../api/client'
import type { BatchJob, BatchQueueSummary, BatchTarget, TaskItem } from '../api/types'
import { InputsPreview, isTerminal, jobProgressPresentation } from '../lib/batchUi'
import { UNCHANGED, forgetTags, getIfChanged, type TagHolder } from '../lib/conditionalGet'
import { fmtBytes, notifyExportsChanged } from '../lib/exportJobs'
import { watchQueue } from '../lib/queueWatch'
import { useQueueScope, withQueueScope, type QueueScope } from '../lib/queueScope'
import { formatTaskTime, runOutcome } from '../lib/taskUi'
import { isActiveTask, taskKey } from '../lib/useMyTasks'
import { useAuth } from '../auth'
import { startVisiblePoll } from '../lib/visiblePoll'
import { loadFailure } from '../lib/loadFailure'
import LoadGate from './LoadGate'
import TaskRow, { type TaskTone } from './TaskRow'
import RunningFor from './RunningFor'
import ExportDetailModal from './ExportDetailModal'
import { openJobDetail, pageShowsJob } from '../lib/jobDetailStore'

export type DrawerTab = 'runs' | 'exports'

// Finished exports are kept this long (exportFileTTL in internal/app/export_jobs.go).
const EXPORT_KEPT_DAYS = 7
const RECENT_MAX = 5

// One scope's last answer: null until its first summary / jobs call has landed.
type ScopeView = { summary: BatchQueueSummary | null; jobs: BatchJob[] | null; err: string }

export interface TasksDrawerProps {
  open: boolean
  onClose: () => void
  /** Holds run_batch: the Runs tab and the run queue are theirs. */
  canRun: boolean
  /** Reader export is allowed for them (administrators have the Exports tab regardless). */
  canExport: boolean
  /** The caller's own tasks from /api/tasks; the drawer shows the exports among them. */
  tasks: TaskItem[]
  refreshTasks: () => void | Promise<void>
  tab: DrawerTab
  onTabChange: (tab: DrawerTab) => void
}

const finishedAtOf = (t: TaskItem) => t.finished_at || t.created_at

// The header Tasks drawer (docs/adr/0007, ADR 0047): a live glance at the caller's runs and exports.
// Runs poll the batch queue while open; exports come from the shell's task feed. Deeper management
// lives on the /queue page and the export pages.
export default function QueueDrawer({ open, onClose, canRun, canExport, tasks, refreshTasks, tab, onTabChange }: TasksDrawerProps) {
  const { t, i18n } = useTranslation()
  const lang = i18n?.language || 'en-US'
  const { message } = App.useApp()
  const { token } = theme.useToken()
  const navigate = useNavigate()
  const loc = useLocation()
  const { admin } = useAuth()
  const showRuns = canRun
  const showExports = canExport || admin
  const activeTab: DrawerTab = tab === 'exports' ? (showExports ? 'exports' : 'runs') : showRuns ? 'runs' : 'exports'
  // The queue is asked for only while the Runs tab is on screen; switching back asks at once.
  // The row whose cancel confirmation is open keeps its actions on screen.
  const [confirming, setConfirming] = useState<string | null>(null)
  // The export whose compact detail is open.
  const [exportDetail, setExportDetail] = useState<string | null>(null)
  // It belongs to this open of the drawer and this page: closing the drawer or changing route puts it away.
  const here = loc.pathname + loc.search
  useEffect(() => {
    setExportDetail(null)
  }, [open, here])
  const runsShown = canRun && activeTab === 'runs'
  // Whose runs the drawer covers; the same per-browser pick as the full page (ADR 0047).
  const [scope, setScope] = useQueueScope(admin)
  // Each scope keeps its own last answer, so the Mine / All switch shows the other at once and
  // never shows one scope's runs under the other's label. A scope with no answer yet has no
  // entry: `[]` and a wall of zeros are where the state starts, not an answer, so nothing is said
  // about the queue until that scope's jobs call has landed at least once.
  const [views, setViews] = useState<Partial<Record<QueueScope, ScopeView>>>({})
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const view = views[scope]
  const summary = view?.summary ?? null
  const jobs = useMemo(() => view?.jobs ?? [], [view])
  const loaded = !!view?.jobs
  const loadErr = view?.err ?? ''
  // The drawer's own revalidation tags. The header badge and the queue page poll the same summary
  // URL, so with shared tags a 304 could answer "unchanged" about a summary the drawer never had.
  // Each scope's URLs are fixed and its view keeps the last answer, so whatever tag the drawer
  // holds for a URL came with the answer it is showing.
  const [tagHolder] = useState<TagHolder>(() => ({}))
  // Also when the viewer's admin role changes: the plain URL then means another scope (everyone's
  // for an administrator, their own otherwise), so a tag kept across that would answer for the wrong one.
  useEffect(() => () => forgetTags(undefined, tagHolder), [tagHolder, admin])

  const loadScope = async (sc: QueueScope) => {
    const patch = (p: Partial<ScopeView>) => setViews((v) => ({ ...v, [sc]: { summary: null, jobs: null, err: '', ...v[sc], ...p } }))
    const summaryUrl = withQueueScope('/api/admin/batch/queue', sc, admin)
    const jobsUrl = withQueueScope('/api/admin/batch/jobs', sc, admin)
    const summaryRequest = getIfChanged<BatchQueueSummary>(summaryUrl, tagHolder)
      .then((r) => {
        if (r !== UNCHANGED) patch({ summary: r })
      })
      .catch(() => {})
    try {
      const r = await getIfChanged<{ jobs: BatchJob[] }>(jobsUrl, tagHolder)
      if (r !== UNCHANGED) patch({ jobs: r.jobs || [], err: '' })
    } catch (e) {
      // Read only while that scope has no rows: a poll that fails once the list is on screen
      // changes nothing, since those runs are still the last thing the server said.
      const text = loadFailure(e, t)
      if (text !== null) patch({ err: text })
    }
    await summaryRequest
  }
  const load = () => loadScope(scope)
  // The scope not on screen is asked once per open of the Runs tab, so switching to it has an
  // answer ready; the poll then keeps whichever scope is showing current.
  const askedOther = useRef(false)
  useEffect(() => {
    if (!open || !runsShown) askedOther.current = false
  }, [open, runsShown])

  useEffect(() => {
    if (!open) return
    void refreshTasks()
    if (!runsShown) return
    // Reopening, switching scope or returning to Runs asks again, so an earlier failure is not
    // presented as this ask's answer (a scope that never loaded shows its spinner until the poll
    // answers or fails again) — but the rows are kept so none of these is a flash of spinner.
    setViews((v) => Object.fromEntries(Object.entries(v).map(([k, x]) => [k, { ...x, err: '' }])))
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || [])).catch(() => {})
    const unwatch = watchQueue() // the badge stands down while the drawer shows the same thing
    const stop = startVisiblePoll(load, 3000)
    if (admin && !askedOther.current) {
      askedOther.current = true
      void loadScope(scope === 'all' ? 'mine' : 'all')
    }
    return () => {
      stop()
      unwatch()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, scope, runsShown])

  const targetName = (id: number) => targets.find((tg) => tg.id === id)?.name || `#${id}`

  // In progress: running first, then waiting, then scheduled. Finished: newest first, capped.
  const activeJobs = useMemo(() => {
    const rank = (j: BatchJob) => (j.status === 'queued' ? (j.scheduled ? 2 : 1) : 0)
    return jobs.filter((j) => !isTerminal(j.status)).sort((a, b) => rank(a) - rank(b))
  }, [jobs])
  const recentJobs = useMemo(
    () => jobs.filter((j) => isTerminal(j.status)).sort((a, b) => (b.finished_at || '').localeCompare(a.finished_at || '')).slice(0, RECENT_MAX),
    [jobs],
  )
  const exportTasks = useMemo(() => tasks.filter((x) => x.kind === 'export'), [tasks])
  const activeExports = useMemo(() => exportTasks.filter(isActiveTask), [exportTasks])
  const recentExports = useMemo(
    () => exportTasks.filter((x) => !isActiveTask(x)).sort((a, b) => finishedAtOf(b) - finishedAtOf(a)).slice(0, RECENT_MAX),
    [exportTasks],
  )

  const cancelRun = async (id: number) => {
    try {
      await api.post(`/api/admin/batch/jobs/${id}/cancel`)
      message.success(t('batch.msg.cancelRequested'))
      void load()
    } catch (e) {
      message.error(errText(e, t))
    }
  }
  const cancelExport = async (task: TaskItem) => {
    if (!task.cancel) return
    try {
      await api.post(task.cancel)
      void refreshTasks()
      notifyExportsChanged()
    } catch (e) {
      message.error(errText(e, t))
    }
  }

  const iconAction = (label: string, icon: ReactNode, props: { onClick?: () => void; href?: string; className?: string }) => (
    <Tooltip title={label}>
      <Button type="text" size="small" icon={icon} aria-label={label} {...props} />
    </Tooltip>
  )

  // null = not answered yet; a dash rather than a 0, which would read as a settled idle queue.
  const stat = (label: string, value: number | null) => (
    <div>
      <div style={{ fontSize: 20, fontWeight: 500 }}>{value ?? <Typography.Text type="secondary">—</Typography.Text>}</div>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {label}
      </Typography.Text>
    </div>
  )

  const section = (label: string, aside: ReactNode, count: number | null, children: ReactNode) => (
    <section style={{ marginTop: 20 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12, color: token.colorTextSecondary, padding: '0 12px 8px' }}>
        <span>{count === null ? label : t('tasks.section.count', { label, count })}</span>
        {aside && <span>{aside}</span>}
      </div>
      <ul aria-label={label} className="rp-task-list">
        {children}
      </ul>
    </section>
  )

  const runRow = (j: BatchJob) => {
    const live = !isTerminal(j.status)
    const progress = jobProgressPresentation(j)
    let tone: TaskTone = 'idle'
    let detail: ReactNode
    let percent: number | undefined
    if (j.status === 'running' || j.status === 'cancelling') {
      tone = 'running'
      if (j.status === 'running' && j.total === 1) {
        // One row: a bar and "0 / 1 rows" say nothing, so say how long it has been going.
        detail = <RunningFor since={j.started_at_ms ?? 0} />
      } else {
        percent = progress.percent
        detail = t('tasks.rows', { done: progress.done, total: j.total })
      }
    } else if (j.status === 'queued') {
      tone = 'waiting'
      detail = j.window_blocked
        ? t('queue.avoidingWindow')
        : j.scheduled
          ? t('tasks.scheduledAt', { at: formatTaskTime(j.run_at, lang) || j.run_at })
          : j.ahead === undefined
            ? t('tasks.runStatus.queued')
            : j.ahead === 0
              ? t('tasks.next')
              : t('tasks.queuedAhead', { n: j.ahead })
    } else if (live) {
      tone = 'warn'
      detail = t(`batch.status.${j.status}`, j.status)
    } else if (j.status === 'cancelled') {
      detail = t('tasks.result.cancelled')
    } else if (j.status === 'expired') {
      detail = t('batch.status.expired')
    } else {
      const ok = j.succeeded + j.partial
      const outcome = runOutcome(j)
      if (outcome === 'ok' && ok === 0) {
        detail = t('tasks.result.cancelled')
      } else if (outcome === 'ok') {
        tone = 'ok'
        detail = t('tasks.result.done', { ok })
      } else if (outcome === 'partialOnly') {
        tone = 'warn'
        detail = t('tasks.result.partial', { n: j.partial })
      } else if (outcome === 'someFailed') {
        tone = 'warn'
        detail = t('tasks.result.partly', { ok, failed: j.failed })
      } else {
        tone = 'fail'
        detail = t('tasks.result.failed', { failed: j.failed })
      }
    }
    return (
      <TaskRow
        key={j.id}
        pinned={confirming === `run-${j.id}`}
        label={`${targetName(j.target_id)}, ${t(`batch.status.${j.status}`, j.status)}`}
        onOpen={() => {
          // The queue page already shows this run in its own drawer: step aside rather than stack a second one.
          if (pageShowsJob(loc, j.id)) onClose()
          else openJobDetail(j.id)
        }}
        tone={tone}
        name={
          <>
            {targetName(j.target_id)}
            {j.priority === 'urgent' && <ThunderboltOutlined style={{ color: token.colorError, marginLeft: 6 }} title={t('batch.priority.urgent')} />}
          </>
        }
        sub={<InputsPreview inputs={j.inputs} rows={1} style={{ whiteSpace: 'nowrap' }} />}
        percent={percent}
        detail={detail}
        time={formatTaskTime(live ? j.created_at : j.finished_at, lang, undefined, t('tasks.justNow'))}
        actions={
          <>
            {live && (
              <Popconfirm title={t('queue.cancelConfirm')} onConfirm={() => cancelRun(j.id)} onOpenChange={(o) => setConfirming(o ? `run-${j.id}` : null)}>
                <Tooltip title={t('tasks.cancel')}>
                  <Button type="text" size="small" className="rp-task-cancel" icon={<CloseCircleOutlined />} aria-label={t('tasks.cancel')} />
                </Tooltip>
              </Popconfirm>
            )}
          </>
        }
      />
    )
  }

  const exportRow = (x: TaskItem) => {
    const live = isActiveTask(x)
    let tone: TaskTone = 'idle'
    let detail: ReactNode
    let percent: number | undefined
    if (x.status === 'running' || x.status === 'cancelling') {
      tone = 'running'
      percent = x.total > 0 ? Math.max(0, Math.min(100, Math.floor((x.done / x.total) * 100))) : 0
      detail = x.total > 0 ? t('tasks.rows', { done: x.done, total: x.total }) : t('tasks.waiting')
    } else if (x.status === 'queued') {
      tone = 'waiting'
      detail = x.ahead === undefined ? t('export.status.queued') : x.ahead === 0 ? t('tasks.next') : t('tasks.queuedAhead', { n: x.ahead })
    } else if (x.status === 'done') {
      tone = 'ok'
      detail = x.done > 0
        ? x.bytes
          ? t('tasks.result.exportDone', { n: x.done, size: fmtBytes(x.bytes) })
          : t('tasks.result.exportDoneNoSize', { n: x.done })
        : t('export.status.done')
    } else if (x.status === 'failed') {
      tone = 'fail'
      detail = x.error ? t('tasks.result.failedWhy', { error: x.error }) : t('export.status.failed')
    } else {
      detail = t(`export.status.${x.status}`, x.status)
    }
    return (
      <TaskRow
        key={taskKey(x)}
        pinned={confirming === `export-${taskKey(x)}`}
        label={`${x.name}, ${t(`export.status.${x.status}`, x.status)}`}
        onOpen={() => setExportDetail(taskKey(x))}
        tone={tone}
        name={x.name}
        sub={x.format ? <Typography.Text type="secondary" ellipsis style={{ fontSize: 12 }}>{t(`export.format.${x.format}`, x.format)}</Typography.Text> : undefined}
        percent={percent}
        detail={detail}
        time={formatTaskTime(live ? x.created_at : finishedAtOf(x), lang, undefined, t('tasks.justNow'))}
        actions={
          <>
            {x.cancel && (
              <Popconfirm
                title={t('export.history.cancelConfirm')}
                onConfirm={() => void cancelExport(x)}
                onOpenChange={(o) => setConfirming(o ? `export-${taskKey(x)}` : null)}
              >
                <Tooltip title={t('tasks.cancel')}>
                  <Button type="text" size="small" className="rp-task-cancel" icon={<CloseCircleOutlined />} aria-label={t('tasks.cancel')} />
                </Tooltip>
              </Popconfirm>
            )}
            {x.download && iconAction(t('tasks.download'), <DownloadOutlined />, { href: x.download })}
          </>
        }
      />
    )
  }

  const empty = (text: string) => <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={text} style={{ margin: '32px 0' }} />

  const runsPane = (
    <>
      <div className="rp-task-stats">
        {stat(t('queue.running'), summary?.running ?? null)}
        {stat(t('queue.waiting'), summary?.waiting ?? null)}
        {stat(t('queue.scheduled'), summary?.scheduled ?? null)}
        {stat(t('queue.budget'), summary?.budget ?? null)}
      </div>
      <LoadGate
        loading={!loaded && !loadErr}
        error={loaded ? undefined : loadErr}
        onRetry={load}
        minHeight={200}
        title={t('common.loadFailedContent')}
      >
        {activeJobs.length === 0 && recentJobs.length === 0 && empty(t('tasks.emptyRuns'))}
        {activeJobs.length > 0 && section(t('tasks.section.active'), null, activeJobs.length, activeJobs.map(runRow))}
        {recentJobs.length > 0 && section(t('tasks.section.finished'), null, null, recentJobs.map(runRow))}
      </LoadGate>
    </>
  )

  const exportsPane = (
    <>
      {activeExports.length === 0 && recentExports.length === 0 && empty(t('tasks.emptyExports'))}
      {activeExports.length > 0 && section(t('tasks.section.active'), null, activeExports.length, activeExports.map(exportRow))}
      {recentExports.length > 0 && section(t('tasks.section.finished'), t('tasks.keptDays', { n: EXPORT_KEPT_DAYS }), null, recentExports.map(exportRow))}
    </>
  )

  // The two views switch with a full-width segmented control, like Mine / All. An in-progress
  // count is a small pill, shown only when there is something in progress.
  const viewLabel = (label: string, n: number | null) => (
    <span className="rp-task-views__label">
      {label}
      {n ? <span className="rp-task-views__count">{n}</span> : null}
    </span>
  )
  const tabItems = [
    ...(showRuns ? [{ key: 'runs' as const, label: viewLabel(t('tasks.tab.runs'), loaded ? activeJobs.length : null), children: runsPane }] : []),
    ...(showExports ? [{ key: 'exports' as const, label: viewLabel(t('tasks.tab.exports'), activeExports.length), children: exportsPane }] : []),
  ]
  const activePane = (tabItems.find((item) => item.key === activeTab) ?? tabItems[0])?.children

  const onRuns = activeTab === 'runs'
  const footerTo = onRuns ? '/queue' : admin ? '/manage/exports' : '/exports'

  return (
    <Drawer
      title={t('tasks.title')}
      extra={
        admin && showRuns && activeTab === 'runs' ? (
          <Segmented
            size="small"
            value={scope}
            onChange={(v) => setScope(v as QueueScope)}
            options={[
              { label: t('queue.scope.mine'), value: 'mine' },
              { label: t('queue.scope.all'), value: 'all' },
            ]}
          />
        ) : null
      }
      size={420}
      open={open}
      onClose={onClose}
      styles={{ body: { padding: '0 12px 8px' } }}
      footer={
        <Button type="link" icon={<ArrowRightOutlined />} iconPlacement="end" onClick={() => { onClose(); navigate(footerTo) }} style={{ padding: 0 }}>
          {onRuns ? t('tasks.openQueue') : t('tasks.openExports')}
        </Button>
      }
    >
      {tabItems.length > 1 && (
        <Segmented
          block
          className="rp-task-views"
          value={activeTab}
          onChange={(k) => onTabChange(k as DrawerTab)}
          options={tabItems.map((item) => ({ value: item.key, label: item.label }))}
        />
      )}
      {activePane}
      <ExportDetailModal
        task={exportDetail ? (exportTasks.find((x) => taskKey(x) === exportDetail) ?? null) : null}
        onClose={() => setExportDetail(null)}
        onCancel={cancelExport}
      />
    </Drawer>
  )
}
