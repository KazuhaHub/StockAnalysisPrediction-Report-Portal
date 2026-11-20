import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Card, Input, InputNumber, Modal, Select, Space, Table, Tag, Typography } from 'antd'
import { DeleteOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errText } from '../../api/client'

// The administrator's side of the PostgreSQL liveness probe (doc 178 section 6). The reviewed
// targets, the private-network allowlist match and the disclosure are shown before anything is
// granted. There is no way to skip certificate verification: a private CA is pinned by its PEM.

type Network = 'public' | 'private'

interface TargetView {
  id: string
  host: string
  port: number
  database: string
  network: Network
  ca_sha256?: string
  label: string
  network_target_sha256: string
}

interface Preview {
  policy_revision: string
  declared: boolean
  granted: boolean
  ready: boolean
  previous_policy_sha256: string
  targets: TargetView[]
  deadline_ms: number
  concurrency: number
  private_networks_sha256: string
  disclosure: string
}

interface PlanTarget extends TargetView {
  allowlist: { prefix: string; ports: number[]; plain_http?: boolean } | null
}

interface Plan {
  plan_id: string
  plan_sha256: string
  expires_ms: string
  policy_revision: string
  diff: {
    granted: boolean
    previous_granted: boolean
    targets: PlanTarget[]
    deadline_ms: number
    concurrency: number
    previous_policy_sha256: string
    policy_sha256: string
    private_networks_sha256: string
    disclosure: string
  }
}

interface FormTarget {
  id: string
  host: string
  port: number | null
  database: string
  network: Network
  caPem: string
}

const MAX_CA_PEM = 4096
const DEFAULT_DEADLINE_MS = 10000
const DATABASE = /^[A-Za-z0-9_-]{1,63}$/

const blankTarget = (): FormTarget => ({ id: '', host: '', port: 5432, database: '', network: 'public', caPem: '' })

const fromView = (item: TargetView): FormTarget => ({
  id: item.id, host: item.host, port: item.port, database: item.database, network: item.network, caPem: '',
})

const validTarget = (item: FormTarget): boolean =>
  item.id.trim() !== '' && item.host.trim() !== '' && DATABASE.test(item.database) &&
  item.port !== null && Number.isInteger(item.port) && item.port >= 1 && item.port <= 65535 &&
  item.caPem.length <= MAX_CA_PEM

const shortDigest = (value: string) => value.slice(0, 12)

export default function AppPostgresPolicyPanel({ root, installation, ready, onChanged }: {
  root: string
  installation: string
  ready: boolean
  onChanged?: () => void
}) {
  const { t } = useTranslation()
  const route = `${root}/installations/${installation}/postgres-policy`
  const [preview, setPreview] = useState<Preview | null>(null)
  const [loadError, setLoadError] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [editing, setEditing] = useState(false)
  const [targets, setTargets] = useState<FormTarget[]>([])
  const [deadline, setDeadline] = useState<number>(DEFAULT_DEADLINE_MS)
  const [concurrency, setConcurrency] = useState<number>(1)
  const [plan, setPlan] = useState<Plan | null>(null)

  const refresh = useCallback(async () => {
    try {
      setPreview(await api.get<Preview>(route))
      setLoadError('')
    } catch (failure) {
      setLoadError(errText(failure, t))
    }
  }, [route, t])

  useEffect(() => {
    void refresh()
  }, [refresh])

  if (!preview) return loadError ? <Alert type="error" showIcon title={loadError} style={{ marginTop: 12 }} /> : null
  if (!preview.declared) return null

  const editable = ready && preview.ready
  const disclosure = t('appPostgres.disclosure', { defaultValue: preview.disclosure })

  const openEdit = () => {
    setError('')
    setTargets(preview.targets.length ? preview.targets.map(fromView) : [blankTarget()])
    setDeadline(preview.deadline_ms || DEFAULT_DEADLINE_MS)
    setConcurrency(preview.concurrency || 1)
    setEditing(true)
  }

  const requestPlan = async (body: Record<string, unknown>, fromEdit: boolean) => {
    if (busy) return
    setBusy(true); setError('')
    try {
      setPlan(await api.post<Plan>(`${route}/plan`, body))
    } catch (failure) {
      const text = errText(failure, t)
      if (fromEdit) setError(text)
      else setLoadError(text)
    } finally {
      setBusy(false)
    }
  }

  const review = () => {
    void requestPlan({
      policyRevision: preview.policy_revision,
      previousPolicySha256: preview.previous_policy_sha256,
      targets: targets.map((item) => {
        const pem = item.caPem.trim()
        return {
          id: item.id.trim(), host: item.host.trim(), port: item.port as number, database: item.database,
          network: item.network, ...(pem ? { caPem: pem } : {}),
        }
      }),
      deadlineMs: deadline,
      concurrency,
    }, true)
  }

  // Withdrawal is a plan with no targets; the body carries nothing else.
  const withdraw = () => {
    setLoadError('')
    void requestPlan({
      policyRevision: preview.policy_revision,
      previousPolicySha256: preview.previous_policy_sha256,
      targets: [],
    }, false)
  }

  const closeReview = () => {
    if (busy) return
    setPlan(null)
    setError('')
  }

  const confirm = async () => {
    if (!plan || busy) return
    setBusy(true); setError('')
    try {
      await api.post(`${route}/consume`, { plan_id: plan.plan_id, plan_sha256: plan.plan_sha256 })
      setPlan(null)
      setEditing(false)
      await refresh()
      onChanged?.()
    } catch (failure) {
      setError(errText(failure, t))
    } finally {
      setBusy(false)
    }
  }

  const update = (index: number, patch: Partial<FormTarget>) =>
    setTargets((current) => current.map((item, i) => (i === index ? { ...item, ...patch } : item)))

  const formValid = targets.length > 0 && targets.every(validTarget) &&
    new Set(targets.map((item) => item.id.trim())).size === targets.length

  const networkTag = (network: Network) => <Tag>{t(`appPostgres.network.${network}`)}</Tag>

  return <Card
    size="small"
    style={{ marginTop: 12 }}
    title={t('appPostgres.title')}
    extra={<Space wrap>
      <Button disabled={!editable} onClick={openEdit}>{t('appPostgres.edit')}</Button>
      {preview.granted && <Button loading={busy && !editing && !plan} onClick={withdraw}>{t('appPostgres.withdraw')}</Button>}
    </Space>}
  >
    <Space orientation="vertical" size={8} style={{ width: '100%' }}>
      <Alert type="info" showIcon title={disclosure} />
      {loadError && <Alert type="error" showIcon title={loadError} />}
      <Table<TargetView>
        rowKey="id"
        size="small"
        pagination={false}
        dataSource={preview.targets}
        locale={{ emptyText: t('appPostgres.noTargets') }}
        columns={[
          { title: t('appPostgres.target'), dataIndex: 'label' },
          { title: t('appPostgres.networkClass'), dataIndex: 'network', render: (network: Network) => networkTag(network) },
          { title: t('appPostgres.ca'), dataIndex: 'ca_sha256', render: (value?: string) => value ? <Typography.Text code>{shortDigest(value)}</Typography.Text> : null },
        ]}
      />
      {!editable && <Typography.Text type="secondary">{t('appPostgres.notReady')}</Typography.Text>}
    </Space>

    <Modal
      className="rp-platform-review-modal"
      open={editing}
      title={t('appPostgres.editTitle')}
      okText={t('appPostgres.review')}
      cancelText={t('common.cancel')}
      okButtonProps={{ disabled: !formValid || !editable }}
      confirmLoading={busy && !plan}
      closable={!busy}
      mask={{ closable: false }}
      onOk={review}
      onCancel={() => { if (!busy) { setEditing(false); setError('') } }}
      width={720}
    >
      <Space orientation="vertical" size={12} style={{ width: '100%' }}>
        {targets.map((item, index) => <Card key={index} size="small" title={`${t('appPostgres.target')} ${index + 1}`} extra={targets.length > 1
          ? <Button size="small" aria-label={t('appPostgres.removeTarget')} icon={<DeleteOutlined aria-hidden="true" />} onClick={() => setTargets((current) => current.filter((_, i) => i !== index))} />
          : null}>
          <Space orientation="vertical" size={8} style={{ width: '100%' }}>
            <Space wrap>
              <Input aria-label={t('appPostgres.id')} placeholder={t('appPostgres.id')} maxLength={64} autoComplete="off" value={item.id} onChange={(event) => update(index, { id: event.target.value })} />
              <Input aria-label={t('appPostgres.host')} placeholder={t('appPostgres.host')} autoComplete="off" value={item.host} onChange={(event) => update(index, { host: event.target.value })} />
              <InputNumber aria-label={t('appPostgres.port')} min={1} max={65535} precision={0} value={item.port} onChange={(value) => update(index, { port: value === null ? null : Number(value) })} />
              <Input aria-label={t('appPostgres.database')} placeholder={t('appPostgres.database')} maxLength={63} autoComplete="off" value={item.database} onChange={(event) => update(index, { database: event.target.value })} />
              <Select
                aria-label={t('appPostgres.networkClass')}
                value={item.network}
                style={{ minWidth: 120 }}
                onChange={(value: Network) => update(index, { network: value })}
                options={(['public', 'private'] as Network[]).map((network) => ({ value: network, label: t(`appPostgres.network.${network}`) }))}
              />
            </Space>
            <Input.TextArea
              aria-label={t('appPostgres.caPem')}
              placeholder={t('appPostgres.caPem')}
              rows={3}
              maxLength={MAX_CA_PEM}
              autoComplete="off"
              value={item.caPem}
              onChange={(event) => update(index, { caPem: event.target.value })}
            />
            <Typography.Text type="secondary">{item.caPem === '' && preview.targets.some((view) => view.id === item.id && view.ca_sha256)
              ? t('appPostgres.caReenter')
              : t('appPostgres.caHint')}</Typography.Text>
          </Space>
        </Card>)}
        <Button icon={<PlusOutlined aria-hidden="true" />} onClick={() => setTargets((current) => [...current, blankTarget()])}>{t('appPostgres.addTarget')}</Button>
        <Space wrap>
          <label>{t('appPostgres.deadline')} <InputNumber min={1} max={DEFAULT_DEADLINE_MS} precision={0} value={deadline} onChange={(value) => setDeadline(Number(value ?? DEFAULT_DEADLINE_MS))} /></label>
          <label>{t('appPostgres.concurrency')} <Select
            value={concurrency}
            style={{ minWidth: 80 }}
            onChange={(value: number) => setConcurrency(value)}
            options={[{ value: 1, label: '1' }, { value: 2, label: '2' }]}
          /></label>
        </Space>
        {error && !plan && <Alert type="error" showIcon title={error} />}
      </Space>
    </Modal>

    <Modal
      className="rp-platform-review-modal"
      open={!!plan}
      title={plan && !plan.diff.granted ? t('appPostgres.withdrawTitle') : t('appPostgres.confirmTitle')}
      okText={plan && !plan.diff.granted ? t('appPostgres.confirmWithdraw') : t('appPostgres.confirm')}
      cancelText={t('common.cancel')}
      confirmLoading={busy}
      closable={!busy}
      mask={{ closable: false }}
      onOk={() => void confirm()}
      onCancel={closeReview}
      width={720}
    >
      {plan && <Space orientation="vertical" size={8} style={{ width: '100%' }}>
        <Alert type="warning" showIcon title={t('appPostgres.disclosure', { defaultValue: plan.diff.disclosure })} />
        {plan.diff.targets.length === 0 && <Typography.Text>{t('appPostgres.withdrawBody')}</Typography.Text>}
        {plan.diff.targets.map((item) => <Space key={item.id} orientation="vertical" size={2} style={{ width: '100%' }}>
          <Space wrap>
            <Typography.Text strong>{item.label}</Typography.Text>
            {networkTag(item.network)}
          </Space>
          {item.allowlist && <Typography.Text type="secondary">
            {t('appPostgres.allowlist', { prefix: item.allowlist.prefix, ports: item.allowlist.ports.join(', ') })}
          </Typography.Text>}
          {item.ca_sha256 && <Typography.Text type="secondary">
            {t('appPostgres.caFingerprint', { fingerprint: shortDigest(item.ca_sha256) })}
          </Typography.Text>}
        </Space>)}
        {plan.diff.targets.length > 0 && <Typography.Text type="secondary">
          {t('appPostgres.limits', { deadline: plan.diff.deadline_ms, concurrency: plan.diff.concurrency })}
        </Typography.Text>}
        {error && <Alert type="error" showIcon title={error} />}
      </Space>}
    </Modal>
  </Card>
}
