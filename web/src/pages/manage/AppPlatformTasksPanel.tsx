import { useEffect, useState } from 'react'
import { Alert, Button, Descriptions, Space, Table, Tag, Typography } from 'antd'
import type { TableColumnsType } from 'antd'
import { ReloadOutlined, SafetyCertificateOutlined, StopOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import { usePrefs } from '../../prefs'

type Attempt = {
  attempt: number
  state: string
  startedMs: number
  finishedMs?: number
  errorCode?: string
  resultSha256?: string
  evidenceSha256?: string
}
type Task = {
  id: string
  installationId: string
  appId: string
  occurrenceId: string
  handler: string
  state: string
  generation: number
  policyRevision: number
  attempts: number
  maxAttempts: number
  nextAttemptMs: number
  expiresMs: number
  errorCode?: string
  result?: string
  completedMs?: number
  updatedMs: number
  attemptHistory: Attempt[]
}

const root = '/api/admin/app-platform/tasks'
const active = new Set(['waiting', 'leased', 'running'])
const reviewable = new Set(['uncertain', 'review_required'])

export default function AppPlatformTasksPanel() {
  const { t } = useTranslation()
  const { lang } = usePrefs()
  const [tasks, setTasks] = useState<Task[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const [reload, setReload] = useState(0)

  useEffect(() => {
    const abort = new AbortController()
    setLoading(true)
    api.get<{ tasks: Task[] }>(`${root}?limit=100`, { signal: abort.signal })
      .then((value) => {
        if (!abort.signal.aborted) {
          setTasks(Array.isArray(value.tasks) ? value.tasks : [])
          setError('')
        }
      })
      .catch((failure) => { if (!abort.signal.aborted) setError(errText(failure, t)) })
      .finally(() => { if (!abort.signal.aborted) setLoading(false) })
    return () => abort.abort()
  }, [reload, t])

  const when = (value?: number) => value && Number.isSafeInteger(value)
    ? new Intl.DateTimeFormat(lang, { dateStyle: 'short', timeStyle: 'medium' }).format(new Date(value))
    : '—'
  const stateColor = (state: string) => state === 'succeeded' ? 'success'
    : state === 'failed' || state === 'uncertain' || state === 'review_required' ? 'error'
      : state === 'cancelled' ? 'default'
        : 'processing'
  const mutate = async (task: Task, action: 'cancel' | 'review-failed' | 'review-cancelled') => {
    setBusy(`${task.id}:${action}`); setError('')
    try {
      if (action === 'cancel') {
        await api.post(`${root}/${task.id}/cancel`, {})
      } else {
        await api.post(`${root}/${task.id}/review`, {
          decision: action === 'review-failed' ? 'failed' : 'cancelled',
        })
      }
      setReload((value) => value + 1)
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy('')
    }
  }

  const columns: TableColumnsType<Task> = [
    {
      title: t('appPlatform.tasks.occurrence'),
      key: 'occurrence',
      render: (_, task) => <Space orientation="vertical" size={0}>
        <Typography.Text strong>{task.occurrenceId}</Typography.Text>
        <Typography.Text type="secondary">{task.appId} · {task.handler}</Typography.Text>
      </Space>,
    },
    {
      title: t('appPlatform.tasks.state'),
      dataIndex: 'state',
      render: (state: string) => <Tag color={stateColor(state)}>{t(`appPlatform.tasks.state.${state}`, { defaultValue: state })}</Tag>,
    },
    {
      title: t('appPlatform.tasks.attempts'),
      key: 'attempts',
      render: (_, task) => `${task.attempts}/${task.maxAttempts}`,
    },
    {
      title: t('appPlatform.tasks.due'),
      dataIndex: 'nextAttemptMs',
      render: (value: number) => when(value),
    },
    {
      title: t('appPlatform.tasks.outcome'),
      key: 'outcome',
      render: (_, task) => <Space orientation="vertical" size={2} style={{ maxWidth: 360 }}>
        {task.errorCode && <Typography.Text code>{task.errorCode}</Typography.Text>}
        {task.result && <Typography.Paragraph code ellipsis={{ rows: 2 }} style={{ margin: 0 }}>{task.result}</Typography.Paragraph>}
        {!task.errorCode && !task.result && <Typography.Text type="secondary">—</Typography.Text>}
      </Space>,
    },
    {
      title: t('appPlatform.action'),
      key: 'actions',
      render: (_, task) => <Space wrap>
        {active.has(task.state) && <Button
          size="small"
          danger
          icon={<StopOutlined aria-hidden="true" />}
          loading={busy === `${task.id}:cancel`}
          disabled={!!busy}
          onClick={() => void mutate(task, 'cancel')}
        >{t('appPlatform.tasks.cancel')}</Button>}
        {reviewable.has(task.state) && <>
          <Button
            size="small"
            icon={<SafetyCertificateOutlined aria-hidden="true" />}
            loading={busy === `${task.id}:review-failed`}
            disabled={!!busy}
            onClick={() => void mutate(task, 'review-failed')}
          >{t('appPlatform.tasks.reviewFailed')}</Button>
          <Button
            size="small"
            loading={busy === `${task.id}:review-cancelled`}
            disabled={!!busy}
            onClick={() => void mutate(task, 'review-cancelled')}
          >{t('appPlatform.tasks.reviewCancelled')}</Button>
        </>}
      </Space>,
    },
  ]

  return <Space orientation="vertical" size={16} style={{ width: '100%' }}>
    <Alert type="info" showIcon title={t('appPlatform.tasks.explanation')} />
    <Space>
      <Button icon={<ReloadOutlined aria-hidden="true" />} loading={loading} disabled={!!busy} onClick={() => setReload((value) => value + 1)}>
        {t('appPlatform.tasks.refresh')}
      </Button>
    </Space>
    {error && <Alert type="error" showIcon title={error} closable onClose={() => setError('')} />}
    <Table<Task>
      rowKey="id"
      loading={loading}
      dataSource={tasks}
      columns={columns}
      pagination={false}
      scroll={{ x: 960 }}
      locale={{ emptyText: t('appPlatform.tasks.empty') }}
      expandable={{
        expandedRowRender: (task) => <Descriptions
          size="small"
          column={1}
          items={[
            { key: 'installation', label: t('appPlatform.tasks.installation'), children: task.installationId },
            { key: 'expiry', label: t('appPlatform.tasks.expires'), children: when(task.expiresMs) },
            {
              key: 'attempts',
              label: t('appPlatform.tasks.attemptHistory'),
              children: task.attemptHistory.length
                ? <Space orientation="vertical" size={4}>{task.attemptHistory.map((attempt) => <Typography.Text key={attempt.attempt}>
                  #{attempt.attempt} · {attempt.state} · {when(attempt.startedMs)}
                  {attempt.finishedMs ? ` → ${when(attempt.finishedMs)}` : ''}
                  {attempt.errorCode ? ` · ${attempt.errorCode}` : ''}
                </Typography.Text>)}</Space>
                : '—',
            },
          ]}
        />,
      }}
    />
  </Space>
}
