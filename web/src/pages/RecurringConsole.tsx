import { useEffect, useState } from 'react'
import { App, Button, Card, Drawer, Space, Switch, Tag, Typography } from 'antd'
import { ClockCircleOutlined, DeleteOutlined, EditOutlined, HistoryOutlined, PlayCircleOutlined, PlusOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { confirmAction } from '../components/Confirm'
import { visibleOn } from '../lib/batchUi'
import { api, errText } from '../api/client'
import type { BatchTarget, RecurringDetail, RecurringRun, RecurringTask, RecurringTasksResp } from '../api/types'
import LoadGate from '../components/LoadGate'
import { useLoadFailure } from '../lib/useOnReconnect'
import { settleOnAnswer } from '../lib/loadFailure'
import CardTable from '../components/CardTable'
import { notifyError } from '../lib/notify'

// Recurring-tasks console (计划任务; docs/adr/0018-recurring-tasks.md). A first-party app card
// (gated by run_batch) that manages saved job templates + a daily/weekly/monthly cadence the server
// fires into the run queue. It talks to /api/admin/batch/recurring/* (same channel as the batch
// console, ADR 0018 §7) — the actual firing is a trusted backend loop, not this UI. This page is
// the list; creating and editing a task happen on their own page (RecurringEditor).

const ROUTE_BASE = '/apps/recurring'

export default function RecurringConsole() {
  const { t } = useTranslation()
  const { catchLoad, retryKey } = useLoadFailure()
  const { message, modal } = App.useApp()
  const navigate = useNavigate()
  const [targets, setTargets] = useState<BatchTarget[]>([])
  const [targetsLoaded, setTargetsLoaded] = useState(false)
  const [tasks, setTasks] = useState<RecurringTask[]>([])
  const [loading, setLoading] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [loadErr, setLoadErr] = useState('')

  const [history, setHistory] = useState<{ task: RecurringTask; runs: RecurringRun[] } | null>(null)

  const loadTargets = () =>
    api
      .get<{ targets: BatchTarget[] }>('/api/admin/batch/targets')
      .then((r) => setTargets(visibleOn(r.targets || [], 'recurring')))
      // Settled either way: load() swallows this rejection, and gating New on the flag alone
      // would leave the button disabled for good on a server that failed to answer once.
      .finally(() => setTargetsLoaded(true))
  // `refresh` is a reload behind an action: it keeps the list it has when the request fails and
  // always ends its own spinner, so a lost connection cannot leave the table loading.
  const loadTasks = (refresh = false) => {
    setLoading(true)
    return api
      .get<RecurringTasksResp>('/api/admin/batch/recurring')
      .then((r) => {
        setTasks(r.tasks || [])
        setLoaded(true)
      })
      .then(...(refresh ? ([() => setLoading(false), (e: unknown) => { setLoading(false); throw e }] as const) : settleOnAnswer(() => setLoading(false))))
  }

  // Two claims this console must not make before its two GETs land: "no run targets yet" (with
  // New greyed out beside it) and "no scheduled tasks yet". Both are about the server.
  const load = () => {
    setLoadErr('')
    loadTargets().catch(() => {})
    return loadTasks().catch(catchLoad(setLoadErr))
  }
  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [retryKey])

  const openCreate = () => navigate(`${ROUTE_BASE}/new`)
  // The task's name rides along as router state so the breadcrumb can show it before (and without)
  // the page fetching it; a deep link falls back to the generic title.
  const openEdit = (task: RecurringTask) => navigate(`${ROUTE_BASE}/${task.id}`, { state: { crumb: task.name } })

  const toggle = async (task: RecurringTask, enabled: boolean) => {
    try {
      await api.post(`/api/admin/batch/recurring/${task.id}/enable`, { enabled })
      loadTasks(true).catch(() => {})
    } catch (e) {
      notifyError(errText(e, t))
    }
  }

  const runNow = (task: RecurringTask) =>
    confirmAction(modal, {
      title: t('recurring.runNowTitle', { name: task.name }),
      description: t('recurring.runNowBody'),
      okText: t('recurring.runNowAction'),
      danger: false,
      onOk: async () => {
        const r = await api.post<{ job_id: number }>(`/api/admin/batch/recurring/${task.id}/run`, {})
        message.success(t('recurring.firedJob', { id: r.job_id }))
        loadTasks(true).catch(() => {})
      },
    }, t)

  const remove = (task: RecurringTask) =>
    confirmAction(modal, {
      title: t('recurring.deleteTitle', { name: task.name }),
      description: t('recurring.deleteBody'),
      okText: t('recurring.deleteAction'),
      irreversible: true,
      onOk: async () => {
        await api.del(`/api/admin/batch/recurring/${task.id}`)
        message.success(t('recurring.deleted'))
        loadTasks(true).catch(() => {})
      },
    }, t)

  const openHistory = async (task: RecurringTask) => {
    const d = await api.get<RecurringDetail>(`/api/admin/batch/recurring/${task.id}`)
    setHistory({ task, runs: d.history || [] })
  }

  const cadenceLabel = (task: RecurringTask) => {
    if (task.freq === 'daily') return t('recurring.cadence.daily', { time: task.at_time })
    if (task.freq === 'weekly') return t('recurring.cadence.weekly', { day: t(`run.weekday.${task.weekday}`), time: task.at_time })
    if (task.freq === 'monthly') return t('recurring.cadence.monthly', { day: task.monthday, time: task.at_time })
    return task.freq
  }

  const columns = [
    {
      title: t('recurring.colName'),
      dataIndex: 'name',
      render: (name: string, task: RecurringTask) => (
        <Space orientation="vertical" size={0}>
          <Typography.Text strong>{name}</Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {task.target_name || t('recurring.targetGone')} · {t('recurring.rowsN', { n: task.row_count })}
          </Typography.Text>
        </Space>
      ),
    },
    { title: t('recurring.colCadence'), key: 'cadence', render: (_: unknown, task: RecurringTask) => cadenceLabel(task) },
    {
      title: t('recurring.colPriority'),
      dataIndex: 'priority',
      render: (p: string) => {
        if (p === 'idle') return <Tag>{t('recurring.priorityIdle')}</Tag>
        if (p === 'urgent') return <Tag color="red">{t('recurring.priorityUrgent')}</Tag>
        if (p !== '' && !Number.isNaN(Number(p))) return <Tag color="gold">{t('recurring.priorityBase', { n: p })}</Tag>
        return <Tag color="blue">{t('recurring.priorityNormal')}</Tag>
      },
    },
    {
      title: t('recurring.colNext'),
      dataIndex: 'next_run',
      render: (v: string, task: RecurringTask) => (task.enabled ? v || '—' : <Typography.Text type="secondary">{t('recurring.paused')}</Typography.Text>),
    },
    { title: t('recurring.colLastFired'), dataIndex: 'last_fired', render: (v: string) => v || <Typography.Text type="secondary">{t('recurring.never')}</Typography.Text> },
    {
      title: t('recurring.colEnabled'),
      dataIndex: 'enabled',
      render: (enabled: boolean, task: RecurringTask) => <Switch checked={enabled} onChange={(v) => toggle(task, v)} />,
    },
    {
      title: t('recurring.colActions'),
      key: 'actions',
      render: (_: unknown, task: RecurringTask) => (
        <Space>
          <Button size="small" icon={<PlayCircleOutlined />} onClick={() => runNow(task)}>
            {t('recurring.runNow')}
          </Button>
          <Button size="small" icon={<HistoryOutlined />} aria-label={t('recurring.history')} onClick={() => openHistory(task)} />
          <Button size="small" icon={<EditOutlined />} aria-label={t('common.edit')} onClick={() => openEdit(task)} />
          <Button size="small" danger icon={<DeleteOutlined />} aria-label={t('common.delete')} onClick={() => remove(task)} />
        </Space>
      ),
    },
  ]

  return (
    <Space orientation="vertical" size={16} style={{ width: '100%' }}>
      <Card
        title={
          <Space>
            <ClockCircleOutlined />
            {t('recurring.title')}
          </Space>
        }
        extra={
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate} disabled={!targetsLoaded || targets.length === 0}>
            {t('recurring.new')}
          </Button>
        }
      >
        <Space orientation="vertical" size={12} style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('recurring.intro')}</Typography.Text>
          {targetsLoaded && targets.length === 0 && <Typography.Text type="warning">{t('recurring.noTargets')}</Typography.Text>}
          <LoadGate loading={loading && !loaded} error={loaded ? undefined : loadErr} onRetry={load} minHeight={200} title={t('common.loadFailedContent')}>
          <div style={{ overflowX: 'auto' }}>
            <CardTable
              rowKey="id"
              size="small"
              loading={loading}
              pagination={false}
              dataSource={tasks}
              columns={columns}
              locale={{ emptyText: t('recurring.empty') }}
            />
          </div>
          </LoadGate>
        </Space>
      </Card>

      <Drawer
        open={history != null}
        onClose={() => setHistory(null)}
        size={440}
        title={history ? t('recurring.historyTitle', { name: history.task.name }) : ''}
      >
        <CardTable
          rowKey="id"
          size="small"
          pagination={false}
          dataSource={history?.runs || []}
          locale={{ emptyText: t('recurring.never') }}
          columns={[
            { title: t('recurring.histFired'), dataIndex: 'fired_at' },
            {
              title: t('recurring.histJob'),
              dataIndex: 'job_id',
              render: (id: number, r: RecurringRun) => (
                <Space size={6}>
                  <Typography.Text>#{id}</Typography.Text>
                  {r.job_status && <Tag>{r.job_status}</Tag>}
                </Space>
              ),
            },
          ]}
        />
      </Drawer>
    </Space>
  )
}
