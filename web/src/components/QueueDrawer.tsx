import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { App, Button, Drawer, Empty, Popconfirm, Segmented, Tabs, Tooltip, Typography, theme } from 'antd'
import { ArrowRightOutlined, DownloadOutlined, StopOutlined, ThunderboltOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { api, errText } from '../api/client'
import type { BatchJob, BatchQueueSummary, BatchTarget, TaskItem } from '../api/types'
import { InputsPreview, isTerminal, jobProgressPresentation } from '../lib/batchUi'
import { UNCHANGED, forgetTags, getIfChanged } from '../lib/conditionalGet'
import { fmtBytes, notifyExportsChanged } from '../lib/exportJobs'
import { watchQueue } from '../lib/queueWatch'
import { useQueueScope, withQueueScope, type QueueScope } from '../lib/queueScope'
import { formatTaskTime, runOutcome } from '../lib/taskUi'
import { isActiveTask, taskKey } from '../lib/useMyTasks'
import { useAuth } from '../auth'
import { startVisiblePoll } from '../lib/visiblePoll'
import LoadGate from './LoadGate'
import TaskRow, { type TaskTone } from './TaskRow'

export type DrawerTab = 'runs' | 'exports'

// Finished exports are kept this long (exportFileTTL in internal/app/export_jobs.go).
const EXPORT_KEPT_DAYS = 7
const RECENT_MAX = 5

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
  const { admin } = useAuth()
  const showRuns = canRun
  const showExports = canExport || admin
  const activeTab: DrawerTab = tab === 'exports' ? (showExports ? 'exports' : 'runs') : showRuns ? 'runs' : 'exports'
  // The queue is asked for only while the Runs tab is on screen; switching back asks at once.
  const runsShown = canRun && activeTab === 'runs'
  // Whose runs the drawer covers; the same per-browser pick as the full page (ADR 0047).
  const [scope, setScope] = useQueueScope(admin)
  const [summary, setSummary] = useState<BatchQueueSummary | null>(null)
  const [jobs, setJobs] = useState<BatchJob[]>([])
  const [targets, setTargets] = useState<BatchTarget[]>([])
  // Same rule as the full queue page: `[]` and a wall of zeros are where the state starts, not
  // an answer. Nothing is said about the queue until the jobs call has landed at least once.
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')
  // What this drawer is actually holding. The tag store is module-global, and the header badge
  // polls the same summary URL — so on the first open a 304 would answer "unchanged" about a
  // summary the drawer has never seen, leaving all four tiles on a dash for good.
  // The URLs whose answers the drawer holds (they change with the scope).
  const heldSummary = useRef<string | null>(null)
  const heldJobs = useRef<string | null>(null)

  const load = async () => {
    const summaryUrl = withQueueScope('/api/admin/batch/queue', scope, admin)
    const jobsUrl = withQueueScope('/api/admin/batch/jobs', scope, admin)
    if (heldSummary.current !== summaryUrl) forgetTags(summaryUrl)
    if (heldJobs.current !== jobsUrl) forgetTags(jobsUrl)
    const summaryRequest = getIfChanged<BatchQueueSummary>(summaryUrl)
      .then((r) => {
        if (r === UNCHANGED) return
        setSummary(r)
        heldSummary.current = summaryUrl
      })
      .catch(() => {})
    const jobsRequest = getIfChanged<{ jobs: BatchJob[] }>(jobsUrl).then((r) => {
      if (r === UNCHANGED) return
      setJobs(r.jobs || [])
      heldJobs.current = jobsUrl
    })
    try {
      await jobsRequest
      if (heldJobs.current === jobsUrl) {
        setLoaded(true)
        setLoadErr('')
      }
    } catch (e) {
      // Read only while !loaded: a poll that fails once the list is on screen changes nothing,
      // since those runs are still the last thing the server said about the queue.
      setLoadErr(errText(e, t))
    }
    await summaryRequest
  }

  useEffect(() => {
    if (!open) return
    void refreshTasks()
    if (!runsShown) return
    // Reopening asks again, so the drawer must not present the previous open's answer as this
    // one's — but it keeps the rows it has so a reopen is not a flash of spinner.
    setLoadErr('')
    api.get<{ targets: BatchTarget[] }>('/api/admin/batch/targets').then((r) => setTargets(r.targets || [])).catch(() => {})
    const unwatch = watchQueue() // the badge stands down while the drawer shows the same thing
    const stop = startVisiblePoll(load, 3000)
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

  const iconAction = (label: string, icon: ReactNode, props: { onClick?: () => void; href?: string; danger?: boolean }) => (
    <Tooltip title={label}>
      <Button type="text" size="small" icon={icon} aria-label={label} {...props} />
    </Tooltip>
  )
  const openAction = (label: string, to: string) => iconAction(label, <ArrowRightOutlined />, { onClick: () => { onClose(); navigate(to) } })

  // null = not answered yet; a dash rather than a 0, which would read as a settled idle queue.
  const stat = (label: string, value: number | null) => (
    <div>
      <div style={{ fontSize: 20, fontWeight: 500 }}>{value ?? <Typography.Text type="secondary">—</Typography.Text>}</div>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {label}
      </Typography.Text>
    </div>
  )

  const section = (label: string, aside: ReactNode, children: ReactNode) => (
    <section style={{ marginTop: 4 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: 12, color: token.colorTextSecondary, padding: '12px 8px 6px' }}>
        <span>{label}</span>
        {aside && <span>{aside}</span>}
      </div>
      <ul aria-label={label} style={{ margin: 0, padding: 0 }}>
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
      percent = progress.percent
      detail = t('tasks.rows', { done: progress.done, total: j.total })
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
              <Popconfirm title={t('queue.cancelConfirm')} onConfirm={() => cancelRun(j.id)}>
                <Tooltip title={t('tasks.cancel')}>
                  <Button type="text" size="small" danger icon={<StopOutlined />} aria-label={t('tasks.cancel')} />
                </Tooltip>
              </Popconfirm>
            )}
            {openAction(t('tasks.open'), '/queue')}
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
        tone={tone}
        name={x.name}
        sub={x.format ? <Typography.Text type="secondary" ellipsis style={{ fontSize: 12 }}>{t(`export.format.${x.format}`, x.format)}</Typography.Text> : undefined}
        percent={percent}
        detail={detail}
        time={formatTaskTime(live ? x.created_at : finishedAtOf(x), lang, undefined, t('tasks.justNow'))}
        actions={
          <>
            {x.cancel && iconAction(t('tasks.cancel'), <StopOutlined />, { onClick: () => void cancelExport(x), danger: true })}
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
        {activeJobs.length > 0 && section(t('tasks.section.active'), null, activeJobs.map(runRow))}
        {recentJobs.length > 0 && section(t('tasks.section.finished'), null, recentJobs.map(runRow))}
      </LoadGate>
    </>
  )

  const exportsPane = (
    <>
      {activeExports.length === 0 && recentExports.length === 0 && empty(t('tasks.emptyExports'))}
      {activeExports.length > 0 && section(t('tasks.section.active'), null, activeExports.map(exportRow))}
      {recentExports.length > 0 && section(t('tasks.section.finished'), t('tasks.keptDays', { n: EXPORT_KEPT_DAYS }), recentExports.map(exportRow))}
    </>
  )

  const count = (n: number | null) =>
    n ? <span style={{ marginLeft: 4, fontSize: 12, color: token.colorTextSecondary }}>{n}</span> : null
  const tabItems = [
    ...(showRuns ? [{ key: 'runs', label: <span>{t('tasks.tab.runs')}{count(loaded ? activeJobs.length : null)}</span>, children: runsPane }] : []),
    ...(showExports ? [{ key: 'exports', label: <span>{t('tasks.tab.exports')}{count(activeExports.length)}</span>, children: exportsPane }] : []),
  ]

  const onRuns = activeTab === 'runs'
  const footerTo = onRuns ? '/queue' : admin ? '/manage/exports' : '/exports'

  return (
    <Drawer
      title={
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <span>{t('tasks.title')}</span>
          {admin && showRuns && activeTab === 'runs' && (
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
        </div>
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
      {tabItems.length > 1 ? (
        <Tabs size="small" activeKey={activeTab} onChange={(k) => onTabChange(k as DrawerTab)} items={tabItems} tabBarStyle={{ margin: '0 8px' }} />
      ) : (
        tabItems[0]?.children
      )}
    </Drawer>
  )
}
