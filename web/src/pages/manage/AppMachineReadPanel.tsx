import { useCallback, useEffect, useState } from 'react'
import { confirmAction } from '../../components/Confirm'
import { Alert, App, Button, Checkbox, Input, InputNumber, Modal, Space, Table, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'

// The administrator's side of the machine read API (doc 183 section 6.4). The disclosure is shown
// before anything is enabled, the choice of routes and limits narrows what the release declared, and
// a token is created, rotated and revoked here and nowhere else. A token's secret is shown once, in a
// dialog that forgets it when it closes; the list never has it.

type MachineRoute = { id: string; path: string; query?: string[] | null; response_bytes: number; timeout_ms: number; description?: string }
type Preview = {
  policy_revision: string
  granted: boolean
  ready: boolean
  declared: { routes: MachineRoute[] }
  policy: { routes: string[]; installation_per_second: number; installation_concurrency: number }
  openapi_path: string
  disclosure: string
  disclosure_sha256: string
  runner_support: 'supported' | 'unsupported' | 'unknown'
}
type Token = {
  handle: string
  label: string
  routes: string[]
  perMinute: number
  sourceCidrs: string[]
  state: 'active' | 'expired' | 'revoked'
  createdBy: string
  createdAt: string
  expiresAt: string
  lastUsedAt: string | null
  revokedAt: string | null
}
type Usage = { at: string; action: string; detail: Record<string, unknown> }
type TokenList = { tokens: Token[]; usage: Usage[]; now: string }
type Created = { token: string; tokenInfo: Token; openapiPath: string }

export default function AppMachineReadPanel({ root, installation, granted, ready, onChanged }: {
  root: string
  installation: string
  granted: boolean
  ready: boolean
  onChanged: () => void
}) {
  const { modal } = App.useApp()
  const { t, i18n } = useTranslation()
  const base = `${root}/installations/${installation}`
  const grantRoute = `${base}/machine-read`
  const tokenRoute = `${base}/machine-tokens`
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [consent, setConsent] = useState(false)
  const [preview, setPreview] = useState<Preview | null>(null)
  const [routes, setRoutes] = useState<string[]>([])
  const [perSecond, setPerSecond] = useState(5)
  const [concurrency, setConcurrency] = useState(4)
  const [list, setList] = useState<TokenList | null>(null)
  const [creating, setCreating] = useState(false)
  const [label, setLabel] = useState('')
  const [tokenRoutes, setTokenRoutes] = useState<string[]>([])
  const [perMinute, setPerMinute] = useState(60)
  const [cidrs, setCidrs] = useState('')
  const [days, setDays] = useState(90)
  const [rotating, setRotating] = useState<Token | null>(null)
  const [grace, setGrace] = useState(1)
  const [shown, setShown] = useState<Created | null>(null)
  const format = (value: string | null) => value ? new Intl.DateTimeFormat(i18n.language, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value)) : '—'

  const load = useCallback(async () => {
    try {
      setList(await api.get<TokenList>(tokenRoute))
    } catch (failure) {
      setError(errText(failure, t))
    }
  }, [tokenRoute, t])
  useEffect(() => { if (granted) void load(); else setList(null) }, [granted, load])

  const guarded = async (run: () => Promise<void>) => {
    if (busy) return
    setBusy(true); setError('')
    try {
      await run()
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }
  const showConsent = () => guarded(async () => {
    const value = await api.get<Preview>(grantRoute)
    setPreview(value)
    setRoutes(value.policy.routes)
    setPerSecond(value.policy.installation_per_second)
    setConcurrency(value.policy.installation_concurrency)
    setConsent(true)
  })
  const enable = () => {
    if (!preview) return
    // Routes go back in the descriptor's order: the server accepts only the sorted list.
    const chosen = preview.declared.routes.map((item) => item.id).filter((id) => routes.includes(id))
    void guarded(async () => {
      await api.post(grantRoute, { enabled: true, policyRevision: preview.policy_revision, routes: chosen, installationPerSecond: perSecond, installationConcurrency: concurrency, disclosureSha256: preview.disclosure_sha256 })
      setConsent(false)
      onChanged()
    })
  }
  const disable = () => guarded(async () => {
    // The revision comes from a fresh read, so a withdrawal is never refused for a stale page.
    const value = await api.get<Preview>(grantRoute)
    await api.post(grantRoute, { enabled: false, policyRevision: value.policy_revision, routes: null, installationPerSecond: null, installationConcurrency: null, disclosureSha256: null })
    onChanged()
  })
  const openCreate = () => guarded(async () => {
    const value = await api.get<Preview>(grantRoute)
    setPreview(value)
    setLabel(''); setTokenRoutes(value.policy.routes); setPerMinute(60); setCidrs(''); setDays(90)
    setCreating(true)
  })
  const create = () => guarded(async () => {
    const chosen = (preview?.declared.routes ?? []).map((item) => item.id).filter((id) => tokenRoutes.includes(id))
    const sourceCidrs = cidrs.split(/[\s,]+/).filter(Boolean)
    const made = await api.post<Created>(tokenRoute, { label: label.trim(), routes: chosen, perMinute, sourceCidrs, expiresInDays: days })
    setCreating(false)
    setShown(made)
    await load()
  })
  const rotate = () => guarded(async () => {
    if (!rotating) return
    const made = await api.post<Created>(`${tokenRoute}/${rotating.handle}/rotate`, { graceDays: grace, expiresInDays: days })
    setRotating(null)
    setShown(made)
    await load()
  })
  const revoke = (token: Token) => confirmAction(modal, {
    title: t('appPlatform.settings.machine.revokeTitle', { label: token.label }),
    description: t('appPlatform.settings.machine.revokeBody'),
    okText: t('appPlatform.settings.machine.revoke'),
    onOk: () => guarded(async () => {
      await api.post(`${tokenRoute}/${token.handle}/revoke`, {})
      await load()
    }),
  }, t)
  const openapi = shown ? `${window.location.origin}${shown.openapiPath}` : ''
  const policyRoutes = preview?.policy.routes ?? []
  const declared = preview?.declared.routes ?? []

  return <Space orientation="vertical" size={8} style={{ width: '100%', marginTop: 12 }}>
    <Typography.Text type="secondary">{t('appPlatform.capability.machine.read')}</Typography.Text>
    {error && !consent && !creating && !rotating && <Alert type="error" showIcon title={error} />}
    <Space wrap>
      <Button loading={busy && !consent} disabled={!ready} onClick={() => void showConsent()}>{granted ? t('appPlatform.settings.machine.edit') : t('appPlatform.settings.machine.enable')}</Button>
      {granted && <Button loading={busy && !consent} onClick={() => void disable()}>{t('appPlatform.settings.machine.disable')}</Button>}
      {granted && <Button type="primary" disabled={!ready} onClick={() => void openCreate()}>{t('appPlatform.settings.machine.create')}</Button>}
    </Space>
    {!ready && <Typography.Text type="secondary">{t('appPlatform.settings.machine.notReady')}</Typography.Text>}
    {granted && list && <Table<Token>
      size="small"
      rowKey="handle"
      pagination={false}
      dataSource={list.tokens}
      locale={{ emptyText: t('appPlatform.settings.machine.noTokens') }}
      scroll={{ x: true }}
      columns={[
        { title: t('appPlatform.settings.machine.col.label'), dataIndex: 'label' },
        { title: t('appPlatform.settings.machine.col.handle'), dataIndex: 'handle', render: (value: string) => <Typography.Text code>{value}</Typography.Text> },
        { title: t('appPlatform.settings.machine.col.routes'), dataIndex: 'routes', render: (value: string[]) => value.join(', ') },
        { title: t('appPlatform.settings.machine.col.limit'), dataIndex: 'perMinute' },
        { title: t('appPlatform.settings.machine.col.sources'), dataIndex: 'sourceCidrs', render: (value: string[]) => value.length ? value.join(', ') : t('appPlatform.settings.machine.anySource') },
        { title: t('appPlatform.settings.machine.col.createdBy'), dataIndex: 'createdBy' },
        { title: t('appPlatform.settings.machine.col.created'), dataIndex: 'createdAt', render: format },
        { title: t('appPlatform.settings.machine.col.expires'), dataIndex: 'expiresAt', render: format },
        { title: t('appPlatform.settings.machine.col.lastUsed'), dataIndex: 'lastUsedAt', render: (value: string | null) => value ? format(value) : t('appPlatform.settings.machine.never') },
        { title: t('appPlatform.settings.machine.col.state'), dataIndex: 'state', render: (value: string) => t(`appPlatform.settings.machine.state.${value}`) },
        {
          title: '', key: 'actions', render: (_: unknown, token: Token) => token.state === 'active' && <Space>
            <Button size="small" disabled={!ready} onClick={() => { setRotating(token); setGrace(1); setDays(90) }}>{t('appPlatform.settings.machine.rotate')}</Button>
            <Button size="small" danger onClick={() => revoke(token)}>{t('appPlatform.settings.machine.revoke')}</Button>
          </Space>,
        },
      ]}
    />}
    {granted && list && list.usage.length > 0 && <div>
      <Typography.Text strong>{t('appPlatform.settings.machine.usage')}</Typography.Text>
      <ul style={{ margin: '4px 0 0', paddingInlineStart: 20 }}>
        {list.usage.slice(0, 10).map((row, index) => <li key={index}>
          <Typography.Text type="secondary">{format(row.at)}</Typography.Text>{' '}
          {row.action.endsWith('machine_token_source')
            ? t('appPlatform.settings.machine.usageSource', { handle: String(row.detail.handle ?? ''), address: String(row.detail.address ?? '') })
            : t('appPlatform.settings.machine.usageSummary', { handle: String(row.detail.handle ?? ''), requests: Number(row.detail.requests ?? 0), calls: Number(row.detail.backend_calls ?? 0), bytes: Number(row.detail.bytes_out ?? 0) })}
        </li>)}
      </ul>
    </div>}

    <Modal
      className="rp-platform-review-modal"
      open={consent}
      title={t('appPlatform.settings.machine.title')}
      okText={t('appPlatform.settings.machine.confirm')}
      cancelText={t('common.cancel')}
      okButtonProps={{ disabled: routes.length === 0 || preview?.runner_support === 'unsupported' }}
      confirmLoading={busy}
      closable={!busy}
      mask={{ closable: false }}
      onOk={enable}
      onCancel={() => { if (!busy) { setConsent(false); setError('') } }}
    >
      {preview && <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Alert type="warning" showIcon title={t('appPlatform.capability.machine.read')} />
        {preview.runner_support === 'unsupported' && <Alert type="error" showIcon title={t('appPlatform.settings.machine.runnerUnsupported')} />}
        <Checkbox.Group value={routes} onChange={(value) => setRoutes(value as string[])}>
          <Space orientation="vertical">
            {declared.map((item) => <Checkbox key={item.id} value={item.id}>
              {t('appPlatform.settings.machine.route', { id: item.id, path: item.path })}
            </Checkbox>)}
          </Space>
        </Checkbox.Group>
        <Space wrap>
          <label>{t('appPlatform.settings.machine.perSecond')} <InputNumber min={1} max={50} value={perSecond} onChange={(value) => setPerSecond(Number(value ?? 5))} /></label>
          <label>{t('appPlatform.settings.machine.concurrency')} <InputNumber min={1} max={16} value={concurrency} onChange={(value) => setConcurrency(Number(value ?? 4))} /></label>
        </Space>
        <Typography.Text type="secondary">{t('appPlatform.settings.machine.hint')}</Typography.Text>
        {error && <Alert type="error" showIcon title={error} />}
      </Space>}
    </Modal>

    <Modal
      className="rp-platform-review-modal"
      open={creating}
      title={t('appPlatform.settings.machine.createTitle')}
      okText={t('appPlatform.settings.machine.createConfirm')}
      cancelText={t('common.cancel')}
      okButtonProps={{ disabled: !label.trim() || tokenRoutes.length === 0 }}
      confirmLoading={busy}
      closable={!busy}
      mask={{ closable: false }}
      onOk={create}
      onCancel={() => { if (!busy) { setCreating(false); setError('') } }}
    >
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <label>{t('appPlatform.settings.machine.labelField')}
          <Input value={label} maxLength={64} placeholder={t('appPlatform.settings.machine.labelPlaceholder')} onChange={(event) => setLabel(event.target.value)} />
        </label>
        <Checkbox.Group value={tokenRoutes} onChange={(value) => setTokenRoutes(value as string[])}>
          <Space orientation="vertical">
            {declared.filter((item) => policyRoutes.includes(item.id)).map((item) => <Checkbox key={item.id} value={item.id}>
              {t('appPlatform.settings.machine.route', { id: item.id, path: item.path })}
            </Checkbox>)}
          </Space>
        </Checkbox.Group>
        <Space wrap>
          <label>{t('appPlatform.settings.machine.perMinute')} <InputNumber min={1} max={600} value={perMinute} onChange={(value) => setPerMinute(Number(value ?? 60))} /></label>
          <label>{t('appPlatform.settings.machine.expiresDays')} <InputNumber min={1} max={365} value={days} onChange={(value) => setDays(Number(value ?? 90))} /></label>
        </Space>
        <label>{t('appPlatform.settings.machine.cidrs')}
          <Input.TextArea rows={2} value={cidrs} placeholder="203.0.113.0/24" onChange={(event) => setCidrs(event.target.value)} />
        </label>
        <Typography.Text type="secondary">{t('appPlatform.settings.machine.cidrsHint')}</Typography.Text>
        {error && <Alert type="error" showIcon title={error} />}
      </Space>
    </Modal>

    <Modal
      className="rp-platform-review-modal"
      open={!!rotating}
      title={t('appPlatform.settings.machine.rotateTitle', { label: rotating?.label ?? '' })}
      okText={t('appPlatform.settings.machine.rotate')}
      cancelText={t('common.cancel')}
      confirmLoading={busy}
      closable={!busy}
      mask={{ closable: false }}
      onOk={rotate}
      onCancel={() => { if (!busy) { setRotating(null); setError('') } }}
    >
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Typography.Text>{t('appPlatform.settings.machine.rotateHint')}</Typography.Text>
        <Space wrap>
          <label>{t('appPlatform.settings.machine.graceDays')} <InputNumber min={0} max={7} value={grace} onChange={(value) => setGrace(Number(value ?? 1))} /></label>
          <label>{t('appPlatform.settings.machine.expiresDays')} <InputNumber min={1} max={365} value={days} onChange={(value) => setDays(Number(value ?? 90))} /></label>
        </Space>
        {error && <Alert type="error" showIcon title={error} />}
      </Space>
    </Modal>

    <Modal
      className="rp-platform-review-modal"
      open={!!shown}
      title={t('appPlatform.settings.machine.shownTitle')}
      okText={t('appPlatform.settings.machine.done')}
      cancelButtonProps={{ style: { display: 'none' } }}
      closable={false}
      destroyOnHidden
      // No exit animation: the secret leaves the page the moment the dialog is dismissed.
      transitionName=""
      maskTransitionName=""
      mask={{ closable: false }}
      onOk={() => setShown(null)}
    >
      {shown && <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        <Alert type="warning" showIcon title={t('appPlatform.settings.machine.shownOnce')} />
        <Typography.Text code copyable style={{ wordBreak: 'break-all' }}>{shown.token}</Typography.Text>
        <Typography.Text>{t('appPlatform.settings.machine.openapiUrl')}</Typography.Text>
        <Typography.Text code copyable style={{ wordBreak: 'break-all' }}>{openapi}</Typography.Text>
        <Typography.Text type="secondary">{t('appPlatform.settings.machine.difyHint')}</Typography.Text>
      </Space>}
    </Modal>
  </Space>
}
