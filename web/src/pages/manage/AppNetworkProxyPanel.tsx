import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Input, InputNumber, Modal, Progress, Select, Space, Table, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'
import StatusTag from '../../components/StatusTag'

// The administrator's side of the reviewed forward proxy (doc 184 sections 5.2 and 7). The app
// declares the hosts it may reach; the administrator narrows or edits that list, sees the plan the
// server computed, and only a confirmed review consumes it. Withdrawal is the same flow with no hosts.
// An exact host may also be pinned (doc 186): the address and port are chosen from the entries the
// runner's operator lists for that name, never typed.

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
type Pin = { host: string; address: string; port: number }
type PinView = Pin & { permitted: boolean }
type PinChange = { host: string; old: Pin; new: Pin }
type State = {
  policy_revision: string
  granted: boolean
  ready: boolean
  declared_hosts: string[]
  policy: { hosts: string[]; pins: PinView[]; daily_bytes: number } | null
  runner: { available: boolean; pins_supported: boolean; pins: Pin[] }
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
    pins: { added: Pin[]; removed: Pin[]; changed: PinChange[] }
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
// A destination as it is written to people: address:port, with an IPv6 address in brackets.
const endpointOf = (pin: { address: string; port: number }) => `${pin.address.includes(':') ? `[${pin.address}]` : pin.address}:${pin.port}`
// The select's value for a destination; the separator cannot occur in an address.
const choiceOf = (pin: { address: string; port: number }) => `${pin.address}|${pin.port}`
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
  // The chosen destination per exact host name; no entry means the name is not pinned.
  const [choices, setChoices] = useState<Record<string, string>>({})
  const [plan, setPlan] = useState<Plan | null>(null)
  const [reload, setReload] = useState(0)

  const load = useCallback(async () => {
    try {
      const value = await api.get<State>(route)
      setState(value)
      const source = value.policy
      const pinned = source ? source.pins : []
      setHostsText((source ? Array.from(new Set([...source.hosts, ...pinned.map((pin) => pin.host)])).sort() : value.declared_hosts).join('\n'))
      setChoices(Object.fromEntries(pinned.map((pin) => [pin.host, choiceOf(pin)])))
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

  const runnerPins = state?.runner.pins ?? []
  // The names that can be pinned: exact hosts of the list that the runner lists a destination for, and any
  // that already carry a choice (a pin the operator has since removed stays visible so it can be undone).
  const pinnable = hosts.filter((host) => !isWildcard(host) && (runnerPins.some((pin) => pin.host === host) || choices[host]))
  const chosen = pinnable.flatMap((host) => {
    const key = choices[host]
    const pin = key ? runnerPins.find((item) => item.host === host && choiceOf(item) === key) ?? state?.policy?.pins.find((item) => item.host === host && choiceOf(item) === key) : undefined
    return pin ? [{ host, address: pin.address, port: pin.port }] : []
  })
  const pinnedHosts = new Set(chosen.map((pin) => pin.host))

  const requestPlan = async (list: string[], pins: Pin[], bytes: number) => {
    if (busy || !state) return
    setBusy(true); setError(''); setDialogError('')
    try {
      const value = await api.post<Plan>(`${route}/plan`, {
        policyRevision: state.policy_revision,
        previousPolicySha256: state.previous_policy_sha256,
        hosts: list,
        pins,
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
    void requestPlan(hosts.filter((host) => !pinnedHosts.has(host)), chosen, dailyBytes)
  }
  const withdraw = () => { if (state) void requestPlan([], [], state.policy?.daily_bytes ?? state.max_daily_bytes) }
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

  const pinLine = (pin: Pin, sign: string) => <li key={`${sign}${pin.host}`}>
    <Typography.Text code>{sign} {pin.host} → {endpointOf(pin)}</Typography.Text>
    {sign === '+' && <div><Typography.Text type="warning">{t('appPlatform.networkProxy.pin.warning', { host: pin.host, endpoint: endpointOf(pin) })}</Typography.Text></div>}
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
          {state.policy.pins.map((pin) => <li key={`pin:${pin.host}`}>
            <Typography.Text code>{pin.host}</Typography.Text>
            {' '}<StatusTag tone="neutral">{t('appPlatform.networkProxy.pin.tag', { endpoint: endpointOf(pin) })}</StatusTag>
            {!pin.permitted && <> <StatusTag tone="error">{t('appPlatform.networkProxy.pin.notPermitted')}</StatusTag> <Typography.Text type="secondary">{t('appPlatform.networkProxy.pin.notPermittedHint')}</Typography.Text></>}
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
      <Space orientation="vertical" size={4} style={{ width: '100%' }}>
        <Typography.Text strong>{t('appPlatform.networkProxy.pin.title')}</Typography.Text>
        <Typography.Text type="secondary">{t('appPlatform.networkProxy.pin.hint')}</Typography.Text>
        {runnerPins.length === 0 && pinnable.length === 0 && <Typography.Text type="secondary">{t('appPlatform.networkProxy.pin.unavailable')}</Typography.Text>}
        {pinnable.map((host) => {
          const options = runnerPins.filter((pin) => pin.host === host).map((pin) => ({ value: choiceOf(pin), label: endpointOf(pin), title: endpointOf(pin) }))
          const current = choices[host]
          const missing = current && !options.some((option) => option.value === current)
            ? state.policy?.pins.find((pin) => pin.host === host && choiceOf(pin) === current) : undefined
          if (missing) options.push({ value: choiceOf(missing), label: endpointOf(missing), title: endpointOf(missing) })
          return <Space key={host} wrap>
            <Typography.Text code>{host}</Typography.Text>
            <Select
              aria-label={t('appPlatform.networkProxy.pin.choice', { host })}
              style={{ minWidth: 220 }}
              allowClear
              disabled={!ready}
              placeholder={t('appPlatform.networkProxy.pin.notPinned')}
              options={options}
              value={current}
              onChange={(value?: string) => setChoices((previous) => {
                const next = { ...previous }
                if (value) next[host] = value
                else delete next[host]
                return next
              })}
            />
            {missing && <StatusTag tone="error">{t('appPlatform.networkProxy.pin.notPermitted')}</StatusTag>}
          </Space>
        })}
      </Space>
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
        {plan.diff.pins.added.length > 0 && <div>
          <Typography.Text strong>{t('appPlatform.networkProxy.pin.added')}</Typography.Text>
          <ul style={{ margin: 0, paddingInlineStart: 20 }}>{plan.diff.pins.added.map((pin) => pinLine(pin, '+'))}</ul>
        </div>}
        {plan.diff.pins.changed.length > 0 && <div>
          <Typography.Text strong>{t('appPlatform.networkProxy.pin.changed')}</Typography.Text>
          <ul style={{ margin: 0, paddingInlineStart: 20 }}>{plan.diff.pins.changed.map((change) => <li key={`~${change.host}`}>
            <Typography.Text code>{t('appPlatform.networkProxy.pin.changeLine', { host: change.host, old: endpointOf(change.old), new: endpointOf(change.new) })}</Typography.Text>
            <div><Typography.Text type="warning">{t('appPlatform.networkProxy.pin.warning', { host: change.host, endpoint: endpointOf(change.new) })}</Typography.Text></div>
          </li>)}</ul>
        </div>}
        {plan.diff.pins.removed.length > 0 && <div>
          <Typography.Text strong>{t('appPlatform.networkProxy.pin.removed')}</Typography.Text>
          <ul style={{ margin: 0, paddingInlineStart: 20 }}>{plan.diff.pins.removed.map((pin) => pinLine(pin, '-'))}</ul>
        </div>}
        {plan.diff.daily_bytes.old !== plan.diff.daily_bytes.new && <Typography.Text>
          {t('appPlatform.networkProxy.budgetChange', { old: formatBytes(plan.diff.daily_bytes.old), new: formatBytes(plan.diff.daily_bytes.new) })}
        </Typography.Text>}
        {dialogError && <Alert type="error" showIcon title={dialogError} />}
      </Space>}
    </Modal>
  </Space>
}
