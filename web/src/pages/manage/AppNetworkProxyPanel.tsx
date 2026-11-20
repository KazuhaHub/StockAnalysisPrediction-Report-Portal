import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Input, InputNumber, Modal, Progress, Space, Table, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import StatusTag from '../../components/StatusTag'

// The administrator's side of the reviewed forward proxy (doc 184 sections 5.2 and 7). The app
// declares the hosts it may reach; the administrator narrows or edits that list, sees the plan the
// server computed, and only a confirmed review consumes it. Withdrawal is the same flow with no hosts.

const GIB = 1024 * 1024 * 1024
const CLOSE_REASONS = ['eof', 'idle', 'lifetime', 'session', 'budget', 'policy', 'sni_mismatch', 'error'] as const

type Rollup = {
  hour: string
  host: string
  tunnels: number
  bytes_in: number
  bytes_out: number
  closes: Record<string, number>
}
type State = {
  policy_revision: string
  granted: boolean
  ready: boolean
  declared_hosts: string[]
  policy: { hosts: string[]; daily_bytes: number } | null
  max_daily_bytes: number
  previous_policy_sha256: string
  today: { day: string; bytes: number; budget: number; reported: boolean }
  rollups: Rollup[]
}
type DiffEntry = { entry: string; wildcard: boolean }
type Plan = {
  plan_id: string
  plan_sha256: string
  expires_ms: number
  policy_revision: string
  diff: {
    added: DiffEntry[]
    removed: DiffEntry[]
    daily_bytes: { old: number; new: number }
  }
}

export function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 1024) return `${Math.max(0, Math.round(value || 0))} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let scaled = value
  let unit = -1
  while (scaled >= 1024 && unit < units.length - 1) { scaled /= 1024; unit++ }
  return `${scaled >= 100 ? Math.round(scaled) : Math.round(scaled * 10) / 10} ${units[unit]}`
}

const isWildcard = (entry: string) => entry.startsWith('*.')
const parseHosts = (text: string) => Array.from(new Set(text.split('\n').map((line) => line.trim()).filter(Boolean)))

export default function AppNetworkProxyPanel({ root, installation, ready, onChanged }: {
  root: string
  installation: string
  granted: boolean
  ready: boolean
  onChanged: () => void
}) {
  const { t } = useTranslation()
  const route = `${root}/installations/${installation}/network-proxy`
  const [state, setState] = useState<State | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [dialogError, setDialogError] = useState('')
  const [hostsText, setHostsText] = useState('')
  const [gib, setGib] = useState<number | null>(null)
  const [plan, setPlan] = useState<Plan | null>(null)
  const [reload, setReload] = useState(0)

  const load = useCallback(async () => {
    try {
      const value = await api.get<State>(route)
      setState(value)
      const source = value.policy
      setHostsText((source ? source.hosts : value.declared_hosts).join('\n'))
      setGib((source ? source.daily_bytes : value.max_daily_bytes) / GIB)
      setError('')
    } catch (failure) {
      setError(errText(failure, t))
    }
  }, [route, t])
  // `load` changes identity with `t`; only the route and an explicit refresh re-read the state.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { void load() }, [route, reload])

  const maxGib = state ? state.max_daily_bytes / GIB : 0
  const overBudget = !!state && gib != null && gib > maxGib
  const hosts = parseHosts(hostsText)
  const dailyBytes = gib == null ? 0 : Math.round(gib * GIB)

  const requestPlan = async (list: string[], bytes: number) => {
    if (busy || !state) return
    setBusy(true); setError(''); setDialogError('')
    try {
      const value = await api.post<Plan>(`${route}/plan`, {
        policyRevision: state.policy_revision,
        previousPolicySha256: state.previous_policy_sha256,
        hosts: list,
        dailyBytes: bytes,
      })
      setPlan(value)
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }
  const review = () => {
    if (!state || overBudget || hosts.length === 0 || !gib || gib <= 0) return
    void requestPlan(hosts, dailyBytes)
  }
  const withdraw = () => { if (state) void requestPlan([], state.policy?.daily_bytes ?? state.max_daily_bytes) }
  const consume = async () => {
    if (busy || !plan) return
    setBusy(true); setDialogError('')
    try {
      await api.post(`${root}/network-proxy-reviews/${plan.plan_id}`, { sha256: plan.plan_sha256 })
      setPlan(null)
      setReload((value) => value + 1)
      onChanged()
    } catch (failure) {
      setDialogError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }
  const cancel = () => { if (!busy) { setPlan(null); setDialogError('') } }

  const today = state?.today
  const percent = today && today.budget > 0 ? Math.min(100, (today.bytes / today.budget) * 100) : 0
  const entryLine = (item: DiffEntry, sign: string) => <li key={`${sign}${item.entry}`}>
    <Typography.Text code>{sign} {item.entry}</Typography.Text>
    {item.wildcard && <> <StatusTag tone="warning">{t('appPlatform.networkProxy.wildcard')}</StatusTag></>}
    {item.wildcard && sign === '+' && <div><Typography.Text type="warning">{t('appPlatform.networkProxy.wildcardWarning', { base: item.entry.slice(2) })}</Typography.Text></div>}
  </li>

  return <Space orientation="vertical" size={8} style={{ width: '100%', marginTop: 12 }}>
    <Typography.Text type="secondary">{t('appPlatform.networkProxy.title')}</Typography.Text>
    {error && !plan && <Alert type="error" showIcon title={error} />}
    {state && <>
      {state.granted && state.policy && <Space orientation="vertical" size={4}>
        <Typography.Text strong>{t('appPlatform.networkProxy.reviewedHosts')}</Typography.Text>
        <ul style={{ margin: 0, paddingInlineStart: 20 }}>
          {state.policy.hosts.map((host) => <li key={host}>
            <Typography.Text code>{host}</Typography.Text>
            {isWildcard(host) && <> <StatusTag tone="warning">{t('appPlatform.networkProxy.wildcard')}</StatusTag> <Typography.Text type="secondary">{t('appPlatform.networkProxy.wildcardWarning', { base: host.slice(2) })}</Typography.Text></>}
          </li>)}
        </ul>
      </Space>}
      {!state.granted && <Space orientation="vertical" size={4}>
        <Typography.Text strong>{t('appPlatform.networkProxy.suggestions')}</Typography.Text>
        <Typography.Text type="secondary">{t('appPlatform.networkProxy.suggestionsHint')}</Typography.Text>
        <ul style={{ margin: 0, paddingInlineStart: 20 }}>
          {state.declared_hosts.map((host) => <li key={host}>
            <Typography.Text code>{host}</Typography.Text>
            {isWildcard(host) && <> <StatusTag tone="warning">{t('appPlatform.networkProxy.wildcard')}</StatusTag></>}
          </li>)}
        </ul>
      </Space>}
      <Typography.Text>{t('appPlatform.networkProxy.hostsLabel')}</Typography.Text>
      <Input.TextArea aria-label={t('appPlatform.networkProxy.hostsLabel')} rows={4} value={hostsText} disabled={!ready} onChange={(event) => setHostsText(event.target.value)} />
      <Space wrap>
        <label>{t('appPlatform.networkProxy.budgetLabel')} <InputNumber aria-label={t('appPlatform.networkProxy.budgetLabel')} min={0.001} step={0.5} value={gib} disabled={!ready} onChange={(value) => setGib(value == null ? null : Number(value))} /></label>
        <Typography.Text type="secondary">{t('appPlatform.networkProxy.budgetMax', { max: formatBytes(state.max_daily_bytes) })}</Typography.Text>
      </Space>
      {overBudget && <Alert type="error" showIcon title={t('appPlatform.networkProxy.budgetTooLarge', { max: formatBytes(state.max_daily_bytes) })} />}
      <Space wrap>
        <Button type="primary" loading={busy && !plan} disabled={!ready || overBudget || hosts.length === 0 || !gib || gib <= 0} onClick={review}>{state.granted ? t('appPlatform.networkProxy.review') : t('appPlatform.networkProxy.enable')}</Button>
        {state.granted && <Button loading={busy && !plan} disabled={!ready} onClick={withdraw}>{t('appPlatform.networkProxy.withdraw')}</Button>}
      </Space>
      {!ready && <Typography.Text type="secondary">{t('appPlatform.networkProxy.notReady')}</Typography.Text>}
      <Typography.Text strong>{t('appPlatform.networkProxy.today')}</Typography.Text>
      {today?.reported
        ? <>
          <Progress percent={Math.round(percent * 10) / 10} format={() => `${formatBytes(today.bytes)} / ${formatBytes(today.budget)}`} />
        </>
        : <Typography.Text type="secondary">{t('appPlatform.networkProxy.noData')}</Typography.Text>}
      <Typography.Text strong>{t('appPlatform.networkProxy.rollups')}</Typography.Text>
      <Table<Rollup>
        size="small"
        pagination={false}
        rowKey={(row) => `${row.hour}|${row.host}`}
        dataSource={state.rollups}
        locale={{ emptyText: t('appPlatform.networkProxy.rollupsEmpty') }}
        scroll={{ x: 'max-content' }}
        columns={[
          { title: t('appPlatform.networkProxy.col.hour'), dataIndex: 'hour', render: (value: string) => `${value.replace('T', ' ').slice(0, 16)} UTC` },
          { title: t('appPlatform.networkProxy.col.host'), dataIndex: 'host' },
          { title: t('appPlatform.networkProxy.col.tunnels'), dataIndex: 'tunnels' },
          { title: t('appPlatform.networkProxy.col.bytesIn'), dataIndex: 'bytes_in', render: (value: number) => formatBytes(value) },
          { title: t('appPlatform.networkProxy.col.bytesOut'), dataIndex: 'bytes_out', render: (value: number) => formatBytes(value) },
          {
            title: t('appPlatform.networkProxy.col.closes'),
            dataIndex: 'closes',
            render: (value: Record<string, number>) => CLOSE_REASONS.filter((reason) => (value?.[reason] ?? 0) > 0)
              .map((reason) => `${t(`appPlatform.networkProxy.close.${reason}`)} ${value[reason]}`).join(', ') || '—',
          },
        ]}
      />
    </>}
    <Modal
      className="rp-platform-review-modal"
      open={!!plan}
      title={t('appPlatform.networkProxy.reviewTitle')}
      okText={t('appPlatform.networkProxy.confirm')}
      cancelText={t('common.cancel')}
      confirmLoading={busy}
      closable={!busy}
      mask={{ closable: false }}
      onOk={() => void consume()}
      onCancel={cancel}
    >
      {plan && <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        {plan.diff.added.length > 0 && <div>
          <Typography.Text strong>{t('appPlatform.networkProxy.added')}</Typography.Text>
          <ul style={{ margin: 0, paddingInlineStart: 20 }}>{plan.diff.added.map((item) => entryLine(item, '+'))}</ul>
        </div>}
        {plan.diff.removed.length > 0 && <div>
          <Typography.Text strong>{t('appPlatform.networkProxy.removed')}</Typography.Text>
          <ul style={{ margin: 0, paddingInlineStart: 20 }}>{plan.diff.removed.map((item) => entryLine(item, '-'))}</ul>
        </div>}
        {plan.diff.daily_bytes.old !== plan.diff.daily_bytes.new && <Typography.Text>
          {t('appPlatform.networkProxy.budgetChange', { old: formatBytes(plan.diff.daily_bytes.old), new: formatBytes(plan.diff.daily_bytes.new) })}
        </Typography.Text>}
        {dialogError && <Alert type="error" showIcon title={dialogError} />}
      </Space>}
    </Modal>
  </Space>
}
